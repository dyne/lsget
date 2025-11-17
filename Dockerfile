# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

WORKDIR /build

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build arguments
ARG VERSION=dev

# Build the application with version info
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s -X main.version=${VERSION}" \
    -o lsget .

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (ca-certificates for HTTPS, curl for healthcheck)
RUN apk --no-cache add ca-certificates curl

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/lsget .

# Create directories for serving files and logs
RUN mkdir -p /data /logs

# Expose default port
EXPOSE 8080

# Run as non-root user
RUN adduser -D -u 1000 lsget && \
    chown -R lsget:lsget /app /data /logs
USER lsget

# Set volumes for data and logs
VOLUME ["/data", "/logs"]

# Run lsget - configuration via environment variables
# Default env vars (can be overridden):
# LSGET_ADDR=0.0.0.0:8080
# LSGET_DIR=/data
# LSGET_LOGFILE=/logs/access.log
CMD ["/app/lsget"]
