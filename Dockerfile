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

# Use ubuntu image to install keytool binary
# Openssl image already present in Ubuntu
# A distroless image cannot be used here: certProcessor shells out to keytool (from the JRE)
# and openssl at runtime to build the PKCS12/JKS keystores.
FROM ubuntu:26.04

LABEL org.opencontainers.image.title="Strimzi Schema Registry Operator" \
      org.opencontainers.image.description="Kubernetes operator that manages Confluent Schema Registry for Strimzi Kafka, including its TLS keystores." \
      org.opencontainers.image.source="https://github.com/Randsw/schema-registry-operator-strimzi"

# keytool is shipped by the headless JRE; the GUI libraries pulled in by default-jre are
# not needed, so use the smaller default-jre-headless package instead.
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get -y install --no-install-recommends default-jre-headless openssl \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /
COPY --from=builder /workspace/bin/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
