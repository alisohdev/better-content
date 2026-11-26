import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import integrationConfig from "../../../vitest.integration.config.mts";

const setupFiles = [fileURLToPath(new URL("./assert-runtime.mts", import.meta.url))];
// Bun's native Zod module exposes the named export. Vitest's default interop unwraps it incorrectly.
const deps = process.env.SPIKE_CANDIDATE === "bun" ? { interopDefault: false } : undefined;
const unitConfig = defineConfig({
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("../../../src", import.meta.url)),
      "server-only": fileURLToPath(new URL("../../../src/test/server-only.ts", import.meta.url)),
    },
  },
  test: {
    deps,
    setupFiles,
    environment: "node",
    include: ["src/modules/**/*.test.ts"],
    exclude: ["src/**/*.integration.test.ts"],
  },
});

export default process.env.SPIKE_VITEST_INTEGRATION === "1"
  ? defineConfig({
      ...integrationConfig,
      test: { ...integrationConfig.test, deps, setupFiles },
    })
  : unitConfig;
