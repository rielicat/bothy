# syntax=docker/dockerfile:1
# Gateway image (SPEC §2.3): one static binary on distroless, uid 65532.
#
#   docker buildx build -f deploy/Dockerfile --build-arg VERSION=0.1.0 \
#     --platform linux/amd64,linux/arm64 -t ghcr.io/rielicat/bothy:0.1.0 .
#
# Releases use the binaries goreleaser builds instead (deploy/Dockerfile.release);
# both produce the same layout.

FROM --platform=$BUILDPLATFORM docker.io/library/node:24.21.0-trixie-slim@sha256:173f125896c3b47ddf056734c7ea789d04595a6a08769a8f78e0df642781fb66 AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json web/.npmrc ./
RUN npm ci --ignore-scripts
COPY web/ ./
RUN mkdir -p /src/internal/webui && npm run build

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.2-trixie@sha256:e58d6f83b3416618d8bcac2b3dde1b7f7e3c4a77d25e88637f8bbae81536c48d AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=ui /src/internal/webui/dist/ internal/webui/dist/
RUN if [ "$TARGETARCH" = arm ]; then export GOARM="${TARGETVARIANT#v}"; fi \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/bothy ./cmd/bothy
# Named volumes copy ownership and mode from the image on first mount, so the
# layout is deterministic whichever container mounts a volume first.
RUN install -d -m 0700 -o 65532 -g 65532 /rootfs/data /rootfs/keys \
 && install -d -m 2770 -o 65532 -g 10001 /rootfs/work \
 && install -d -m 0755 /rootfs/run \
 && install -d -m 0770 -o 65532 -g 65532 /rootfs/run/bothy

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /rootfs/ /
COPY --from=build /out/bothy /bothy
ENV BOTHY_DEPLOYMENT=container
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --retries=3 --start-period=30s --start-interval=1s CMD ["/bothy", "healthcheck"]
ENTRYPOINT ["/bothy"]
CMD ["serve"]
