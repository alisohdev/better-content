import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { writeFile } from "node:fs/promises";
import { createServer as httpServer } from "node:http";
import { createServer as httpsServer } from "node:https";
import { once } from "node:events";
import { setTimeout as delay } from "node:timers/promises";
import {
  NodeControlledRemoteTransport,
  remoteAcquisitionLimits,
} from "@/modules/assets/infrastructure/controlled-remote-acquisition";
import {
  avalAIClientConfiguration,
  createAvalAIResponsesClient,
} from "@/modules/ai/infrastructure/avalai/avalai-generate-ideas-provider";
import {
  AVALAI_CONTENT_SCRIPT_TIMEOUT_MS,
  AVALAI_CONTENT_SCRIPT_MAX_RETRIES,
} from "@/modules/ai/infrastructure/avalai/avalai-generate-content-script-provider";
import { CreateBucketCommand, S3Client } from "@aws-sdk/client-s3";
import OpenAI from "openai";

const runtime = "bun" in process.versions ? "bun" : "node";
const mode = process.argv[2];
const report: Record<string, unknown> = { runtime, versions: process.versions };
const fetchOriginal = globalThis.fetch;

try {
  if (mode === "tls") {
    let seenSni: string | undefined;
    const server = httpsServer(
      {
        key: readFileSync("/tmp/spike.key"),
        cert: readFileSync("/tmp/spike.crt"),
      },
      (request, response) => {
        const socket = request.socket as import("node:tls").TLSSocket;
        if ("servername" in socket && typeof socket.servername === "string")
          seenSni = socket.servername;
        assert.equal(request.headers.host, "media.spike.invalid");
        response.end("spike media");
      },
    );
    server.listen(443, "127.0.0.2");
    await once(server, "listening");
    try {
      // Exercise the actual transport after resolution, solely against the private local fixture.
      const result = await new NodeControlledRemoteTransport().request({
        url: new URL("https://media.spike.invalid/asset"),
        servername: "media.spike.invalid",
        address: { address: "127.0.0.2", family: 4 },
        limits: remoteAcquisitionLimits,
      });
      assert.equal(result.statusCode, 200);
      const body: Uint8Array[] = [];
      for await (const chunk of result.body) body.push(chunk);
      assert.equal(Buffer.concat(body).toString(), "spike media");
      assert.equal(seenSni, "media.spike.invalid");
      report.tls = { approvedFixtureAddress: "127.0.0.2", seenSni, status: 200 };
    } finally {
      server.closeAllConnections();
      server.close();
    }
  } else if (mode === "s3") {
    assert.equal(process.env.ASSET_S3_ENDPOINT, "http://s3:9000");
    const client = new S3Client({
      endpoint: process.env.ASSET_S3_ENDPOINT,
      region: process.env.ASSET_S3_REGION,
      forcePathStyle: true,
      credentials: {
        accessKeyId: process.env.ASSET_S3_ACCESS_KEY_ID!,
        secretAccessKey: process.env.ASSET_S3_SECRET_ACCESS_KEY!,
      },
      maxAttempts: 1,
    });
    try {
      for (let attempt = 1; ; attempt++) {
        try {
          await client.send(new CreateBucketCommand({ Bucket: "spike-private" }));
          break;
        } catch (error) {
          if (error instanceof Error && error.name === "BucketAlreadyOwnedByYou") break;
          if (attempt === 20) throw error;
          await delay(250);
        }
      }
    } finally {
      client.destroy();
    }
  } else if (mode === "ai") {
    const requests: Array<{ path: string; closedAtMs?: number }> = [];
    const start = performance.now();
    const server = httpServer((incoming, outgoing) => {
      const request: (typeof requests)[number] = { path: incoming.url! };
      requests.push(request);
      incoming.resume();
      outgoing.once("close", () => {
        request.closedAtMs = performance.now() - start;
      });
    });
    server.listen(3210, "127.0.0.1");
    await once(server, "listening");
    try {
      globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
        assert.equal(String(input), avalAIClientConfiguration.baseURL + "/responses");
        return fetchOriginal("http://127.0.0.1:3210/responses", init);
      }) as typeof fetch;
      const client = createAvalAIResponsesClient({
        AVALAI_API_KEY: "fixture-no-provider-credential",
        AI_SAFETY_IDENTIFIER_SECRET: "fixture-no-provider-secret",
      });
      const trials = [
        {
          name: "ideas",
          timeout: avalAIClientConfiguration.timeout,
          maxRetries: avalAIClientConfiguration.maxRetries,
        },
        {
          name: "content",
          timeout: AVALAI_CONTENT_SCRIPT_TIMEOUT_MS,
          maxRetries: AVALAI_CONTENT_SCRIPT_MAX_RETRIES,
        },
      ];
      const results = await Promise.all(
        trials.map(async (trial) => {
          const started = performance.now();
          let failure: string | undefined;
          try {
            await client.responses.create({ model: "fixture", input: "fixture" } as never, trial);
          } catch (error) {
            if (!(error instanceof Error)) throw error;
            failure =
              error instanceof OpenAI.APIConnectionTimeoutError
                ? error.constructor.name
                : error.name;
          }
          return { ...trial, failure, elapsedMs: performance.now() - started };
        }),
      );
      await delay(300);
      report.requests = requests;
      report.results = results;
      assert.equal(requests.length, 2);
      assert.ok(requests.every((request) => request.path === "/responses" && request.closedAtMs));
      for (const result of results) {
        assert.equal(result.failure, "APIConnectionTimeoutError");
        assert.equal(result.maxRetries, 0);
        assert.ok(
          result.elapsedMs >= result.timeout - 250 && result.elapsedMs < result.timeout + 5_000,
        );
      }
    } finally {
      globalThis.fetch = fetchOriginal;
      server.closeAllConnections();
      server.close();
    }
  } else {
    throw new Error("Run the tls, s3, or ai probe.");
  }
} catch (error) {
  report.error =
    error instanceof Error
      ? { name: error.name, message: error.message, cause: String(error.cause ?? "") }
      : String(error);
  throw error;
} finally {
  await writeFile(`/evidence/${runtime}-${mode}.json`, JSON.stringify(report, null, 2) + "\n");
}
