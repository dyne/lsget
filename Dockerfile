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

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o lsget .

# Runtime stage
FROM alpine:latest

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/lsget .

# Create directory for serving files
RUN mkdir -p /data

# Expose default port
EXPOSE 8080

# Run as non-root user
RUN adduser -D -u 1000 lsget && \
    chown -R lsget:lsget /app
USER lsget

# Set default served directory
VOLUME ["/data"]

# Default command serves /data on 0.0.0.0:8080
ENTRYPOINT ["/app/lsget"]
CMD ["-dir", "/data", "-addr", "0.0.0.0:8080"]
