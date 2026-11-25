import { createHash } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

import sharp from "sharp";

import { DefaultMediaInspector } from "../src/modules/assets/infrastructure/media-inspector";
import { MediaInspectionError } from "../src/modules/assets/domain/media-inspection-contracts";

const requiredSharpVersion = "0.35.4";
const outputDirectory = fileURLToPath(
  new URL("../backend/internal/assets/testdata/images/", import.meta.url),
);
const inspector = new DefaultMediaInspector();

type Fixture = Readonly<{
  name: string;
  bytes: Buffer;
  declaredMediaType: "IMAGE" | "VIDEO";
  filename: string;
}>;

async function generatedImage(
  width: number,
  height: number,
  channels: 3 | 4 = 3,
  background = "#345678",
): Promise<Buffer> {
  return sharp({ create: { width, height, channels, background } }).png().toBuffer();
}

async function createFixtures(): Promise<Fixture[]> {
  const jpeg = await sharp({
    create: { width: 2, height: 3, channels: 3, background: "#345678" },
  })
    .jpeg()
    .toBuffer();
  const png = await generatedImage(2, 3, 4);
  const webp = await sharp({
    create: { width: 2, height: 3, channels: 4, background: "#345678" },
  })
    .webp()
    .toBuffer();
  const fixtures: Fixture[] = [
    { name: "static-jpeg", bytes: jpeg, declaredMediaType: "IMAGE", filename: "photo.jpg" },
    { name: "static-png", bytes: png, declaredMediaType: "IMAGE", filename: "photo.png" },
    { name: "static-webp", bytes: webp, declaredMediaType: "IMAGE", filename: "photo.webp" },
    {
      name: "truncated-jpeg",
      bytes: jpeg.subarray(0, Math.max(16, Math.floor(jpeg.byteLength / 2))),
      declaredMediaType: "IMAGE",
      filename: "truncated.jpg",
    },
    {
      name: "empty-file",
      bytes: Buffer.alloc(0),
      declaredMediaType: "IMAGE",
      filename: "empty.png",
    },
    {
      name: "corrupt-jpeg",
      bytes: Buffer.from("synthetic non-image bytes"),
      declaredMediaType: "IMAGE",
      filename: "corrupt.jpg",
    },
    {
      name: "extension-mismatch",
      bytes: jpeg,
      declaredMediaType: "IMAGE",
      filename: "photo.png",
    },
    {
      name: "declared-type-mismatch",
      bytes: jpeg,
      declaredMediaType: "VIDEO",
      filename: "photo.mp4",
    },
    {
      name: "dimension-limit-exact",
      bytes: await generatedImage(12_000, 1),
      declaredMediaType: "IMAGE",
      filename: "width-limit.png",
    },
    {
      name: "dimension-limit-over",
      bytes: await generatedImage(12_001, 1),
      declaredMediaType: "IMAGE",
      filename: "wide.png",
    },
    {
      name: "pixel-limit-exact",
      bytes: await generatedImage(12_000, 5_000),
      declaredMediaType: "IMAGE",
      filename: "pixel-limit.png",
    },
    {
      name: "pixel-limit-over",
      bytes: await generatedImage(12_000, 5_001),
      declaredMediaType: "IMAGE",
      filename: "pixel-limit-over.png",
    },
    {
      name: "byte-limit-over",
      bytes: Buffer.alloc(10 * 1024 * 1024 + 1, 0x5a),
      declaredMediaType: "IMAGE",
      filename: "large.png",
    },
  ];

  for (let orientation = 1; orientation <= 8; orientation += 1) {
    const oriented = await sharp({
      create: { width: 2, height: 3, channels: 3, background: "#345678" },
    })
      .withMetadata({ orientation })
      .jpeg()
      .toBuffer();
    fixtures.push({
      name: `exif-orientation-${orientation}`,
      bytes: oriented,
      declaredMediaType: "IMAGE",
      filename: `orientation-${orientation}.jpg`,
    });
  }

  const animated = await sharp(
    [await generatedImage(2, 2, 4, "#ff0000"), await generatedImage(2, 2, 4, "#0000ff")],
    { join: { animated: true } },
  )
    .webp({ delay: [100, 100], loop: 0 })
    .toBuffer();
  const animatedMetadata = await sharp(animated, { animated: true }).metadata();
  if (animatedMetadata.pages !== 2)
    throw new Error(
      `The generated WebP fixture has unexpected metadata: ${JSON.stringify({ pages: animatedMetadata.pages, pageHeight: animatedMetadata.pageHeight, width: animatedMetadata.width, height: animatedMetadata.height })}`,
    );
  fixtures.push({
    name: "animated-webp",
    bytes: animated,
    declaredMediaType: "IMAGE",
    filename: "animated.webp",
  });

  const tiff = await sharp({
    create: { width: 2, height: 3, channels: 3, background: "#345678" },
  })
    .tiff()
    .toBuffer();
  fixtures.push({
    name: "unsupported-tiff",
    bytes: tiff,
    declaredMediaType: "IMAGE",
    filename: "unsupported.tiff",
  });

  return fixtures;
}

async function capture() {
  if (sharp.versions.sharp !== requiredSharpVersion)
    throw new Error(`Expected sharp@${requiredSharpVersion}; found ${sharp.versions.sharp}.`);

  await mkdir(outputDirectory, { recursive: true });
  const results = [];
  for (const fixture of await createFixtures()) {
    const filePath = `${outputDirectory}/${fixture.name}.bin`;
    await writeFile(filePath, fixture.bytes);
    try {
      const result = await inspector.inspect({
        filePath,
        declaredMediaType: fixture.declaredMediaType,
        originalFilename: fixture.filename,
      });
      results.push({
        name: fixture.name,
        sha256: createHash("sha256").update(fixture.bytes).digest("hex"),
        sizeBytes: fixture.bytes.byteLength,
        declaredMediaType: fixture.declaredMediaType,
        filename: fixture.filename,
        result,
      });
    } catch (error) {
      if (!(error instanceof MediaInspectionError)) throw error;
      results.push({
        name: fixture.name,
        sha256: createHash("sha256").update(fixture.bytes).digest("hex"),
        sizeBytes: fixture.bytes.byteLength,
        declaredMediaType: fixture.declaredMediaType,
        filename: fixture.filename,
        failureCode: error.code,
      });
    }
  }

  const baseline = {
    schemaVersion: 1,
    provenance: {
      generatedBy: "scripts/capture-asset-image-corpus.ts",
      sharpVersion: sharp.versions.sharp,
      libvipsVersion: sharp.versions.vips,
      nodeVersion: process.version,
      source: "Synthetic pixels and deliberately malformed bytes; no creator data.",
    },
    fixtures: results,
  };
  await writeFile(
    `${outputDirectory}/baseline.json`,
    `${JSON.stringify(baseline, null, 2)}\n`,
    "utf8",
  );
  process.stdout.write(`Captured ${results.length} legacy image inspection baselines.\n`);
}

await capture();
