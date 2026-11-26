import { readFileSync } from "node:fs";
const directory = ".scratch/phase-07-go-backend-migration/fixtures";
export const fixtureCookie: { secret: string; databaseSessionToken: string; cookieValue: string } =
  JSON.parse(readFileSync(`${directory}/better-auth-session-cookie-v1.json`, "utf8"));
export const fixturePassword: { hash: string; password: string; equivalentNfkcPassword: string } =
  JSON.parse(readFileSync(`${directory}/better-auth-scrypt-v1.json`, "utf8"));
