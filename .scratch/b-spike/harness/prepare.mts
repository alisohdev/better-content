import { db } from "@/db";
import { prepare } from "./app.mts";

try {
  console.log(await prepare());
} finally {
  await db.$client.end();
}
