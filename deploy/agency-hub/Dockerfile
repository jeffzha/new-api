FROM oven/bun:1@sha256:0733e50325078969732ebe3b15ce4c4be5082f18c4ac1a0f0ca4839c2e4e42a7 AS frontend
ARG BUN_REGISTRY=https://registry.npmjs.org
ENV BUN_CONFIG_REGISTRY=${BUN_REGISTRY}
WORKDIR /build/agency-web
COPY agency-web/package.json agency-web/bun.lock ./
RUN bun install --frozen-lockfile
COPY agency-web ./
RUN bun run build

FROM golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df AS backend
ARG GO_PROXY=https://proxy.golang.org,direct
ARG BUILD_VERSION
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 GOWORK=off GOPROXY=${GO_PROXY} GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64}
WORKDIR /build
COPY go.mod go.sum ./
COPY relaykit/go.mod ./relaykit/go.mod
RUN go mod download
COPY . .
COPY --from=frontend /build/agency-web/dist ./pkg/agencyhub/webdist
RUN version="${BUILD_VERSION:-$(cat VERSION)}" \
    && go build -trimpath -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=${version}'" -o /out/agency-hub ./cmd/agency-hub

FROM debian:bookworm-slim@sha256:f06537653ac770703bc45b4b113475bd402f451e85223f0f2837acbf89ab020a
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata wget \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 agency-hub \
    && useradd --system --uid 10001 --gid 10001 --home-dir /data --shell /usr/sbin/nologin agency-hub \
    && mkdir -p /data /var/lib/agency-hub/exports \
    && chown 10001:10001 /data /var/lib/agency-hub/exports
COPY --from=backend /out/agency-hub /agency-hub
COPY LICENSE NOTICE THIRD-PARTY-LICENSES.md /licenses/
WORKDIR /data
USER 10001:10001
EXPOSE 3201
ENTRYPOINT ["/agency-hub"]
