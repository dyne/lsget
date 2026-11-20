# AGENTS.md

## Project layout

- lsget is a read-only HTTP file server with a Unix-style terminal in the browser. It ships as one Go binary. Keep browser commands implemented in Go and preserve the read-only contract for HTTP clients.
- `main.go` contains the CLI flags and `LSGET_*` defaults, the `net/http` routes, in-memory sessions identified by a cookie, virtual path handling, file commands, ZIP downloads, and sitemap generation. The server handles file paths relative to the root set by `-dir` or `LSGET_DIR`.
- `index.html` contains the terminal UI and calls `/api/config`, `/api/exec`, `/api/complete`, and `/api/download`. When rendering the terminal, the server loads this file from its working directory if available and otherwise uses the embedded copy. Datastar and marked are vendored in `assets/js/` and embedded in the binary.
- The catch-all route serves files and directory-local `index.html` files directly. Directories without an index get the terminal or a plain listing with `?nojs=1`. Check both directory views when changing browsing behavior.
- `README.md` documents the commands and configuration. `.env.example` shows deployment settings. `Dockerfile`, `docker-compose.yaml`, and `deploy/` cover container, systemd, and OpenRC deployments. `CRUSH.md` contains the original development notes.

## Development and checks

Run commands from the directory containing `go.mod`. Use Go 1.24.5 or newer. The Go commands work without Task or mise.

- `go test ./...` runs the Go suite. Use `go test -run '^TestName$' -count=1 .` for a focused check or `task test` for the verbose suite.
- `go build .` builds the binary. `go tool golangci-lint run ./...` runs the project-pinned linter, also exposed as `task lint`. Format edited Go code with `gofmt`.
- Run `go run . -addr 127.0.0.1:8080 -dir /path/to/test-fixtures` for a local smoke test. Use a fixture directory because the default served root is the current directory. For terminal testing, use a fixture root without its own `index.html`. `task dev` runs Air after `go mod tidy`, which can modify dependency files.
- For HTTP tests, follow the `httptest` and fixture patterns in `handlers_test.go` and `utils_test.go`. Other tests cover downloads, ignore rules, startup, and environment defaults. When editing routes or UI behavior, check the browser flow in addition to the handler response.

## Behavior to preserve

- Keep browser-visible paths slash-delimited and rooted at `/`. Use the virtual path helpers at the filesystem boundary. For changes involving file access, check root containment and the relevant `.lsgetignore` behavior across the affected listing, serving, search, or download paths.
- Keep response payloads and session working directories compatible with the UI. Escape filenames and paths for their destination context, especially links, template substitutions, and the no-JavaScript listing.
- When command syntax or configuration changes, update the in-app help, `README.md`, and any affected examples or deployment defaults.

## GitHub workflow

Use a feature branch and follow the contribution steps in `README.md`. The release workflow lints and tests pushes and pull requests targeting `main`. Semantic release and container publishing run on `main`.
