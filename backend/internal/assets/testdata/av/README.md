# Legacy audio and video inspection corpus

These generated files contain no creator data. `baseline.json` records each
file's SHA-256, declared media type, filename, ffprobe output used by the Go
normalizer, and the normalized result or failure from the legacy TypeScript
inspector.

The current corpus has eight cases for H.264/AAC MP4, MP3, AAC/M4A, PCM/WAV,
invalid bytes, declared-type mismatches, and an extension mismatch. It uses
ffmpeg and ffprobe 8.1.2 from the local machine. The manifest marks this
baseline as provisional because the project has not approved that ffprobe build.

Regenerate the fixtures with an installed ffmpeg and the approved ffprobe path
and version. Mark the baseline provisional until the deployment owner approves
the exact runtime.

```powershell
$env:FFMPEG_PATH = (Get-Command ffmpeg).Source
$env:FFPROBE_PATH = (Get-Command ffprobe).Source
$env:FFPROBE_EXPECTED_VERSION = (ffprobe -version | Select-Object -First 1) -replace '^ffprobe version ([^ ]+).*', '$1'
$env:FFPROBE_BASELINE_PROVISIONAL = '1'
node --env-file-if-exists=.env.local --import tsx scripts/capture-asset-av-corpus.ts
```

The capture script verifies the configured ffprobe version before writing any
fixtures. The ffmpeg build must include H.264, AAC, MP3, and PCM encoders. The
Go test checks each fixture hash and compares Go normalization with the captured
legacy result. Re-run the script with the approved runtime before treating this
corpus as cutover evidence.

The Phase 7 comparison recorded separate results from the legacy adapter
using the checksum-verified Debian amd64 package `ffmpeg=7:7.1.5-0+deb13u1`.
`TestPinnedFFprobeMatchesLegacyRuntime` uses those results with these same
fixture bytes and runs the real Go adapter. To repeat the check on that
runtime, set `ASSET_FFPROBE_PARITY_PATH` to its ffprobe executable and run
`go -C backend test ./internal/assets -run '^TestPinnedFFprobeMatchesLegacyRuntime$' -count=1`.
Both adapters report 1,045 ms for the MP3 on this build. The older 8.1.2
manifest still records the corresponding 1,000 ms result.
