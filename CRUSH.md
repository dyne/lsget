# CRUSH.md - Codebase Guidelines for lsget

## Build/Test/Lint Commands
```bash
# Download vendored JavaScript assets (first time or to update)
cd static && ./download-assets.sh && cd ..

# Build
go build .

# Run development server with hot-reload
task dev

# Tidy dependencies
task tidy
go mod tidy

# Lint (requires golangci-lint)
task lint
golangci-lint run ./...

# Run tests (standard Go testing)
go test ./...
go test -v ./...  # verbose
go test -run TestName  # run single test
```

## Code Style Guidelines
- **Language**: Go 1.24.5
- **Imports**: Group stdlib, then external deps, then internal packages
- **Formatting**: Use `gofmt` (standard Go formatting)
- **Naming**: CamelCase for exports, camelCase for unexported
- **Error Handling**: Return errors, check with `if err != nil`
- **Comments**: Minimal, only when necessary for clarity
- **Constants**: Define color codes and config as const blocks
- **HTTP**: Use standard `net/http` with `http.ServeMux`
- **File Structure**: Single `main.go` for simplicity
- **Embedded Assets**: Use `//go:embed` for index.html and static JS dependencies
- **Privacy**: All JavaScript assets vendored in `static/` (no CDN dependencies)