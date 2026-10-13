# syntax=docker/dockerfile:1
# Tool box images (SPEC §2.6). Build from the repository root; the targets are
# the two profiles of BOTHY_SBX_IMAGES:
#
#   docker buildx build -f deploy/sandbox/Dockerfile --target base \
#     --platform linux/amd64,linux/arm64 -t ghcr.io/bothyhq/bothy-sandbox:0.1.0 .
#   docker buildx build -f deploy/sandbox/Dockerfile --target node \
#     --platform linux/amd64,linux/arm64 -t ghcr.io/bothyhq/bothy-sandbox-node:0.1.0 .
#
# Dockerfile.dockerignore limits the context to what these stages read.

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.2-trixie@sha256:e58d6f83b3416618d8bcac2b3dde1b7f7e3c4a77d25e88637f8bbae81536c48d AS sbxinit
ARG TARGETOS TARGETARCH
ENV GOTOOLCHAIN=local GOFLAGS=-mod=readonly
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/sbx-init/ cmd/sbx-init/
COPY internal/sbxinit/ internal/sbxinit/
COPY internal/sandboxd/proto/ internal/sandboxd/proto/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/sbx-init ./cmd/sbx-init

FROM ghcr.io/astral-sh/uv:0.12.19@sha256:04d046b13e60d6bcec73cbc5e1cad25d680dea90c8573340950a0ac2d1aef424 AS uv

FROM docker.io/library/node:24.21.0-trixie-slim@sha256:173f125896c3b47ddf056734c7ea789d04595a6a08769a8f78e0df642781fb66 AS nodejs

FROM docker.io/library/debian:trixie-20260918-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a AS base
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      python3 python3-venv bash coreutils git curl ca-certificates jq ripgrep unzip procps \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/* \
 && groupadd -g 10001 sbx \
 && useradd -u 10001 -g 10001 -d /home/sbx -M -s /bin/bash sbx \
 && install -d -m 0755 /opt/bothy /opt/mcp /workspace /skills /run/egress /home/sbx
COPY --from=uv /uv /uvx /usr/local/bin/
COPY --from=sbxinit /out/sbx-init /opt/bothy/sbx-init
COPY deploy/sandbox/gitconfig /etc/gitconfig
# Nothing in a box may gain privileges: strip every setuid/setgid bit.
RUN find / -xdev -perm /6000 -type f -exec chmod a-s {} +
ENV HOME=/home/sbx LANG=C.UTF-8 PATH=/usr/local/bin:/usr/bin:/bin
USER 10001:10001
WORKDIR /workspace
CMD ["/opt/bothy/sbx-init"]

FROM base AS node
USER 0:0
COPY --from=nodejs /usr/local/bin/node /usr/local/bin/node
COPY --from=nodejs /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
 && find / -xdev -perm /6000 -type f -exec chmod a-s {} +
USER 10001:10001
