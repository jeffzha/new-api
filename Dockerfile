FROM oven/bun:1.4.0@sha256:5ff609364c049b54eb0ff560ec96319729a972078ef2c755d758f0c6ef89c2d6 AS builder

ARG BUILD_VERSION
ARG BUN_REGISTRY=https://registry.npmjs.org
ARG BUN_MAX_HTTP_REQUESTS=48
ENV BUN_CONFIG_REGISTRY=${BUN_REGISTRY} BUN_CONFIG_MAX_HTTP_REQUESTS=${BUN_MAX_HTTP_REQUESTS}

WORKDIR /build/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY ./web ./
COPY ./VERSION /build/VERSION
RUN version="${BUILD_VERSION:-$(cat /build/VERSION)}" \
    && DISABLE_ESLINT_PLUGIN='true' VITE_REACT_APP_VERSION="$version" bun run build

# Agency's lockfileVersion 2 requires Bun 1.4; keep frozen-lockfile validation.
FROM oven/bun:1.4.2@sha256:9114c058aeae42162ee16dd5084b95fe9473970bb6bcb5b232ab1630f0546895 AS agency-web-builder
ARG BUN_REGISTRY=https://registry.npmjs.org
ENV BUN_CONFIG_REGISTRY=${BUN_REGISTRY}
WORKDIR /build/agency-web
COPY agency-web/package.json agency-web/bun.lock ./
RUN bun install --frozen-lockfile
COPY agency-web ./
RUN bun run build

FROM golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df AS builder2
ARG BUILD_VERSION
ARG GO_PROXY=https://proxy.golang.org,direct
ENV GO111MODULE=on CGO_ENABLED=0 GOWORK=off GOPROXY=${GO_PROXY}

ARG TARGETOS
ARG TARGETARCH
ENV GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64}
ENV GOEXPERIMENT=greenteagc

WORKDIR /build

ADD go.mod go.sum ./
# relaykit is a local submodule referenced via replace; its go.mod must be
# present for go mod download to resolve the main module graph.
ADD relaykit/go.mod ./relaykit/go.mod
RUN go mod download

COPY . .
COPY --from=builder /build/web/dist ./web/dist
COPY --from=agency-web-builder /build/agency-web/dist ./pkg/agencyhub/webdist
RUN version="${BUILD_VERSION:-$(cat VERSION)}" \
    && go build -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=${version}'" -o new-api \
    && go build -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=${version}'" -o hwdrama-proxy ./cmd/hwdrama-proxy \
    && go build -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=${version}'" -o reverse-newapi-volcengine ./cmd/reverse-newapi-volcengine \
    && go build -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=${version}'" -o enterprise-policy-hub ./cmd/enterprise-policy-hub \
    && go build -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=${version}'" -o reseller-hub ./cmd/reseller-hub \
    && go build -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=${version}'" -o agency-hub ./cmd/agency-hub

FROM debian:bookworm-slim@sha256:f06537653ac770703bc45b4b113475bd402f451e85223f0f2837acbf89ab020a AS agency-hub

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata libasan8 wget \
    && rm -rf /var/lib/apt/lists/* \
    && update-ca-certificates

COPY --from=builder2 /build/new-api /
COPY --from=builder2 /build/hwdrama-proxy /
COPY --from=builder2 /build/reverse-newapi-volcengine /
COPY --from=builder2 /build/enterprise-policy-hub /
COPY --from=builder2 /build/reseller-hub /
COPY --from=builder2 /build/agency-hub /
COPY LICENSE NOTICE THIRD-PARTY-LICENSES.md /licenses/
EXPOSE 3000
WORKDIR /data
ENTRYPOINT ["/new-api"]
