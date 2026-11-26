import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { eq } from "drizzle-orm";
import { migrate } from "drizzle-orm/node-postgres/migrator";
import { Hono } from "hono";
import type { ContentfulStatusCode } from "hono/utils/http-status";

import { db } from "@/db";
import * as schema from "@/db/schema";
import { emailAndPasswordOptions, validateUserName } from "@/lib/auth/options";
import { getAuthOriginConfiguration } from "@/lib/auth/origin";
import { ApplicationError } from "@/lib/errors/app-error";
import { createContentDnaApplicationService } from "@/modules/dna/application/content-dna-service";
import { getOrCreateDefaultWorkspace } from "@/modules/workspace/application/default-workspace";

import { contractPath, validateSave } from "./contract.mts";
import { fixtureCookie, fixturePassword } from "./fixtures.mts";

const origin = "http://127.0.0.1:3100";
const runtime = "bun" in process.versions ? "bun" : "node";
if (
  process.env.DATABASE_URL !==
  `postgresql://postgres:spike-disposable-only@db:5432/spike_${runtime}_http_test`
) {
  throw new Error("The HTTP harness requires its private Compose database.");
}

const auth = betterAuth({
  ...getAuthOriginConfiguration(origin),
  secret: fixtureCookie.secret,
  database: drizzleAdapter(db, { provider: "pg", schema }),
  emailAndPassword: emailAndPasswordOptions,
  user: { validateUserInfo: ({ user, source }) => validateUserName(user, source) },
});

export const fixtureEmail = "owner@example.test";
const userId = "spike-fixture-owner";

export async function prepare() {
  await migrate(db, { migrationsFolder: "drizzle" });
  await db.transaction(async (tx) => {
    await tx.delete(schema.user).where(eq(schema.user.id, userId));
    await tx.insert(schema.user).values({ id: userId, email: fixtureEmail, name: "Spike Owner" });
    await tx.insert(schema.account).values({
      id: userId,
      accountId: userId,
      userId,
      providerId: "credential",
      password: fixturePassword.hash,
      updatedAt: new Date(),
    });
    await tx.insert(schema.session).values({
      id: "spike-fixture-session",
      token: fixtureCookie.databaseSessionToken,
      userId,
      expiresAt: new Date(Date.now() + 3_600_000),
      updatedAt: new Date(),
    });
  });
  return (await getOrCreateDefaultWorkspace(userId)).id;
}

export const app = new Hono();
const route = "/api/v1" + contractPath.replace("{workspaceId}", ":workspaceId");

function dna(request: Request) {
  return createContentDnaApplicationService({
    getAuthenticatedUserId: async () =>
      (await auth.api.getSession({ headers: request.headers }))?.user.id ?? null,
    logger: { info() {}, warn() {} },
  });
}

app.onError((error, context) => {
  if (error instanceof ApplicationError) {
    return context.json({ code: error.code }, error.status as ContentfulStatusCode);
  }
  return context.json({ code: "INTERNAL_ERROR" }, 500);
});
app.get("/health", (context) =>
  context.json({ runtime, versions: process.versions, rss: process.memoryUsage.rss() }),
);
app.on(["GET", "POST"], "/api/auth/*", (context) => auth.handler(context.req.raw));
app.get(route, async (context) =>
  context.json(
    await dna(context.req.raw).getCurrentContentDna({
      workspaceId: context.req.param("workspaceId") ?? "",
    }),
  ),
);
app.put(route, async (context) => {
  if (context.req.header("origin") !== origin) return context.json({ code: "INVALID_ORIGIN" }, 403);
  let body: unknown;
  try {
    body = await context.req.json();
  } catch {
    return context.json({ code: "VALIDATION_ERROR" }, 400);
  }
  if (!validateSave(body)) return context.json({ code: "VALIDATION_ERROR" }, 400);
  return context.json(
    await dna(context.req.raw).saveContentDna({
      workspaceId: context.req.param("workspaceId") ?? "",
      baseVersionId: body.baseVersionId,
      payload: body.payload,
    }),
  );
});
