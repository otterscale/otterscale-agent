# Build stage
FROM golang:1.25.6@sha256:fc24d3881a021e7b968a4610fc024fba749f98fe5c07d4f28e6cfa14dc65a84c AS builder

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build the application
RUN make build

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot@sha256:cba10d7abd3e203428e86f5b2d7fd5eb7d8987c387864ae4996cf97191b33764

WORKDIR /

# Copy the binary from the builder stage
COPY --from=builder /workspace/bin/otterscale .

# Switch to non-root user
USER 65532:65532

# Set environment variable
ENV OTTERSCALE_SERVER_TUNNEL_ADDRESS=0.0.0.0:8300

# Expose ports (8299: HTTP/gRPC API, 8300: Tunnel)
EXPOSE 8299 8300

# Labels
LABEL maintainer="Chung-Hsuan Tsai <paul_tsai@phison.com>"

ENTRYPOINT ["/otterscale"]
CMD ["server"]