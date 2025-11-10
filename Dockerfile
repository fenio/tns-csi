# Build stage
FROM golang:1.25.4-alpine AS builder

WORKDIR /workspace

# Install build dependencies
RUN apk add --no-cache make git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the driver
RUN make build

# Final stage
FROM alpine:3.22

# Install runtime dependencies
RUN apk add --no-cache \
    ca-certificates \
    nfs-utils \
    open-iscsi \
    e2fsprogs \
    e2fsprogs-extra \
    xfsprogs \
    blkid \
    util-linux \
    nvme-cli

# Copy the driver binary
COPY --from=builder /workspace/bin/tns-csi-driver /usr/local/bin/

# Set the entrypoint
ENTRYPOINT ["/usr/local/bin/tns-csi-driver"]
