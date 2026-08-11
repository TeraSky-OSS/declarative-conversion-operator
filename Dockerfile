# Build any of this repo's three binaries from one Dockerfile via the
# COMPONENT build arg, so all three stay version-matched (built from the
# exact same source tree) without duplicating build-stage maintenance
# across separate Dockerfiles.
#
#   docker build --build-arg COMPONENT=manager        -t xrd-conversion-operator:manager .
#   docker build --build-arg COMPONENT=webhook-server  -t xrd-conversion-operator:webhook-server .
#   docker build --build-arg COMPONENT=xrdconvctl       -t xrd-conversion-operator:xrdconvctl .

ARG GO_VERSION=1.26.5

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS builder
ARG TARGETOS
ARG TARGETARCH
ARG COMPONENT=manager
ARG VERSION=dev

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/app ./cmd/${COMPONENT}

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/app /app
USER 65532:65532

ENTRYPOINT ["/app"]
