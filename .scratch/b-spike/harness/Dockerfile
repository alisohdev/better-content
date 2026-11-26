FROM oven/bun:1.4.4@sha256:1d22548f177f4ff38e0c15baa3c2c46c6c2060ba2d1bc573bb4720b407548f1d AS dependencies
WORKDIR /app
RUN mkdir -p /opt/node /opt/npm && \
    bun -e 'const [name, version, integrity] = process.argv.slice(1); const response = await fetch("https://registry.npmjs.org/" + name + "/-/" + name + "-" + version + ".tgz"); if (!response.ok) throw Error(String(response.status)); const bytes = await response.arrayBuffer(); if (new Bun.CryptoHasher("sha512").update(bytes).digest("base64") !== integrity) throw Error("Integrity mismatch"); await Bun.write("/tmp/" + name + ".tgz", bytes);' node-linux-x64 24.19.0 vRk8mXc3mi3oveQf8wNrLKMuJWK7mBEi2ASQ8+Tv/0QjebLcsdodqVzE0xvCKPZ3575pG2jfWsKnfCVkoeIKTw== && \
    bun -e 'const response = await fetch("https://registry.npmjs.org/npm/-/npm-11.13.0.tgz"); if (!response.ok) throw Error(String(response.status)); const bytes = await response.arrayBuffer(); if (new Bun.CryptoHasher("sha512").update(bytes).digest("base64") !== "cRmhaghDWA1lFgl3Ug4/VxDJdPBK/U+tNtnrl9kXunFqhWw1x4xL5txkNn7qzPuVfvXOmXyjHpMwsuk2uisbkg==") throw Error("Integrity mismatch"); await Bun.write("/tmp/npm.tgz", bytes);' && \
    tar -xzf /tmp/node-linux-x64.tgz --strip-components=1 -C /opt/node && \
    tar -xzf /tmp/npm.tgz --strip-components=1 -C /opt/npm && \
    ln -s /opt/npm/bin/npm-cli.js /opt/node/bin/npm && rm /tmp/node-linux-x64.tgz /tmp/npm.tgz
ENV PATH="/opt/node/bin:${PATH}"
RUN sed -i s,http://deb.debian.org,https://deb.debian.org,g /etc/apt/sources.list.d/debian.sources && \
    apt-get update -qq && cd /tmp && \
    apt-get download 'ffmpeg=7:7.1.5-0+deb13u1' && \
    printf '%s  %s\n' '8669bc77e55393c14e0dd257ba9a916007cb6da0e1074c9b5e1d53812024f2af' ffmpeg_*.deb | sha256sum -c - && \
    apt-get install -y --no-install-recommends ./ffmpeg_*.deb openssl && \
    rm -rf /var/lib/apt/lists/* /tmp/ffmpeg_*.deb
ENV FFPROBE_PATH=/usr/bin/ffprobe FFPROBE_EXPECTED_VERSION=7.1.5-0+deb13u1
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY .scratch/b-spike/harness/package*.json .scratch/b-spike/harness/
RUN npm --prefix .scratch/b-spike/harness ci --no-audit --no-fund

FROM dependencies AS spike
COPY tsconfig.json vitest.integration.config.mts ./
COPY src src
COPY api api
COPY drizzle drizzle
COPY backend/internal/assets/testdata backend/internal/assets/testdata
COPY .scratch/phase-07-go-backend-migration/fixtures .scratch/phase-07-go-backend-migration/fixtures
COPY .scratch/b-spike/harness .scratch/b-spike/harness
