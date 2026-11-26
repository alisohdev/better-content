import { db } from "@/db";
import { verifyAssetWorkerDeployment } from "@/modules/assets/infrastructure/asset-worker-preflight";
import { createRuntimeAssetStorage } from "@/modules/assets/infrastructure/runtime-asset-storage";

const runtime = "bun" in process.versions ? "bun" : "node";
if (
  process.env.DATABASE_URL !==
    `postgresql://postgres:spike-disposable-only@db:5432/spike_${runtime}_http_test` ||
  process.env.ASSET_S3_ENDPOINT !== "http://s3:9000"
) {
  throw new Error("The spike preflight requires the private Compose dependencies.");
}

try {
  const result = await verifyAssetWorkerDeployment({
    database: db,
    storage: createRuntimeAssetStorage({
      ...process.env,
      ASSET_STORAGE_DRIVER: "s3",
      ASSET_STORAGE_VERSIONING: "disabled",
    }),
  });
  console.log(JSON.stringify({ runtime, result }));
} catch (error) {
  console.log(
    JSON.stringify({ runtime, error: error instanceof Error ? error.message : String(error) }),
  );
  process.exitCode = 1;
} finally {
  await db.$client.end();
}
