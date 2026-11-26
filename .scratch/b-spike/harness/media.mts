import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import sharp from "sharp";
import { DefaultMediaInspector } from "@/modules/assets/infrastructure/media-inspector";
import {
  createSafeFfprobeProcess,
  FfprobeMediaInspector,
} from "@/modules/assets/infrastructure/ffprobe-media-inspector";
import { MediaInspectionError } from "@/modules/assets/domain/media-inspection-contracts";
import type { MediaInspectionInput } from "@/modules/assets/application/media-inspector";

const runtime = "bun" in process.versions ? "bun" : "node";
const executable = process.env.FFPROBE_PATH!;
const expectedVersion = process.env.FFPROBE_EXPECTED_VERSION!;
const ffprobe = new FfprobeMediaInspector({ executablePath: executable, expectedVersion });
const inspect = new DefaultMediaInspector(undefined, ffprobe);
const run = createSafeFfprobeProcess();
const failures: string[] = [];
const results = [];
const idleRss = process.memoryUsage.rss();
const versions = sharp.versions;
const version = await run(executable, ["-version"], {
  timeoutMs: 5_000,
  maxOutputBytes: 256 * 1024,
});
await ffprobe.verifyRuntime();
const started = performance.now();
await assert.rejects(
  run("/usr/bin/sleep", ["5"], { timeoutMs: 100, maxOutputBytes: 256 * 1024 }),
  /process limit exceeded/,
);
const timeoutMs = performance.now() - started;
assert.ok(timeoutMs < 2_000, `Child timeout took ${timeoutMs}ms`);

type Fixture = {
  name: string;
  filename: string;
  sha256: string;
  sizeBytes: number;
  declaredMediaType: MediaInspectionInput["declaredMediaType"];
  result?: object;
  failureCode?: string;
};
for (const corpus of ["images", "av"]) {
  const directory = resolve("backend/internal/assets/testdata", corpus);
  const { fixtures }: { fixtures: Fixture[] } = JSON.parse(
    await readFile(resolve(directory, "baseline.json"), "utf8"),
  );
  assert.equal(fixtures.length, corpus === "images" ? 23 : 8);
  for (const fixture of fixtures) {
    const filePath = resolve(directory, fixture.name + ".bin");
    const bytes = await readFile(filePath);
    assert.equal(bytes.length, fixture.sizeBytes);
    assert.equal(createHash("sha256").update(bytes).digest("hex"), fixture.sha256);
    const expected = fixture.result ?? { failureCode: fixture.failureCode };
    const actual = await inspect
      .inspect({
        filePath,
        declaredMediaType: fixture.declaredMediaType,
        originalFilename: fixture.filename,
      })
      .catch((error: unknown) => {
        if (error instanceof MediaInspectionError) return { failureCode: error.code };
        throw error;
      });
    let matched = true;
    try {
      assert.deepEqual(actual, expected);
    } catch {
      matched = false;
      failures.push(fixture.name);
    }
    results.push({ corpus, fixture: fixture.name, matched, expected, actual });
  }
}
await writeFile(
  `/evidence/${runtime}-media.json`,
  JSON.stringify(
    {
      runtime,
      versions: process.versions,
      sharp: versions,
      ffprobe: version.stdout.split("\n")[0],
      timeoutMs,
      idleRss,
      peakRss: process.resourceUsage().maxRSS * 1024,
      results,
    },
    null,
    2,
  ) + "\n",
);
assert.deepEqual(failures, []);
