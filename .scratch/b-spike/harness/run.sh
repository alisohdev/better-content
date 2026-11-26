#!/usr/bin/env bash
set -u

runtime=${1:?Specify bun or node}
spike=.scratch/b-spike/harness
case "$runtime" in
  bun) candidate=(bun --tsconfig-override "$spike/tsconfig.json"); vitest=(bun);;
  node) candidate=(node --import tsx); vitest=(node);;
  *) echo "Unknown runtime" >&2; exit 2;;
esac
export DATABASE_URL="postgresql://postgres:spike-disposable-only@db:5432/spike_${runtime}_http_test"
export TEST_DATABASE_URL="postgresql://postgres:spike-disposable-only@db:5432/spike_${runtime}_vitest_test"
export BETTER_AUTH_SECRET=fixture-spike-local-only-long-enough
export SPIKE_CANDIDATE="$runtime"
export TZ=UTC
export NO_COLOR=1

service=
cleanup() {
  if [[ -n "$service" ]]; then
    kill "$service" 2>/dev/null || true
    wait "$service" 2>/dev/null || true
  fi
}
trap cleanup EXIT

problems=0
stage() {
  local name=$1 started status
  shift
  started=$(date +%s%3N)
  timeout 150 "$@" >"/evidence/$runtime-$name.log" 2>&1
  status=$?
  printf '%s\t%s\t%s\n' "$name" "$status" "$(( $(date +%s%3N) - started ))" | tee -a "/evidence/$runtime-stages.tsv"
  if (( status != 0 )); then problems=$((problems + 1)); fi
}

printf 'stage\texit\tmilliseconds\n' >"/evidence/$runtime-stages.tsv"
printf 'command=' >"/evidence/$runtime-environment.txt"
printf '%q ' "${candidate[@]}" "$spike/serve-$runtime.mts" >>"/evidence/$runtime-environment.txt"
printf '\n' >>"/evidence/$runtime-environment.txt"
{
  uname -smr
  cat /etc/os-release
  if [[ -f /sys/fs/cgroup/cpu.max ]]; then
    printf 'cpu.max='; cat /sys/fs/cgroup/cpu.max
    printf 'memory.max='; cat /sys/fs/cgroup/memory.max
  else
    printf 'cpu.cfs_quota_us='; cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us
    printf 'cpu.cfs_period_us='; cat /sys/fs/cgroup/cpu/cpu.cfs_period_us
    printf 'memory.limit_in_bytes='; cat /sys/fs/cgroup/memory/memory.limit_in_bytes
  fi
  stat -Lc '%s bytes %n' "$(command -v "$runtime")"
  "${candidate[@]}" -e 'console.log(JSON.stringify(process.versions))'
  ffprobe -version | sed -n '1p'
  sha256sum "$FFPROBE_PATH"
  dpkg-query -W ffmpeg libavformat61 libavcodec61 libavutil59
} >>"/evidence/$runtime-environment.txt"

stage prepare "${candidate[@]}" "$spike/prepare.mts"
if [[ $problems -eq 0 ]]; then
  SPIKE_WORKSPACE_ID=$(grep -E '^[0-9a-f-]{36}$' "/evidence/$runtime-prepare.log" | tail -n 1)
  export SPIKE_WORKSPACE_ID SPIKE_START_MS=$(date +%s%3N)
  "${candidate[@]}" "$spike/serve-$runtime.mts" >"/evidence/$runtime-server.log" 2>&1 &
  service=$!
  stage http node --import tsx "$spike/http.mts" "$runtime"
  cleanup
  service=
fi

stage media "${candidate[@]}" "$spike/media.mts"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=media.spike.invalid \
  -addext subjectAltName=DNS:media.spike.invalid -keyout /tmp/spike.key -out /tmp/spike.crt 2>/dev/null
stage tls env NODE_EXTRA_CA_CERTS=/tmp/spike.crt "${candidate[@]}" "$spike/network.mts" tls
stage s3-prepare "${candidate[@]}" "$spike/network.mts" s3

runner=("${vitest[@]}" node_modules/vitest/vitest.mjs run --config "$spike/vitest.config.mts")
stage domain "${runner[@]}" /domain/
stage ssrf "${runner[@]}" controlled-remote-acquisition.test.ts
stage integration env SPIKE_VITEST_INTEGRATION=1 "${runner[@]}" default-workspace.integration.test.ts
stage s3 env ASSET_STORAGE_LIVE_CONTRACT=1 "${runner[@]}" s3-compatible-asset-storage.live.test.ts
stage ai "${candidate[@]}" "$spike/network.mts" ai

(( problems == 0 ))
