# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

WORKDIR /build

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and vendored dependencies
COPY . .

# Build arguments
ARG VERSION=dev

# Build the application with version info
# Static binary with embedded assets (vendor/ only used for JS, not Go)
RUN CGO_ENABLED=0 GOOS=linux go build \
    -mod=mod \
    -ldflags="-w -s -X main.version=${VERSION}" \
    -a -installsuffix cgo \
    -o lsget .

# Runtime stage - Distroless for maximum security
# No shell, no package manager, minimal attack surface
FROM gcr.io/distroless/static-debian12:nonroot

# Copy CA certificates for HTTPS
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy binary from builder
COPY --from=builder /build/lsget /app/lsget

# Distroless runs as nonroot user (UID 65532) by default
# No need to create user or switch

# Expose default port
EXPOSE 8080

# Set volumes for data and logs
# Note: Volumes must be writable by UID 65532 (nonroot user)
VOLUME ["/data", "/logs"]

# Run lsget directly - no entrypoint script needed
# Configuration via environment variables:
# LSGET_ADDR=0.0.0.0:8080
# LSGET_DIR=/data
# LSGET_LOGFILE=/logs/access.log
ENTRYPOINT ["/app/lsget"]
