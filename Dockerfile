# Build stage
FROM --platform=$BUILDPLATFORM golang:1.25.5-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Install build dependencies
RUN apk add --no-cache make git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the driver for target platform
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} make build

# Final stage - use distroless or minimal base to avoid trigger issues
FROM alpine:3.23

# Install runtime dependencies
# Note: apk exit code 4 means trigger scripts failed but packages are installed (expected under QEMU emulation)
RUN apk add --no-cache \
    ca-certificates \
    nfs-utils \
    e2fsprogs \
    e2fsprogs-extra \
    xfsprogs \
    blkid \
    util-linux \
    eudev \
    nvme-cli \
    || [ $? -eq 4 ]

# Copy the driver binary
COPY --from=builder /workspace/bin/tns-csi-driver /usr/local/bin/

# Set the entrypoint
ENTRYPOINT ["/usr/local/bin/tns-csi-driver"]
