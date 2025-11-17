# Docker Build and Publish Workflow

This document describes the Docker build and publish process for lsget.

## Automated Builds

Docker images are automatically built and published to GitHub Container Registry (ghcr.io) via GitHub Actions.

### Trigger Conditions

The workflow runs when:
- **Tags are pushed** matching pattern `v*` (e.g., `v1.0.0`, `v2.1.3`)
- **Manual trigger** via GitHub Actions UI (`workflow_dispatch`)

### Build Process

1. **Multi-platform builds**: Images are built for:
   - `linux/amd64` (x86_64)
   - `linux/arm64` (ARM64/aarch64)

2. **Image tagging**: Automatically generates tags:
   - `ghcr.io/dyne/lsget:v1.0.0` (full semver)
   - `ghcr.io/dyne/lsget:1.0` (major.minor)
   - `ghcr.io/dyne/lsget:1` (major)
   - `ghcr.io/dyne/lsget:latest` (for main branch)

3. **Version information**: The version is embedded in the binary using:
   ```bash
   -ldflags="-X main.version=${VERSION}"
   ```

4. **Build cache**: Uses GitHub Actions cache for faster builds

## Manual Build

To build the Docker image locally:

```bash
# Build for local architecture
docker build -t lsget:local .

# Build with version
docker build --build-arg VERSION=v1.0.0 -t lsget:v1.0.0 .

# Build for multiple platforms (requires buildx)
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=v1.0.0 \
  -t ghcr.io/dyne/lsget:v1.0.0 \
  --push .
```

## Testing the Image

```bash
# Test with default configuration
docker run --rm ghcr.io/dyne/lsget:latest -version

# Test with environment variables
docker run --rm \
  -e LSGET_ADDR=0.0.0.0:8080 \
  -e LSGET_DIR=/data \
  -e LSGET_LOGFILE=/logs/access.log \
  -v $(pwd)/files:/data \
  -p 8080:8080 \
  ghcr.io/dyne/lsget:latest

# Test with docker-compose
docker-compose up
```

## Image Details

### Base Images
- **Build stage**: `golang:1.24-alpine` - Minimal Go build environment
- **Runtime stage**: `gcr.io/distroless/static-debian12:nonroot` - Google's hardened distroless image

### Runtime Dependencies
- **None** - Fully static binary with ca-certificates embedded
- No shell, no package manager, no GNU utilities
- Minimal attack surface

### Security Features
- ✅ **Distroless base** - No shell, no package manager
- ✅ **Non-root user** - Runs as UID 65532 (`nonroot`)
- ✅ **Static binary** - No CGO, no dynamic linking
- ✅ **Zero Go dependencies** - Uses only Go standard library
- ✅ **Vendored JS assets** - JavaScript dependencies embedded in binary
- ✅ **Minimal size** - Only ~11MB vs ~31MB with Alpine
- ✅ **Reduced CVEs** - Minimal software = minimal vulnerabilities
- ✅ **Immutable** - No way to exec into container or modify it

### Trade-offs
- ❌ **No debugging** - Can't `docker exec` into container (no shell)
- ❌ **No healthcheck command** - Platforms must use external health checks
- ⚠️ **Permission setup** - Volumes must be writable by UID 65532

### Volumes
- `/data` - Directory for serving files
- `/logs` - Directory for access logs

### Ports
- `8080` - HTTP server port (configurable via `LSGET_ADDR`)

## Publishing New Versions

To publish a new version:

1. **Tag the release**:
   ```bash
   git tag -a v1.0.0 -m "Release version 1.0.0"
   git push origin v1.0.0
   ```

2. **GitHub Actions automatically**:
   - Builds multi-platform images
   - Tags with semver patterns
   - Pushes to GitHub Container Registry
   - Generates build provenance attestation

3. **Verify the build**:
   - Check GitHub Actions workflow status
   - Pull and test the image:
     ```bash
     docker pull ghcr.io/dyne/lsget:v1.0.0
     docker run --rm ghcr.io/dyne/lsget:v1.0.0 -version
     ```

## Container Registry

Images are published to: `ghcr.io/dyne/lsget`

View all published versions: https://github.com/dyne/lsget/pkgs/container/lsget

## Environment Variables

The Docker image honors all `LSGET_*` environment variables. See the main README.md for full configuration options.

Default environment variables in docker-compose:
- `LSGET_ADDR=0.0.0.0:8080`
- `LSGET_DIR=/data`
- `LSGET_LOGFILE=/logs/access.log`
- `LSGET_CATMAX=4096`
