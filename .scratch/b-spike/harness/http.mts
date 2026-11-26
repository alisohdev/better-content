import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { setTimeout as delay } from "node:timers/promises";
import { contractPath, validateSave, validateWorkspaceId, verifyResponse } from "./contract.mts";
import { fixtureCookie, fixturePassword } from "./fixtures.mts";

const runtime = process.argv[2];
const workspaceId = process.env.SPIKE_WORKSPACE_ID!;
const origin = "http://127.0.0.1:3100";
const route = origin + "/api/v1" + contractPath.replace("{workspaceId}", workspaceId);
const report: Record<string, unknown> = {};
const start = Number(process.env.SPIKE_START_MS);
try {
  const deadline = Date.now() + 15_000;
  while (true) {
    try {
      const health = await fetch(origin + "/health");
      if (health.ok) {
        report.startupMs = Date.now() - start;
        report.health = await health.json();
        assert.equal((report.health as { runtime: string }).runtime, runtime);
        break;
      }
    } catch (error) {
      if (error instanceof assert.AssertionError) throw error;
    }
    if (Date.now() >= deadline) throw new Error("HTTP startup deadline exceeded");
    await delay(25);
  }

  const signin = (password: string, requestOrigin = origin) =>
    fetch(origin + "/api/auth/sign-in/email", {
      method: "POST",
      headers: { origin: requestOrigin, "content-type": "application/json" },
      body: JSON.stringify({ email: "owner@example.test", password }),
    });

  const fixtureHeader = "better-auth.session_token=" + fixtureCookie.cookieValue;
  const legacy = await fetch(origin + "/api/auth/get-session", {
    headers: { cookie: fixtureHeader },
  });
  assert.equal(legacy.status, 200);
  assert.equal((await legacy.json())?.user.email, "owner@example.test");
  assert.equal((await signin(fixturePassword.password, "https://foreign.example")).status, 403);
  assert.equal((await signin("a wrong spike password")).status, 401);
  let cookie: string | undefined;
  for (const password of [fixturePassword.password, fixturePassword.equivalentNfkcPassword]) {
    const response = await signin(password);
    assert.equal(response.status, 200, "Stored fixture password must sign in");
    const cookies = response.headers.getSetCookie();
    const sessionCookie = cookies.find((cookie) => cookie.startsWith("better-auth.session_token="));
    assert.ok(sessionCookie);
    assert.match(sessionCookie, /;\s*HttpOnly\b/i);
    assert.match(sessionCookie, /;\s*SameSite=Lax\b/i);
    report.cookieForwarding = true;
    const session = await fetch(origin + "/api/auth/get-session", {
      headers: { cookie: sessionCookie.split(";")[0]! },
    });
    assert.equal((await session.json())?.user.email, "owner@example.test");
    cookie ??= sessionCookie.split(";")[0];
  }
  assert.ok(cookie);
  assert.ok(validateWorkspaceId(workspaceId));
  const save = {
    baseVersionId: null,
    payload: { schemaVersion: 1, identity: { creatorOrBrandDescription: "Spike creator" } },
  };
  assert.ok(validateSave(save));

  async function check(
    method: "get" | "put",
    expected: number,
    headers: HeadersInit,
    input?: unknown,
    url = route,
  ) {
    const response = await fetch(url, {
      method: method.toUpperCase(),
      headers,
      body: input === undefined ? undefined : JSON.stringify(input),
    });
    const body = await response.json();
    assert.equal(response.status, expected, JSON.stringify(body));
    verifyResponse(method, expected, body);
    return body;
  }

  assert.equal((await check("get", 401, {})).code, "UNAUTHORIZED");
  assert.equal(
    (await check("get", 403, { cookie }, undefined, route.replace(workspaceId, randomUUID()))).code,
    "FORBIDDEN",
  );
  assert.equal(
    (await check("get", 400, { cookie }, undefined, route.replace(workspaceId, "invalid"))).code,
    "VALIDATION_ERROR",
  );
  const initial = await check("get", 200, { cookie });
  assert.deepEqual(initial, { status: "NOT_CREATED", currentVersion: null });
  const headers = { cookie, origin, "content-type": "application/json" };
  assert.equal((await check("put", 403, { cookie }, save)).code, "INVALID_ORIGIN");
  assert.equal(
    (await check("put", 403, { ...headers, origin: "https://foreign.example" }, save)).code,
    "INVALID_ORIGIN",
  );
  assert.equal((await check("put", 400, headers, { payload: {} })).code, "VALIDATION_ERROR");
  assert.equal(
    (await check("put", 403, headers, save, route.replace(workspaceId, randomUUID()))).code,
    "FORBIDDEN",
  );
  const saved = await check("put", 200, headers, save);
  assert.equal(saved.versionNumber, 1);
  assert.equal((await check("put", 409, headers, save)).code, "CONFLICT");
  assert.deepEqual((await check("get", 200, { cookie })).currentVersion, saved);
  report.contract = "passed";

  const concurrency = 8;
  const samplesPerClient = 100;
  const errors: string[] = [];
  const latenciesMs: number[] = [];
  const runRequest = async () => {
    const requestedAt = performance.now();
    try {
      const response = await fetch(route, {
        headers: { cookie },
        signal: AbortSignal.timeout(5_000),
      });
      await response.arrayBuffer();
      if (!response.ok) errors.push(String(response.status));
    } catch (error) {
      errors.push(error instanceof Error ? error.name : "unknown");
    }
    latenciesMs.push(Number((performance.now() - requestedAt).toFixed(3)));
  };
  for (let index = 0; index < 40; index++) await runRequest();
  errors.length = 0;
  latenciesMs.length = 0;
  const loadAt = performance.now();
  await Promise.all(
    Array.from({ length: concurrency }, async () => {
      for (let index = 0; index < samplesPerClient; index++) await runRequest();
    }),
  );
  const sorted = [...latenciesMs].sort((a, b) => a - b);
  report.load = {
    concurrency,
    samples: latenciesMs.length,
    wallMs: performance.now() - loadAt,
    errors,
    latenciesMs,
    p50Ms: sorted[Math.ceil(sorted.length * 0.5) - 1],
    p95Ms: sorted[Math.ceil(sorted.length * 0.95) - 1],
  };
} catch (error) {
  report.error = error instanceof Error ? `${error.name}: ${error.message}` : String(error);
  throw error;
} finally {
  await writeFile(`/evidence/${runtime}-http.json`, JSON.stringify(report, null, 2) + "\n");
}
