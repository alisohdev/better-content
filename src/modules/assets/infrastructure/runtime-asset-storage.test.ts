import { createReadStream } from "node:fs";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Readable } from "node:stream";
import { describe, expect, it, vi } from "vitest";

import { FilesystemAssetStorage } from "./filesystem-asset-storage";
import { createRuntimeAssetStorage } from "./runtime-asset-storage";
import { createPermanentStorageKey } from "./storage-keys";
import {
  S3CompatibleAssetStorage,
  type S3CompatiblePrivateObjectClient,
} from "./s3-compatible-asset-storage";

function fakeS3Client(): S3CompatiblePrivateObjectClient {
  return {
    issueStagingPut: async () => ({ url: "https://storage.test/staging" }),
    head: async () => null,
    get: async () => Readable.from([]),
    putStaging: async () => undefined,
    putIfAbsent: async () => undefined,
    copyIfAbsent: async () => undefined,
    delete: async () => undefined,
    list: async () => ({ objects: [], nextCursor: null }),
    issuePrivateRead: async () => ({ url: "https://storage.test/private" }),
  };
}

const s3Environment = {
  ASSET_STORAGE_DRIVER: "s3",
  ASSET_S3_ENDPOINT: "https://objects.example.test",
  ASSET_S3_REGION: "us-east-1",
  ASSET_S3_BUCKET: "private-assets",
  ASSET_S3_ACCESS_KEY_ID: "test-access-key",
  ASSET_S3_SECRET_ACCESS_KEY: "test-secret",
  ASSET_S3_FORCE_PATH_STYLE: "true",
  ASSET_STORAGE_VERSIONING: "disabled",
};

describe("runtime AssetStorage composition", () => {
  it("creates filesystem storage only when explicitly configured", () => {
    expect(
      createRuntimeAssetStorage({
        ASSET_STORAGE_DRIVER: "filesystem",
        ASSET_STORAGE_ROOT: ".test-assets",
      }),
    ).toBeInstanceOf(FilesystemAssetStorage);
    expect(() => createRuntimeAssetStorage({})).toThrow("ASSET_STORAGE_DRIVER");
  });

  it("creates S3-compatible storage through the deterministic provider seam", () => {
    const createS3ObjectClient = vi.fn(() => fakeS3Client());
    const storage = createRuntimeAssetStorage(s3Environment, { createS3ObjectClient });
    expect(storage).toBeInstanceOf(S3CompatibleAssetStorage);
    expect(createS3ObjectClient).toHaveBeenCalledWith({
      endpoint: "https://objects.example.test",
      region: "us-east-1",
      bucket: "private-assets",
      accessKeyId: "test-access-key",
      secretAccessKey: "test-secret",
      forcePathStyle: true,
    });
  });

  it("preserves the file length for checksummed permanent uploads", async () => {
    const directory = await mkdtemp(join(tmpdir(), "asset-storage-test-"));
    const uploadPath = join(directory, "payload");
    const data = Buffer.from("test asset");
    const requests: Array<{ encoding?: string; decodedLength?: string }> = [];
    const server = createServer((request, response) => {
      requests.push({
        encoding: request.headers["content-encoding"],
        decodedLength: request.headers["x-amz-decoded-content-length"] as string | undefined,
      });
      request.resume();
      request.once("end", () => response.writeHead(200).end());
    });

    try {
      await writeFile(uploadPath, data);
      await new Promise<void>((resolve, reject) => {
        server.once("error", reject);
        server.listen(0, "127.0.0.1", resolve);
      });
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("Mock S3 did not bind.");
      const storage = createRuntimeAssetStorage({
        ...s3Environment,
        ASSET_S3_ENDPOINT: `http://127.0.0.1:${address.port}`,
      });

      await storage.putPermanentFromStream(
        createPermanentStorageKey(),
        createReadStream(uploadPath),
      );

      expect(requests).toEqual([
        {
          encoding: "aws-chunked",
          decodedLength: String(data.length),
        },
      ]);
    } finally {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
      await rm(directory, { recursive: true });
    }
  });

  it("fails fast for malformed production S3 configuration instead of falling back", () => {
    expect(() => createRuntimeAssetStorage({ ASSET_STORAGE_DRIVER: "s3" })).toThrow(
      "ASSET_S3_ENDPOINT",
    );
    expect(() =>
      createRuntimeAssetStorage({ ...s3Environment, ASSET_S3_FORCE_PATH_STYLE: "sometimes" }),
    ).toThrow("ASSET_S3_FORCE_PATH_STYLE");
    expect(() =>
      createRuntimeAssetStorage({ ...s3Environment, ASSET_STORAGE_VERSIONING: "enabled" }),
    ).toThrow("ASSET_STORAGE_VERSIONING");
    expect(() =>
      createRuntimeAssetStorage({
        ...s3Environment,
        NEXT_PUBLIC_ASSET_S3_SECRET_ACCESS_KEY: "leaked",
      }),
    ).toThrow("ASSET_S3_SECRET_ACCESS_KEY");
  });
});
