# Dockerfile — multi-stage build for PRP GNS3 container
#
# Build stage
FROM golang:1.23-alpine AS builder
WORKDIR /build

# Release version (e.g. v0.5.3), injected into the binaries' version var.
ARG VERSION=dev

# Cache dependencies (changes infrequently)
COPY go.mod go.sum ./
RUN go mod download

# Build the binary
COPY . .
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X prp-gns3/internal/version.Version=${VERSION}" \
    -trimpath \
    -o /prpd ./cmd/prpd \
 && CGO_ENABLED=0 go build \
    -ldflags="-s -w -X prp-gns3/internal/version.Version=${VERSION}" \
    -trimpath \
    -o /usr/local/bin/trafficgen ./cmd/trafficgen

# Runtime stage
FROM alpine:3.19

# Install runtime dependencies:
#   iproute2      — ip link/addr for interface setup
#   busybox-extras — provides telnetd (not in Alpine's minimal busybox)
#                     for GNS3 console access on port 5000
RUN apk add --no-cache iproute2 busybox-extras tcpdump

# Copy only the binary and static files from builder
COPY --from=builder /prpd /usr/local/bin/prpd
COPY --from=builder /usr/local/bin/trafficgen /usr/local/bin/trafficgen
COPY config.yaml /etc/prp/config.yaml
COPY entrypoint.sh /entrypoint.sh

# GNS3 console port (telnetd)
EXPOSE 5000/tcp

# Health check — prpd should be running
HEALTHCHECK --interval=10s --timeout=3s --start-period=2s --retries=3 \
    CMD pgrep -x prpd >/dev/null || exit 1

LABEL org.opencontainers.image.title="PRP GNS3 Simulation Container" \
      org.opencontainers.image.description="PRP (IEC 62439-3) RedBox simulation node for GNS3" \
      org.opencontainers.image.source="https://github.com/grymme/prp-sim" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.vendor="Arctic3D AB"

RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
