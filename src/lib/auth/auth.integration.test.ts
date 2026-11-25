import { createHmac, timingSafeEqual } from "node:crypto";

import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { applySetCookies, splitSetCookieHeader } from "better-auth/cookies";
import { drizzle } from "drizzle-orm/node-postgres";
import { migrate } from "drizzle-orm/node-postgres/migrator";
import { Pool } from "pg";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";

import * as schema from "@/db/schema";
import { getTestDatabaseUrl } from "@/db/test-environment";

import { emailAndPasswordOptions, validateUserName } from "./options";
import { getAuthOriginConfiguration } from "./origin";

const baseUrl = "http://localhost:3000";
const authSecret = "test-only-better-auth-secret-that-is-long-enough";
const pool = new Pool({ connectionString: getTestDatabaseUrl(process.env) });
const database = drizzle({ client: pool, schema });
const auth = betterAuth({
  ...getAuthOriginConfiguration(baseUrl),
  secret: authSecret,
  database: drizzleAdapter(database, {
    provider: "pg",
    schema,
  }),
  emailAndPassword: emailAndPasswordOptions,
  user: {
    validateUserInfo: ({ user, source }) => validateUserName(user, source),
  },
});

type AuthResponse = {
  code?: string;
  user?: {
    email: string;
    name: string;
  };
};

async function callAuth(
  path: string,
  options: Readonly<{
    body?: Record<string, unknown>;
    cookies?: Headers;
    method?: "GET" | "POST";
    origin?: string;
  }> = {},
): Promise<Response> {
  const headers = new Headers({ origin: options.origin ?? baseUrl });

  if (options.cookies?.get("cookie")) {
    headers.set("cookie", options.cookies.get("cookie")!);
  }

  if (options.body) {
    headers.set("content-type", "application/json");
  }

  return auth.handler(
    new Request(`${baseUrl}/api/auth${path}`, {
      method: options.method ?? (options.body ? "POST" : "GET"),
      headers,
      body: options.body ? JSON.stringify(options.body) : undefined,
    }),
  );
}

function retainCookies(target: Headers, response: Response): void {
  const setCookie = response.headers.get("set-cookie");

  if (setCookie) {
    applySetCookies(target, splitSetCookieHeader(setCookie));
  }
}

async function signUp(
  email = "creator@example.com",
  password = "secure-password",
): Promise<Headers> {
  const response = await callAuth("/sign-up/email", {
    body: {
      name: "Creator",
      email,
      password,
    },
  });
  const cookies = new Headers();

  retainCookies(cookies, response);
  expect(response.status).toBe(200);
  return cookies;
}

beforeAll(async () => {
  await migrate(database, { migrationsFolder: "drizzle" });
});

beforeEach(async () => {
  await database.execute('TRUNCATE TABLE "session", "account", "verification", "user" CASCADE');
});

afterAll(async () => {
  await pool.end();
});

describe("email/password authentication", () => {
  it("accepts the canonical local browser origin and rejects a different origin", async () => {
    const canonicalOrigin = await callAuth("/sign-up/email", {
      body: {
        name: "Creator",
        email: "canonical-origin@example.com",
        password: "secure-password",
      },
    });
    const differentOrigin = await callAuth("/sign-up/email", {
      body: {
        name: "Creator",
        email: "different-origin@example.com",
        password: "secure-password",
      },
      origin: "http://127.0.0.1:3000",
    });
    const payload = (await differentOrigin.json()) as AuthResponse;

    expect(canonicalOrigin.status).toBe(200);
    expect(differentOrigin.status).toBe(403);
    expect(payload.code).toBe("INVALID_ORIGIN");
  });

  it("creates an account and an authenticated session on sign-up", async () => {
    const response = await callAuth("/sign-up/email", {
      body: {
        name: "Creator",
        email: "creator@example.com",
        password: "secure-password",
      },
    });
    const payload = (await response.json()) as AuthResponse;
    const cookies = new Headers();

    retainCookies(cookies, response);

    expect(response.status).toBe(200);
    expect(payload.user).toMatchObject({
      name: "Creator",
      email: "creator@example.com",
    });
    expect(cookies.get("cookie")).toContain("better-auth.session_token=");
  });

  it("signs the database session token in an HttpOnly same-site cookie", async () => {
    const response = await callAuth("/sign-up/email", {
      body: {
        name: "Creator",
        email: "cookie@example.com",
        password: "secure-password",
      },
    });
    const cookieLine = splitSetCookieHeader(response.headers.get("set-cookie") ?? "").find(
      (cookie) => cookie.startsWith("better-auth.session_token="),
    );
    const encodedCookieValue = cookieLine
      ?.split(";", 1)[0]
      ?.slice("better-auth.session_token=".length);
    const cookieValue = encodedCookieValue ? decodeURIComponent(encodedCookieValue) : "";
    const signaturePosition = cookieValue.lastIndexOf(".");
    const sessionToken = cookieValue.slice(0, signaturePosition);
    const signature = Buffer.from(cookieValue.slice(signaturePosition + 1), "base64");
    const expectedSignature = createHmac("sha256", authSecret).update(sessionToken).digest();
    const sessions = await pool.query<{ token: string }>('SELECT token FROM "session"');

    expect(response.status).toBe(200);
    expect(cookieLine).toContain("; Path=/");
    expect(cookieLine).toContain("; HttpOnly");
    expect(cookieLine).toContain("; SameSite=Lax");
    expect(cookieLine).toContain("; Max-Age=604800");
    expect(sessionToken).toBe(sessions.rows[0]?.token);
    expect(signature).toHaveLength(expectedSignature.length);
    if (signature.length === expectedSignature.length) {
      expect(timingSafeEqual(signature, expectedSignature)).toBe(true);
    }
  });

  it("rejects invalid sign-up inputs at the server boundary", async () => {
    const blankName = await callAuth("/sign-up/email", {
      body: {
        name: "   ",
        email: "creator@example.com",
        password: "secure-password",
      },
    });
    const blankNamePayload = (await blankName.json()) as AuthResponse;
    const shortPassword = await callAuth("/sign-up/email", {
      body: {
        name: "Creator",
        email: "creator@example.com",
        password: "short",
      },
    });
    const shortPasswordPayload = (await shortPassword.json()) as AuthResponse;

    expect(blankName.status).toBe(403);
    expect(blankNamePayload.code).toBe("INVALID_NAME");
    expect(shortPassword.status).toBe(400);
    expect(shortPasswordPayload.code).toBe("PASSWORD_TOO_SHORT");
  });

  it("creates a new session on successful sign-in", async () => {
    await signUp();
    const response = await callAuth("/sign-in/email", {
      body: {
        email: "creator@example.com",
        password: "secure-password",
      },
    });
    const payload = (await response.json()) as AuthResponse;
    const cookies = new Headers();

    retainCookies(cookies, response);

    expect(response.status).toBe(200);
    expect(payload.user?.email).toBe("creator@example.com");
    expect(cookies.get("cookie")).toContain("better-auth.session_token=");
  });

  it("normalizes Unicode passwords with NFKC before verification", async () => {
    await signUp("unicode@example.com", "Better Auth fixture - e\u0301 - 123");
    const response = await callAuth("/sign-in/email", {
      body: {
        email: "unicode@example.com",
        password: "Better Auth fixture - \u00e9 - 123",
      },
    });

    expect(response.status).toBe(200);
  });

  it("returns the same safe failure for invalid sign-in credentials", async () => {
    const response = await callAuth("/sign-in/email", {
      body: {
        email: "missing@example.com",
        password: "secure-password",
      },
    });
    const payload = (await response.json()) as AuthResponse;

    expect(response.status).toBe(401);
    expect(payload.code).toBe("INVALID_EMAIL_OR_PASSWORD");
  });

  it("persists a session across subsequent server-side session checks", async () => {
    const cookies = await signUp();
    const firstSession = await callAuth("/get-session", { cookies });
    const secondSession = await callAuth("/get-session", { cookies });
    const firstPayload = (await firstSession.json()) as AuthResponse;
    const secondPayload = (await secondSession.json()) as AuthResponse;

    expect(firstSession.status).toBe(200);
    expect(firstPayload.user?.email).toBe("creator@example.com");
    expect(secondPayload.user?.email).toBe("creator@example.com");
  });

  it("renews the session and cookie before the configured rolling expiry", async () => {
    const cookies = await signUp("renewal@example.com");
    const session = await pool.query<{ token: string; expires_at: Date }>(
      'SELECT token, expires_at FROM "session"',
    );
    const priorExpiry = new Date(session.rows[0]!.expires_at);

    await pool.query(
      "UPDATE \"session\" SET expires_at = NOW() + INTERVAL '5 days' WHERE token = $1",
      [session.rows[0]!.token],
    );

    const response = await callAuth("/get-session", { cookies });
    const renewedSession = await pool.query<{ expires_at: Date }>(
      'SELECT expires_at FROM "session" WHERE token = $1',
      [session.rows[0]!.token],
    );

    expect(response.status).toBe(200);
    expect(response.headers.get("set-cookie")).toContain("better-auth.session_token=");
    expect(renewedSession.rows[0]!.expires_at.getTime()).toBeGreaterThan(priorExpiry.getTime());
  });

  it("terminates the persisted session on sign-out", async () => {
    const cookies = await signUp();
    const signOutResponse = await callAuth("/sign-out", {
      body: {},
      cookies,
    });
    const sessionResponse = await callAuth("/get-session", { cookies });
    const sessions = await pool.query("SELECT count(*)::int AS count FROM session");

    expect(signOutResponse.status).toBe(200);
    expect(await sessionResponse.json()).toBeNull();
    expect(sessions.rows[0]?.count).toBe(0);
  });
});
