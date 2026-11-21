<div align="center">

# lsget

## Neon terminal file explorer in your browser. Read-only FTP, UNIX style interface

</div>

<p align="center">
  <a href="https://dyne.org">
    <img src="https://files.dyne.org/software_by_dyne.png" width="170">
  </a>
</p>


---
<br><br>

**lsget** turns any local directory into a mini content delivery network with a web-based Terminal User Interface (TUI). Navigate and interact with your files using familiar Unix commands like `pwd`, `ls`, `cd`, `cat`, and `download` — all from your browser with a retro terminal aesthetic.

Perfect for:
- 🌐 Quickly sharing files over your network with a terminal-style interface
- 📦 Creating a simple self-hosted CDN for static assets
- 🎨 Serving files with a nostalgic hacker-style UI
- 🔧 Development: hot‑reload your server with [Air](https://github.com/cosmtrek/air) via `task dev` for an instant Go hacking loop


## lsget features

| Capability           | Details |
|----------------------|---------|
| **TUI in browser**   | Full-screen Terminal User Interface with reactive updates powered by **[DataStar](https://github.com/starfederation/datastar)**; smooth keyboard handling & command history. |
| **CDN-ready**        | Instantly serve files from any directory with shareable URLs and download capabilities. |
| File operations      | `pwd`, `ls [-l] [-h]`, `cd DIR`, `cat FILE`, `download FILE`, `tree`, `find`, `grep`. |
| Smart autocompletion | **Complete** suggests directories and files, with text and size limits for `cat` suggestions. |
| Colourised output    | Directories in bright blue (Ubuntu style) with trailing `/`, executable files highlighted. |
| Session isolation    | Each browser tab keeps its own working directory. |
| Live reload          | `task dev` ⇒ [Air](https://github.com/cosmtrek/air) rebuilds `main.go` on save for rapid development. |
| Zero‑config binary   | `go run .` or `go build` produces a single executable with embedded assets. |
| Supply chain security| JavaScript dependencies vendored locally to reduce supply chain attacks. |

![Screenshot](./screenshot.png)
<img width="1700" height="918" alt="image" src="https://github.com/user-attachments/assets/0a4a5fce-6d09-4ef3-9211-d66d0244748d" />

## [LIVE DEMO](https://files.dyne.org)

<br>

<div id="toc">

### 🚩 Table of Contents

- [💾 Install](#-install)
- [🎮 Quick start](#-quick-start)
- [📟 Available Commands](#available-commands)
- [🚑 Community & support](#-community--support)
- [😍 Acknowledgements](#-acknowledgements)
- [👤 Contributing](#-contributing)
- [💼 License](#-license)

</div>

***
## 💾 Install

### Binary Installation
Single binary, no need to install anything!

See the [release download names and license notices](.github/RELEASING.md#binary-names) for all supported targets.

```bash
# Download latest release
curl -fsSL  "https://github.com/dyne/lsget/releases/latest/download/lsget-$(uname -s)-$(uname -m)" -o lsget && chmod +x lsget
```

### Docker

Pull the latest Docker image from GitHub Container Registry:

```bash
# Pull latest version
docker pull ghcr.io/dyne/lsget:latest

# Or pull a specific version
docker pull ghcr.io/dyne/lsget:v1.0.0

# Run with Docker
docker run -d \
  -p 8080:8080 \
  -v $(pwd)/files:/data \
  -v $(pwd)/logs:/logs \
  -e LSGET_ADDR=0.0.0.0:8080 \
  -e LSGET_LOGFILE=/logs/access.log \
  ghcr.io/dyne/lsget:latest
```

### Docker Compose

Use the included `docker-compose.yaml`:

```bash
# Start the service
docker-compose up -d

# View logs
docker-compose logs -f
```

**[🔝 back to top](#toc)**

***
## 🎮 Quick start

From the repository root, build the binary with the Go toolchain pinned in [go.mod](go.mod). If you downloaded a release, start with the version check.

```sh
go build -o lsget .
./lsget -version
./lsget -h
```

Create a scratch directory with a file to serve and its own writable state directory. Run lsget in the foreground.

```sh
demo=$(mktemp -d)
mkdir "$demo/files" "$demo/state"
printf 'Hello from lsget\n' > "$demo/files/hello.txt"
./lsget -addr 127.0.0.1:8080 -dir "$demo/files" \
  -logfile "$demo/state/access.log" -pid "$demo/state/lsget.pid"
```

Open `http://127.0.0.1:8080/` for the terminal or run `curl -fsS http://127.0.0.1:8080/hello.txt` in a second shell to check the file. Press Ctrl-C when finished, then run `rm -r "$demo"` from the first shell. For a long-running Linux service, follow the [native deployment quick start](deploy/QUICKSTART.md).

### Direct file links

A file in the served directory can be shared by its path, such as `/eudi/report.pdf`, or with the `file` query parameter, such as `/?file=eudi/report.pdf`. Both resolve from the served root. Encode special characters in each path segment and in the query value. For example, `meeting notes #1.pdf` becomes `meeting%20notes%20%231.pdf`, and a literal `+` in the query value becomes `%2B`.

For scripted downloads, use `?file=` to receive HTTP 404 for a missing or ignored file or a directory. A missing direct path can return the terminal HTML with HTTP 200. Verify the downloaded bytes against the publisher's checksum before unpacking. For example, if the release includes `release/release files.tar.gz` and you have a trusted `release.sha256` for `release-files.tar.gz`, check either URL form on a listener serving that tree.

```sh
curl -fsS -o release-files.tar.gz 'http://127.0.0.1:8080/?file=release%2Frelease%20files.tar.gz' &&
  sha256sum -c release.sha256
```

```sh
curl -fsS -o release-files.tar.gz 'http://127.0.0.1:8080/release/release%20files.tar.gz' &&
  sha256sum -c release.sha256
```

### Ignore rules

Put a `.lsgetignore` file in the served root or in a subdirectory. Use one shell-style glob per line. Blank lines and lines beginning with `#` are skipped. A rule matches the basename or the path relative to its `.lsgetignore` directory. For example, `*.log` hides matching names anywhere below that directory, and `drafts/private` hides that relative path and all its descendants. The rules support `*`, `?` and character classes. They have no Git-style `!` exceptions or special `**` syntax.

Listings, direct and query links, terminal downloads, ZIP entries, and generated sitemaps respect ignore rules. Downloads can follow symlinks when the resolved path stays inside the served root and both names pass the ignore rules. Keep private files outside the served root. Direct requests can still read dotfiles, and lsget skips ignore files it cannot read.

### Terminal sessions

Each browser tab opens at the directory in its URL and keeps track of its own working directory. The terminal sends it in the `cwd` query parameter on `/api/config`, `/api/exec`, and `/api/complete` requests. The tab retains its directory across server restarts and operates independently of the server session pool.

API callers that omit `cwd` use an in-memory session for operations that require a working directory. The `sid` cookie identifies the session and carries `HttpOnly` and `SameSite=Lax`. A session expires after 30 minutes without a request that uses it. The server retains up to 4,096 sessions and evicts the longest-idle session when it needs room. In-flight requests keep their sessions. If all slots are busy, requests that need a new session receive HTTP 503 and can retry. Callers with expired or evicted sessions, or sessions lost during a restart, start at the served root unless they specify a directory.

Direct file links, exact-path downloads, and archive requests with an explicit directory bypass server sessions. lsget sets `Secure` on session cookies for TLS requests. When an HTTPS-only reverse proxy terminates TLS, set `LSGET_SECURE_SESSION_COOKIE=true` or pass `-secure-session-cookie` and keep the lsget listener reachable only by the proxy. Forwarding headers have no effect on the cookie's `Secure` attribute.

### HTTP request limits

| Routes | Allowed methods |
| --- | --- |
| `/api/config` | `GET` |
| `/api/exec`, `/api/complete` | `POST` |
| Downloads, static assets, `/sitemap.xml`, published pages and file links | `GET`, `HEAD` |

A method outside the route's list returns HTTP 405 with an `Allow` header. Each JSON request body must contain one object and fit within 8 KiB, including whitespace. An oversized body returns HTTP 413. Invalid JSON, extra JSON values and non-object bodies return HTTP 400.

JSON requests and `/api/config?path=…` accept same-origin browser requests. If the browser sends `Sec-Fetch-Site`, it must be `same-origin`. A supplied `Origin` must match the request's host, port and TLS scheme or the origin configured in `LSGET_BASEURL`. For an HTTPS-only proxy that preserves the public `Host` header, `LSGET_SECURE_SESSION_COOKIE=true` selects the HTTPS scheme. If the proxy rewrites `Host`, set `LSGET_BASEURL` to the public URL. Forwarding headers play no part in this check.

CLI requests can omit `Origin` and `Sec-Fetch-Site`. When reusing an `sid` cookie without either header, send `Content-Type: application/json` with JSON requests and `X-Lsget-Request: 1` when setting the working directory through `/api/config?path=…`.

The server caps request headers at 64 KiB, gives clients 5 seconds to send headers and 10 seconds total to send the request, and closes idle keep-alive connections after 60 seconds. Downloads have no fixed response deadline, and direct file downloads support byte ranges and `HEAD`.

### Filesystem work limits

The server admits up to eight concurrent filesystem jobs across terminal listing, preview, search, checksum, completion, archive, plain directory listing and generated sitemap work. When all slots are busy, a new job receives HTTP 503 with `Retry-After: 1`. Direct file downloads and cached or manually served sitemaps do not consume a slot.

Each job can examine up to 4,096 directory entries, including hidden entries and nonmatches. Recursive commands can descend up to 128 directory levels. Resolved entry paths share a 1 MiB budget per job. The server reads a directory in bounded batches and sorts the entries it examined. A partial listing can omit entries anywhere in the full alphabetical order. Terminal output is limited to 256 KiB of text before JSON encoding and ends with `[results truncated by server limits]` when a listing or search stops early. The JSON response sets `truncated: true` in that case. Completion returns up to 200 items with the same flag for partial results. A plain directory listing displays a truncation notice when it reaches a limit.

`grep` reads up to 10 MiB per file and 32 MiB across a command. It skips lines longer than 64 KiB, marks the result as truncated, and resumes at the next line. `cat` uses the smaller of the configured cat limit and the terminal output limit. README previews retain their 256 KiB preview limit and use the directory entry budget when choosing the preview file.

An archive that exceeds the entry, depth or path budget is rejected before streaming with HTTP 422. A generated sitemap that exceeds those limits or 8 MiB of XML leaves its previous snapshot available. If there is no snapshot, the server returns HTTP 422. Streamed file and ZIP content have no byte cap. Walks and scans check for cancellation between entries and reads. Checksum and ZIP copies check between chunks.

### Application shell and published websites

The binary serves its embedded terminal shell for browsable directories. To edit the shell locally, run `./lsget -dir ./files -dev-template ./index.html` or use `LSGET_DIR=./files task dev` from this repository. Relative template paths resolve from the process working directory. lsget reads the selected file for each shell request. Give template access only to trusted developers since the override runs as application code and serves without the embedded shell’s Content Security Policy (CSP).

Terminal controls live in `assets/js/terminal.js` and rendering helpers in `assets/js/terminal-view.js`. Rebuild with Go when editing either module. The embedded shell uses `Cache-Control: no-store` and loads its scripts and bundled favicon from `/assets/lsget-shell/<version>/`. The version is a SHA-256 digest of the shell and its assets. These assets can be cached for a year. After an upgrade, reloading the page gets the new shell and asset paths. A missing asset from an older version returns HTTP 404 until the page is reloaded.

The browser dependency revisions, download sources, and license notices are documented in [assets/js/README.md](assets/js/README.md). Run `task vendor-check` to verify the checked-in files offline. The native CI and binary release jobs use the toolchain pinned in `go.mod`. When changing that toolchain, update `mise.toml` to match.

A visible `index.html` in a served directory takes precedence over the terminal, including when the development template is enabled. Published HTML and its assets keep their own serving behavior. Active content hosted on the same origin can interact with the terminal and its APIs using the visitor’s browser session. Serve sites from a separate origin when their authors are outside the terminal’s trust boundary.

The embedded terminal enforces a CSP. Each response supplies a nonce for its scripts and stylesheet, API requests stay on the same origin, and README images can use HTTP or HTTPS. Datastar compiles the shell’s `data-*` expressions with JavaScript’s `Function` constructor, so the enforced policy permits `'unsafe-eval'`. To trial a stricter policy on a development or staging listener, set `-shell-csp report-only`. This mode issues the proposed policy without `'unsafe-eval'` as a report-only header. Browser developer tools show the expected Datastar violations. Use the default `enforce` mode for deployments. The shell policy applies to the embedded terminal response. Published documents, files, and the generated `?nojs=1` directory listing keep their own response behavior.

### Configuration flags

Run `./lsget -h` for the flags in your binary. Flags take precedence over nonempty environment variables, which take precedence over the binary defaults. A standalone binary reads its process environment.

| Environment variable | Flag | Standalone default | Purpose |
| --- | --- | --- | --- |
| `LSGET_ADDR` | `-addr` | `localhost:8080` | Listener address in host:port form |
| `LSGET_DIR` | `-dir` | `.` | Served directory, relative to the working directory if needed |
| `LSGET_CATMAX` | `-catmax` | `4096` | Byte limit for `cat` and its completion suggestions |
| `LSGET_DEV_TEMPLATE` | `-dev-template` | Empty | Local application template for development |
| `LSGET_PID` | `-pid` | Empty | Optional application PID file |
| `LSGET_LOGFILE` | `-logfile` | Empty | Optional access log file. Request logs also go to stdout |
| `LSGET_SECURE_SESSION_COOKIE` | `-secure-session-cookie` | `false` | Require HTTPS for session cookies behind an HTTPS-only proxy |
| `LSGET_BASEURL` | `-baseurl` | Empty | Public URL for canonical tags, share links, origin checks and generated sitemaps |
| `LSGET_SITE_TITLE` | `-site-title` | `📂 lsget` | Browser and Open Graph title for the embedded terminal |
| `LSGET_SITE_DESCRIPTION` | `-site-description` | `Browse and download files with lsget.` | Description for the embedded terminal |
| `LSGET_PREVIEW_IMAGE` | `-preview-image` | Empty | Absolute HTTP(S) URL of the Open Graph preview image |
| `LSGET_SHELL_CSP` | `-shell-csp` | `enforce` | Embedded shell CSP mode |
| `LSGET_SITEMAP` | `-sitemap` | `0` | Sitemap refresh interval in minutes, with `0` for on-demand generation |
| `LSGET_TRUSTED_PROXIES` | `-trusted-proxies` | Empty | Comma-separated proxy CIDRs allowed to supply forwarding headers |

The native recipes listen on `127.0.0.1:8037` and serve `/srv/ftp`. Both work from `/var/lib/lsget` and run as the unprivileged `lsget` user. The default access log is `/var/log/lsget/access.log` for systemd and `/var/log/lsget/lsget.log` for OpenRC. The native managers track the service process, so the application PID file is optional. See the [native defaults and configuration paths](deploy/QUICKSTART.md#native-service-defaults) for both init systems and named instances.

Serve content with read and directory traversal permissions for the process user. Keep the content read-only for that user. An optional access log must be a writable regular file. lsget creates missing log directories when their parent permits it. An optional PID file needs an existing writable parent. Place both outside the served tree. lsget creates the PID file exclusively and removes it after a clean shutdown. If a crash leaves a PID file behind, establish that the old process has exited before removing it and restarting.

`LSGET_CATMAX` and `LSGET_SITEMAP` accept nonnegative whole numbers. Startup errors identify invalid settings and inaccessible paths on stderr. For a bind failure, check that `LSGET_ADDR` names an available local address and port. Stop an earlier lsget instance or choose another port. For service-specific logs, permissions, and restart commands, see [service troubleshooting](deploy/QUICKSTART.md#troubleshooting). SIGINT and SIGTERM stop background refreshes and new connections, then give active HTTP requests up to five seconds to finish.

#### Link previews

The embedded terminal includes a title, description, and local favicon in its initial HTML. Set `LSGET_BASEURL` to add a canonical URL and an `og:url` for each terminal directory. Use the public URL up to the served root, including any reverse proxy path prefix. Set `LSGET_PREVIEW_IMAGE` to a public HTTP(S) image URL without credentials to add `og:image`. Preview readers fetch the image directly from that URL.

```sh
LSGET_BASEURL='https://files.example.com/releases' \
LSGET_SITE_TITLE='Release files' \
LSGET_SITE_DESCRIPTION='Browse releases and download checksums.' \
LSGET_PREVIEW_IMAGE='https://files.example.com/releases/preview.png' \
./lsget -dir ./files
```

Published `index.html` pages and development templates control their own metadata.

#### Sitemap

Set `LSGET_BASEURL` to generate `/sitemap.xml` with canonical URLs. With the default interval of `0`, the first successful request generates a snapshot in memory and lsget uses it until restart. A positive `LSGET_SITEMAP` interval in minutes generates the initial snapshot at startup and replaces it after each successful refresh. Generation never writes XML into the served directory.

If a periodic refresh fails, lsget logs the error and continues to serve the last good snapshot. Check stderr or the service logs. Restore read access and keep the published tree within the filesystem work limits, then wait for the next scheduled refresh. A restart clears the snapshot. Before the first success, an over-budget sitemap returns HTTP 422. Concurrent work can return HTTP 503 and can be retried. If the tree cannot fit the generation limits, clear `LSGET_BASEURL`, restart, and publish a visible, readable `sitemap.xml` in the served root. lsget serves that manual file directly, and a missing or ignored file returns HTTP 404.

#### Proxy metadata and access logs

`LSGET_ADDR` controls where the process listens. Set `LSGET_BASEURL` to the absolute public HTTP or HTTPS URL, such as `https://files.example.com` or `https://example.com/files`, with no credentials, query or fragment. The URL supplies the public origin and optional path prefix for share links and generated sitemaps.

Set `LSGET_TRUSTED_PROXIES` only to the addresses or networks of proxies you control. Without trusted proxies, the log uses the socket peer address. A trusted peer can supply a single `X-Forwarded-For` list of up to 16 plain IPv4 or IPv6 addresses. lsget walks the list from the right through trusted proxies to the first untrusted address, or uses the leftmost address if every hop is trusted. Missing, repeated, malformed or oversized lists use the socket peer. Configure the edge proxy to remove client-supplied forwarding fields and each later trusted proxy to append its observed peer to the same list.

For share links without a configured base URL, the trusted peer may set one `X-Forwarded-Proto: https` value when forwarding over plain HTTP. Configure that peer to set the public scheme itself. `X-Forwarded-Host` and `Forwarded` are ignored. Forwarded metadata has no effect on browser-origin checks or session cookie settings.

Each request produces one combined-format record on stdout and in the configured access log. Recognized download, share and checksum events also produce one command record in the configured file with the command name, resolved virtual file or archive description, and HTTP status. Request records omit query strings. Logs include the selected IP, request path, bounded user agent and referrer origin. Cookie and Authorization headers, raw terminal input and referrer credentials, paths and queries are omitted. `outcome=canceled`, `outcome=aborted` or `outcome=write_error` identifies interrupted responses, with `000` when no final HTTP status was written. Archive failures retain the `archive=incomplete` marker. Rotate by renaming the log. The next request opens the original path again, and file write failures are reported to stderr at most once per minute.

### Docker Compose

The included `docker-compose.yaml` uses environment variables with sensible defaults. You can customize the configuration by creating a `.env` file:

```bash
# Create directories with proper permissions
mkdir -p files logs
chmod 755 files logs

# Copy the example environment file
cp .env.example .env

# Edit with your preferences
nano .env

# Start the service
docker-compose up -d
```

**Default configuration:**
- **Address**: `0.0.0.0:8080` (accessible from outside container)
- **Directory**: `/data` (mapped to `./files` on host)
- **Log file**: `/logs/access.log` (mapped to `./logs` on host)
- **Host port**: Set `ports` in your Compose configuration to publish the container listener on the host.

**Security & Permissions:**

The Docker image uses **Google's Distroless base** for maximum security:
- ✅ **10.9MB** image size (65% smaller than Alpine)
- ✅ No shell, no package manager
- ✅ Minimal attack surface
- ✅ Runs as non-root user (UID 65532)

**Permission Setup for Volumes:**

The container runs as UID 65532 (`nonroot` user). Give it read and directory traversal access to `/data` and write access to `/logs` for the configured access log.

```bash
# Create directories with proper permissions
mkdir -p files logs

# Option 1: World-writable (simplest, works everywhere)
chmod 777 logs
chmod 755 files  # Read-only is fine for serving files

# Option 2: Specific ownership (more secure)
sudo chown -R 65532:65532 files logs

# Option 3: Your user + group write (best for dev)
sudo chown -R $(id -u):$(id -g) files logs
chmod 775 logs  # Needs write for access.log
chmod 755 files
```

**For Coolify/Platform Deployments:**

If the access log is missing:
1. **Check volume permissions** - Logs directory must be writable by UID 65532
2. **Coolify**: Volumes are usually auto-mounted, but check persistent storage settings
3. **Manual fix**: SSH into the server and run `chmod 777 /path/to/mounted/logs`
4. **Verify**: Check server logs for "Logging to: /logs/access.log" message on startup

**Example 1: Simple setup (no baseurl needed):**

```bash
# Basic configuration - baseurl auto-detects
LSGET_CATMAX=262144
# Leave SERVICE_URL_LSGET empty to derive share links from the request.
```

**Example 2: Behind reverse proxy (baseurl needed):**

```bash
# Keep the container listener reachable by the proxy.
LSGET_ADDR=0.0.0.0:8080
SERVICE_URL_LSGET=https://files.example.com
LSGET_SITEMAP=60
```

**Example 3: Docker with custom domain:**

```bash
# Container binds to 0.0.0.0:8080
# But accessed via custom domain
SERVICE_URL_LSGET=https://cdn.mycompany.com
LSGET_LOGFILE=/logs/access.log
```

**Docker Compose commands:**

```bash
# Start in background
docker-compose up -d

# View logs
docker-compose logs -f

# Stop service
docker-compose down

# Rebuild and start
docker-compose up -d --build
```

**Testing Docker locally with Taskfile:**

```bash
# Build and test Docker image (quick version check)
task docker-test

# Build Docker image
task docker-build

# Run Docker container interactively
task docker-run

# Start with docker-compose
task docker-compose-up

# View docker-compose logs
task docker-compose-logs

# Stop docker-compose
task docker-compose-down

# Rebuild and restart docker-compose
task docker-compose-rebuild
```

### Vendored Dependencies

To enhance security and reduce supply chain attacks, JavaScript dependencies are vendored locally:

**Go dependencies.** The server uses only the Go standard library. The dependencies in `go.mod` support the Air development tool.

**JavaScript dependencies**: The following libraries are vendored locally and embedded in the binary:
- `marked.min.js` - Markdown rendering library
- `datastar.js` - Reactive UI framework

**Updating vendored dependencies**:

```bash
# Update JavaScript dependencies
task vendor
```

**Benefits**:
- ✅ Protection against supply chain attacks (compromised CDNs, malicious package updates)
- ✅ No runtime dependencies (self-contained binary)
- ✅ Reproducible builds (dependencies locked to specific versions)
- ✅ Offline execution (no internet connection required)
- ✅ Faster builds (no external downloads)

### Available Commands

Once you open lsget in your browser, you can use the following Unix-like commands in the TUI:
```
Available commands:
• help - print this message again
• pwd - print working directory
• ls [-l] [-h]|dir [-l] [-h] - list files (-h for human readable sizes)
• cd DIR - change directory
• cat FILE - view a text file
• sum|checksum FILE - print MD5 and SHA256 checksums
• get|wget|download FILE - download a file
• url|share FILE - get shareable URL (copies to clipboard)
• tree [-L<DEPTH>] [-a] - directory structure
• find [PATH] [-name PATTERN] [-type f|d] - search for files and directories
• grep [-r] [-i] [-n] PATTERN [FILE...] - search for text patterns in files
```
#### Navigation & File Listing

**`pwd`**
Print the current working directory.

**`cd [DIR]`**
Change directory. Use `..` for parent directory, or provide a path relative to current directory.

**`ls [-l] [-h]`** (alias: `dir`)
List files and directories in the current location.
- `-l` — Long format showing permissions, size, and modification time
- `-h` — Human-readable file sizes (KB, MB, GB)

**`tree [-L<N>] [-a] [PATH]`**
Display directory structure as a tree.
- `-L<N>` — Limit depth to N levels (e.g., `-L2` for 2 levels deep)
- `-a` — Show hidden files (files starting with `.`)

#### File Operations

**`cat FILE`**
Display contents of a text file. For images, displays the image inline in the browser.

**`get FILE|PATTERN`** (aliases: `rget`, `wget`, `download`)
Download a file or multiple files. Supports wildcards like `*.txt` or `*.pdf`. When downloading multiple files, they are automatically packaged as a zip archive.

**`url FILE`** (alias: `share`)
Generate a shareable URL for a file. The URL is automatically copied to your clipboard.

**`sum FILE`** (alias: `checksum`)
Calculate and display MD5 and SHA256 checksums for a file.

#### Search & Discovery

**`find [PATH] [-name PATTERN] [-type f|d]`**
Search for files and directories.
- `-name PATTERN` — Match by name pattern (e.g., `*.go`, `test*`)
- `-type f` — Find only files
- `-type d` — Find only directories

**`grep [-r] [-i] [-n] PATTERN [FILE...]`**
Search for text patterns in files.
- `-r` — Recursive search through directories
- `-i` — Case-insensitive search
- `-n` — Show line numbers in results

#### Help

**`help`**
Display the list of available commands.

#### Special Features

- **Path completion** — Use **Complete** for file and directory suggestions. Use the arrow keys to choose a suggestion, `Enter` to use it, or `Escape` to dismiss the list.
- **Command history** — Use `↑` and `↓` to recall previous commands while the suggestions are closed.
- **Session isolation** — Navigate independently in each browser tab.


**[🔝 back to top](#toc)**

***
## 🚑 Community & support

**[📝 Documentation](#toc)** - Getting started and more.

**[🚩 Issues](../../issues)** - Bugs end errors you encounter using lsget.

**[[] Matrix](https://socials.dyne.org/matrix)** - Hanging out with the community.

**[🗣️ Discord](https://socials.dyne.org/discord)** - Hanging out with the community.

**[🪁 Telegram](https://socials.dyne.org/telegram)** - Hanging out with the community.


**[🔝 back to top](#toc)**

***
## 😍 Acknowledgements

<a href="https://dyne.org">
  <img src="https://files.dyne.org/software_by_dyne.png" width="222">
</a>


Copyleft 🄯 2025 by [Dyne.org](https://www.dyne.org) foundation, Amsterdam

Designed, written and maintained by Puria Nafisi Azizi with contributions by Denis Jaromil Roio

**[🔝 back to top](#toc)**

***
## 👤 Contributing

The [contributor guide](CONTRIBUTING.md) covers Dyne.org’s Contributor Agreement, local verification, and opening a pull request. Report suspected vulnerabilities through the [private security channel](CONTRIBUTING.md#report-a-security-issue).


**[🔝 back to top](#toc)**

***
## 💼 License
    lsget - **Neon terminal file explorer in your browser. Read-only FTP, UNIX style interface**
    Copyleft 🄯2025 Dyne.org foundation, Amsterdam

    This program is free software: you can redistribute it and/or modify
    it under the terms of the GNU Affero General Public License as
    published by the Free Software Foundation, either version 3 of the
    License, or (at your option) any later version.

    This program is distributed in the hope that it will be useful,
    but WITHOUT ANY WARRANTY; without even the implied warranty of
    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
    GNU Affero General Public License for more details.

    You should have received a copy of the GNU Affero General Public License
    along with this program.  If not, see <http://www.gnu.org/licenses/>.

**[🔝 back to top](#toc)**
