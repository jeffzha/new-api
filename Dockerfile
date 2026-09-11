FROM oven/bun:1@sha256:0733e50325078969732ebe3b15ce4c4be5082f18c4ac1a0f0ca4839c2e4e42a7 AS builder

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
