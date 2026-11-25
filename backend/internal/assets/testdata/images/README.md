# Legacy image inspection corpus

These synthetic fixtures contain no creator data. `baseline.json` records the
SHA-256, declared media type, original filename, and exact normalized success
or failure from the existing `DefaultMediaInspector` and `SharpMediaInspector`.
The capture ran with the lockfile-pinned Sharp 0.35.4 / libvips 8.18.6 on Node
24.19.0.

Regenerate with:

```text
node --env-file-if-exists=.env.local --import tsx scripts/capture-asset-image-corpus.ts
```

The 23 cases cover supported JPEG/PNG/static WebP, all eight EXIF orientations,
animated WebP, an unsupported TIFF, empty/corrupt/truncated files, declared
type and extension mismatches, exact/over dimension and pixel limits, and the
over-byte-limit precedence. The over-size fixture is intentionally malformed:
the legacy inspector must reject its byte size before attempting to decode it.

This is the image subset of the parity corpus. Synthetic video and audio
fixtures live in `../av`. Their current ffprobe baseline is provisional and does
not clear the approved-runtime parity gate. See the AV corpus README before
claiming full media parity.
