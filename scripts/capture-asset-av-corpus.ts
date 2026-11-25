import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { fileURLToPath } from "node:url";
import { z } from "zod";

import { DefaultMediaInspector } from "../src/modules/assets/infrastructure/media-inspector";
import {
  FfprobeMediaInspector,
  type FfprobeProcess,
} from "../src/modules/assets/infrastructure/ffprobe-media-inspector";
import { MediaInspectionError } from "../src/modules/assets/domain/media-inspection-contracts";

const outputDirectory = fileURLToPath(
  new URL("../backend/internal/assets/testdata/av/", import.meta.url),
);
const ffmpegPath = process.env.FFMPEG_PATH ?? "ffmpeg";
const ffprobePath = process.env.FFPROBE_PATH;
const expectedVersion = process.env.FFPROBE_EXPECTED_VERSION;
const provisional = process.env.FFPROBE_BASELINE_PROVISIONAL;

type CapturedProcess = Readonly<{ exitCode: number; stdout: string }>;
type FixtureInput = Readonly<{
  name: string;
  sourcePath: string;
  declaredMediaType: "VIDEO" | "AUDIO";
  filename: string;
}>;

const ffprobeOutputSchema = z
  .object({
    streams: z.array(z.record(z.string(), z.unknown())),
    format: z.record(z.string(), z.unknown()),
  })
  .strict();

function sanitizeProbeOutput(output: string): string {
  const parsed = ffprobeOutputSchema.parse(JSON.parse(output));
  const format = { ...parsed.format };
  delete format.filename;
  return JSON.stringify({ streams: parsed.streams, format });
}

function runProcess(executable: string, args: readonly string[]): Promise<CapturedProcess> {
  return new Promise((resolve, reject) => {
    execFile(
      executable,
      [...args],
      {
        shell: false,
        timeout: 60_000,
        maxBuffer: 1024 * 1024,
        windowsHide: true,
        encoding: "utf8",
      },
      (error, stdout) => {
        const exitCode =
          error === null
            ? 0
            : typeof error.code === "number"
              ? error.code
              : /^\d+$/.test(String(error.code))
                ? Number(error.code)
                : undefined;
        if (exitCode === undefined) {
          reject(new Error("A media fixture tool could not run."));
          return;
        }
        resolve({ exitCode, stdout });
      },
    );
  });
}

async function makeFixtures(directory: string): Promise<FixtureInput[]> {
  const video = join(directory, "video.mp4");
  const mp3 = join(directory, "audio.mp3");
  const m4a = join(directory, "audio.m4a");
  const wav = join(directory, "audio.wav");
  const corrupt = join(directory, "corrupt.mp3");
  const common = ["-hide_banner", "-loglevel", "error", "-y"];

  await runProcess(ffmpegPath, [
    ...common,
    "-f",
    "lavfi",
    "-i",
    "color=c=black:s=320x240:r=25:d=1",
    "-f",
    "lavfi",
    "-i",
    "sine=frequency=440:sample_rate=44100:duration=1",
    "-shortest",
    "-threads",
    "1",
    "-c:v",
    "libx264",
    "-pix_fmt",
    "yuv420p",
    "-c:a",
    "aac",
    "-metadata",
    "creation_time=2000-01-01T00:00:00Z",
    "-movflags",
    "+faststart",
    video,
  ]).then((result) => {
    if (result.exitCode !== 0) throw new Error("Could not generate the synthetic MP4 fixture.");
  });
  for (const [path, codec, format] of [
    [mp3, "libmp3lame", "mp3"],
    [m4a, "aac", "ipod"],
    [wav, "pcm_s16le", "wav"],
  ] as const) {
    const result = await runProcess(ffmpegPath, [
      ...common,
      "-f",
      "lavfi",
      "-i",
      "sine=frequency=440:sample_rate=44100:duration=1",
      "-threads",
      "1",
      "-c:a",
      codec,
      "-f",
      format,
      path,
    ]);
    if (result.exitCode !== 0)
      throw new Error(`Could not generate the synthetic ${format} fixture.`);
  }
  await writeFile(corrupt, Buffer.from("synthetic invalid audio bytes"));

  return [
    { name: "video-h264-aac", sourcePath: video, declaredMediaType: "VIDEO", filename: "clip.mp4" },
    {
      name: "video-declared-audio",
      sourcePath: video,
      declaredMediaType: "AUDIO",
      filename: "clip.mp4",
    },
    { name: "audio-mp3", sourcePath: mp3, declaredMediaType: "AUDIO", filename: "track.mp3" },
    {
      name: "audio-mp3-wrong-extension",
      sourcePath: mp3,
      declaredMediaType: "AUDIO",
      filename: "track.wav",
    },
    { name: "audio-m4a", sourcePath: m4a, declaredMediaType: "AUDIO", filename: "track.m4a" },
    { name: "audio-wav", sourcePath: wav, declaredMediaType: "AUDIO", filename: "track.wav" },
    {
      name: "audio-declared-video",
      sourcePath: mp3,
      declaredMediaType: "VIDEO",
      filename: "track.mp3",
    },
    {
      name: "corrupt-audio",
      sourcePath: corrupt,
      declaredMediaType: "AUDIO",
      filename: "corrupt.mp3",
    },
  ];
}

async function capture(): Promise<void> {
  if (!ffprobePath || !expectedVersion || (provisional !== "0" && provisional !== "1"))
    throw new Error(
      "Set FFPROBE_PATH, FFPROBE_EXPECTED_VERSION, and FFPROBE_BASELINE_PROVISIONAL (0 or 1).",
    );
  const versionResult = await runProcess(ffprobePath, ["-version"]);
  const actualVersion = /^ffprobe version ([^\s]+)/m.exec(versionResult.stdout)?.[1];
  if (versionResult.exitCode !== 0 || actualVersion !== expectedVersion)
    throw new Error("The configured ffprobe executable does not match its expected version.");

  const ffmpegVersionResult = await runProcess(ffmpegPath, ["-version"]);
  const ffmpegVersion = /^ffmpeg version ([^\s]+)/m.exec(ffmpegVersionResult.stdout)?.[1];
  if (ffmpegVersionResult.exitCode !== 0 || !ffmpegVersion)
    throw new Error("The configured ffmpeg executable is unavailable.");

  const tempDirectory = await mkdtemp(join(tmpdir(), "better-content-av-corpus-"));
  try {
    const fixtureInputs = await makeFixtures(tempDirectory);
    await mkdir(outputDirectory, { recursive: true });
    const capturedProcesses: CapturedProcess[] = [];
    const avInspector = new FfprobeMediaInspector({
      executablePath: ffprobePath,
      expectedVersion,
      execute: (async (executablePath, args) => {
        const result = await runProcess(executablePath, args);
        const safeResult =
          args.length === 1 && args[0] === "-version"
            ? result
            : result.exitCode === 0 && result.stdout
              ? { ...result, stdout: sanitizeProbeOutput(result.stdout) }
              : result;
        capturedProcesses.push(safeResult);
        return { ...safeResult, stderr: "" };
      }) satisfies FfprobeProcess,
    });
    await avInspector.verifyRuntime();
    capturedProcesses.length = 0;
    const inspector = new DefaultMediaInspector(undefined, avInspector);
    const fixtures = [];

    for (const input of fixtureInputs) {
      const targetPath = join(outputDirectory, `${input.name}.bin`);
      const bytes = await readFile(input.sourcePath);
      await copyFile(input.sourcePath, targetPath);
      let result;
      let failureCode;
      capturedProcesses.length = 0;
      try {
        result = await inspector.inspect({
          filePath: input.sourcePath,
          declaredMediaType: input.declaredMediaType,
          originalFilename: input.filename,
        });
      } catch (error) {
        if (!(error instanceof MediaInspectionError)) throw error;
        failureCode = error.code;
      }
      const processResult = capturedProcesses[0];
      fixtures.push({
        name: input.name,
        sha256: createHash("sha256").update(bytes).digest("hex"),
        sizeBytes: bytes.byteLength,
        declaredMediaType: input.declaredMediaType,
        filename: input.filename,
        ...(processResult
          ? {
              probeExitCode: processResult.exitCode,
              probeOutput: processResult.stdout,
            }
          : {}),
        ...(result ? { result } : { failureCode }),
      });
    }

    await writeFile(
      join(outputDirectory, "baseline.json"),
      `${JSON.stringify(
        {
          schemaVersion: 1,
          provenance: {
            generatedBy: basename(fileURLToPath(import.meta.url)),
            ffmpegVersion,
            ffprobeVersion: actualVersion,
            provisional: provisional === "1",
            source:
              "Synthetic H.264/AAC, MP3, AAC/M4A, PCM/WAV, and malformed bytes; no creator data.",
          },
          fixtures,
        },
        null,
        2,
      )}\n`,
      "utf8",
    );
    process.stdout.write(`Captured ${fixtures.length} legacy audio/video inspection baselines.\n`);
  } finally {
    await rm(tempDirectory, { recursive: true, force: true });
  }
}

capture().catch((error: unknown) => {
  process.stderr.write(
    `${error instanceof Error ? error.message : "Media fixture capture failed."}\n`,
  );
  process.exitCode = 1;
});
