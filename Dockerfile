# appshell-controller's image: a static binary on distroless, running as non-root.
#
# Both base images are pinned by digest, so a build is reproducible and a tag moving upstream
# can't change what ships; Dependabot proposes the next digest as a reviewed change.

# The builder runs on the build machine's platform and cross-compiles for the target, rather than
# emulating it: Go needs no target toolchain, and emulated builds are slow.
FROM --platform=$BUILDPLATFORM golang:1.26@sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Modules first, so a source change doesn't invalidate the downloaded layer.
COPY go.mod go.sum ./
RUN go mod download

# The source, filtered by .dockerignore.
COPY . .

# Static, and reproducible: -trimpath keeps build paths out of the binary, and -s -w drop the
# symbol and debug tables a distroless image has no tools to read.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o manager cmd/main.go

# No shell, no package manager, nothing but the binary and CA certificates.
FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
