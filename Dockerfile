# Build the kubeinfo binary
FROM golang:1.23 AS builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

COPY . .

# Build
RUN make build

# Use ubuntu image to install keytool binary
# Openssl image already present in Ubuntu
FROM ubuntu:24.04
WORKDIR /
RUN apt update && apt -y install --no-install-recommends default-jre openssl && apt-get clean && rm -rf /var/lib/apt/lists/*
COPY --from=builder /workspace/bin/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]

# FROM gcr.io/distroless/static:nonroot
# WORKDIR /
# COPY --from=builder /workspace/sr-operator .
# USER 65532:65532

# ENTRYPOINT ["/sr-operator"]