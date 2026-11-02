# Build the manager binary
ARG BUILDER_ARCH=multiarch
FROM registry.access.redhat.com/ubi9/go-toolset:1.26.10@sha256:5ee38c1f5fc2dfa5c5257928912cc26857f4186c07d836271a2d748dc79b083a AS go-toolset-multiarch
FROM registry.access.redhat.com/ubi9/go-toolset:1.26.10@sha256:c073288dc64060205635575a473f37792cec0fed03c8dbcb5d9ed9415cb0f18e AS go-toolset-arm64
FROM go-toolset-${BUILDER_ARCH} AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN GOTOOLCHAIN=local go mod download

# Copy the go source
COPY main.go main.go
COPY tls_profile.go tls_profile.go
COPY api/ api/
COPY controllers/ controllers/

# Build
USER root
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOFIPS140=v1.0.0 \
    go build -tags no_openssl -a -o manager .

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest


WORKDIR /
COPY --from=builder /workspace/manager .
COPY config/internal config/internal

ARG USER=65532
USER ${USER}:${USER}

ENTRYPOINT ["/manager"]
