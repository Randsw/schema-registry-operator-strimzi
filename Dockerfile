# syntax=docker/dockerfile:1

# Build the manager binary.
# The builder always runs on the build platform and cross-compiles for the target platform,
# so multi-arch builds don't require emulating the Go toolchain.
FROM --platform=$BUILDPLATFORM golang:1.27 AS builder

# TARGETOS/TARGETARCH are set by BuildKit when building for another platform and stay
# empty on a native build (where the builder's own platform is used).
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# Build
# The binary is compiled directly instead of via `make build`: its code generation targets
# download controller-gen at image build time, and the files they produce (config/, generated
# deepcopy code, ...) are committed to the repository and are not part of the image.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o bin/manager cmd/main.go

# Runtime stage: distroless/static.
# The manager is a static, CGO-free binary (CGO_ENABLED=0), and certProcessor
# encodes the JKS/PKCS12 keystores and truststores in pure Go — it no longer
# shells out to keytool (from the JRE) or openssl at runtime, which is what
# makes a minimal distroless image possible: no package manager, no JVM, and
# no TLS tooling are needed in the final image.
# The runtime stage must match the target architecture (no --platform pin):
# the builder cross-compiles, this stage supplies the actual target rootfs.
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="Strimzi Schema Registry Operator" \
      org.opencontainers.image.description="Kubernetes operator that manages Confluent Schema Registry for Strimzi Kafka, including its TLS keystores." \
      org.opencontainers.image.source="https://github.com/Randsw/schema-registry-operator-strimzi"

WORKDIR /
COPY --from=builder /workspace/bin/manager /manager

# The nonroot tag already runs as uid/gid 65532; kept explicit so the
# non-root contract does not depend on the base image tag alone.
USER 65532:65532
ENTRYPOINT ["/manager"]
