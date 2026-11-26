# Better Content runtime spike harness

Run the two candidates on Docker Desktop or another Linux amd64 Docker engine with Compose v2. From the repository root:

```sh
docker compose -p bc-b-spike -f .scratch/b-spike/harness/compose.yml build runner
docker compose -p bc-b-spike -f .scratch/b-spike/harness/compose.yml up -d --wait db s3
docker compose -p bc-b-spike -f .scratch/b-spike/harness/compose.yml run --rm -T runner bash .scratch/b-spike/harness/run.sh node
docker compose -p bc-b-spike -f .scratch/b-spike/harness/compose.yml run --rm -T runner bash .scratch/b-spike/harness/run.sh bun
docker image inspect bc-b-spike-runner --format '{{.Id}} {{.Size}}'
docker compose -p bc-b-spike -f .scratch/b-spike/harness/compose.yml down
```

Use a fresh Compose project or bring down the spike's own services between pairs. Database and object data are on container tmpfs. The four database names come from the spike's bootstrap SQL, and the service binds its HTTP port to loopback. The runner starts with empty fixture databases and reads the existing repository lockfile and migration journal. Its HTTP and S3 test credentials are synthetic. The container network is internal. No external endpoints or inherited database URLs are needed.

The Node HTTP client drives the same real HTTP suite against both candidate servers. The app, media probes, network probes and Vitest execute inside the candidate runtime. The Vitest worker setup asserts the runtime identity before the suites. Evidence goes to the sibling `evidence` directory. Stage exit codes and timings appear in `*-stages.tsv`. The JSON reports contain raw samples, fixture classifications and process versions.

Bun 1.4.4 and Node 24.19.0 use the same Debian 13 container, 2 CPU quota, 1 GiB memory limit, PostgreSQL 18.6 service and provisioned ffprobe build. The Dockerfile pins the runtime inputs and verifies the Node and ffmpeg package digests. Its shared test image includes both runtime binaries plus the baseline test tooling. The reported image size includes that tooling. The Debian candidate is ffmpeg package `7:7.1.5-0+deb13u1` with SHA-256 `8669bc77e55393c14e0dd257ba9a916007cb6da0e1074c9b5e1d53812024f2af`. The application fixture bytes and their expected normalized results come from the repository.

The SSRF policy suite tests the application's resolver and redirect guards. A separate transport probe uses a disposable local TLS server and a synthetic certificate to exercise actual socket pinning and SNI without any outside traffic. The AvalAI probe uses the repository's SDK client configuration and redirects the SDK's fetch transport to a local server that never responds. It observes the full timeout periods and checks the request count.

To check the existing worker deployment preflight, run the following after the matrix has provisioned the private bucket and before bringing down the project:

```sh
for runtime in node bun; do
  if [ "$runtime" = node ]; then
    set -- node --import tsx
  else
    set -- bun --tsconfig-override .scratch/b-spike/harness/tsconfig.json
  fi
  docker compose -p bc-b-spike -f .scratch/b-spike/harness/compose.yml run --rm -T \
    -e BETTER_AUTH_SECRET=fixture-spike-local-only-long-enough \
    -e "DATABASE_URL=postgresql://postgres:spike-disposable-only@db:5432/spike_${runtime}_http_test" \
    runner "$@" .scratch/b-spike/harness/preflight.mts
done
```
