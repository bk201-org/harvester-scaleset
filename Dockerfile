# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.0

FROM golang:${GO_VERSION}-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG VERSION=dev
ARG COMMIT_SHA=unknown
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commitSHA=${COMMIT_SHA}" \
    -o /out/harvester-scaleset \
    ./cmd/harvester-scaleset

FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev
ARG COMMIT_SHA=unknown
LABEL org.opencontainers.image.source="https://github.com/bk201-org/harvester-scaleset" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT_SHA}"

COPY --from=builder /out/harvester-scaleset /usr/local/bin/harvester-scaleset

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/harvester-scaleset"]
