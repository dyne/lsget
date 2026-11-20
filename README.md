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
| Smart autocompletion | Tab‑completes directories and files (text‑only, size‑limited for `cat`). |
| Colourised output    | Directories in bright blue (Ubuntu style) with trailing `/`, executable files highlighted. |
| Session isolation    | Per‑browser *in‑memory* CWD tracked via cookie — multi-user ready. |
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

To start using lsget run the following commands

```bash
# Download and run
./lsget
```
Open your browser at `http://localhost:8080` and enjoy the neon green shell.

### Direct file links

A file in the served directory can be shared by its path, such as `/eudi/report.pdf`, or with the `file` query parameter, such as `/?file=eudi/report.pdf`. Both links resolve from the served root and use the same content type and download headers. URL-encode special characters in the query value, for example `/?file=eudi/meeting%20notes.pdf`. The `file` parameter returns 404 for missing or ignored files and for directories.

Direct file links and terminal downloads respect `.lsgetignore` rules on files and their parent directories. ZIP downloads omit ignored entries. Downloads can follow symlinks that resolve within the served root.

### Terminal sessions

Each terminal session stores its working directory in memory and uses an `sid` cookie marked `HttpOnly` and `SameSite=Lax`. Sessions expire after 30 minutes without a request that uses the working directory. The server retains up to 4,096 sessions and evicts the longest-idle session when it needs room. In-flight requests keep their sessions. If all slots are busy, requests that need a new session receive HTTP 503 and can retry. A returning browser with an expired or evicted session starts at the served root unless it opens a specific directory.

Direct downloads and archive requests that specify their directory bypass terminal sessions. lsget sets `Secure` on session cookies for TLS requests. When an HTTPS-only reverse proxy terminates TLS, set `LSGET_SECURE_SESSION_COOKIE=true` or pass `-secure-session-cookie` and keep the lsget listener reachable only by the proxy. Forwarding headers do not control the cookie's `Secure` attribute.

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

The binary serves its embedded terminal shell for browsable directories. To edit the shell locally, run `lsget -dir ./files -dev-template ./index.html` or use `LSGET_DIR=./files task dev` from this repository. Relative template paths resolve from the process working directory. lsget reads the selected file for each shell request. Give template access only to trusted developers since the override runs as application code and serves without the embedded shell’s Content Security Policy (CSP).

A visible `index.html` in a served directory takes precedence over the terminal, including when the development template is enabled. Published HTML and its assets keep their own serving behavior. Active content hosted on the same origin can interact with the terminal and its APIs using the visitor’s browser session. Serve sites from a separate origin when their authors are outside the terminal’s trust boundary.

The embedded terminal enforces a CSP. Each response supplies a nonce for its scripts and stylesheet, API requests stay on the same origin, and README images can use HTTP or HTTPS. Datastar compiles the shell’s `data-*` expressions with JavaScript’s `Function` constructor, so the enforced policy permits `'unsafe-eval'`. To trial a stricter policy on a development or staging listener, set `-shell-csp report-only`. This mode issues the proposed policy without `'unsafe-eval'` as a report-only header. Browser developer tools show the expected Datastar violations. Use the default `enforce` mode for deployments. The shell policy applies to the embedded terminal response. Published documents, files, and the generated `?nojs=1` directory listing keep their own response behavior.

### Configuration flags
```
./lsget -h
Usage of ./lsget:
  -addr string
        address to listen on (default "localhost:8080")
  -baseurl string
        base URL for the site (e.g., https://files.example.com)
  -catmax cat
        max bytes printable via cat and used by completion (default 4096)
  -dev-template string
        use a local application template for development
  -dir string
        directory to expose as root (default ".")
  -logfile string
        path to log file for statistics
  -pid string
        path to PID file
  -secure-session-cookie
        require HTTPS for session cookies behind an HTTPS-only proxy
  -shell-csp string
        application CSP mode: enforce or report-only (default "enforce")
  -sitemap int
        refresh the in-memory sitemap every N minutes (0 = on demand only)
  -trusted-proxies string
        comma-separated CIDRs allowed to supply forwarded metadata
  -version
        Print the version of this software and exits
```

### Environment Variables

All configuration flags can also be set via environment variables with the `LSGET_` prefix. Environment variables are used as defaults and can be overridden by command-line flags.

| Environment Variable | Flag Equivalent | Description | Example |
|---------------------|-----------------|-------------|---------|
| `LSGET_ADDR` | `-addr` | Address to listen on (bind address) | `LSGET_ADDR=0.0.0.0:8080` |
| `LSGET_DIR` | `-dir` | Directory to expose as root | `LSGET_DIR=/var/www/files` |
| `LSGET_CATMAX` | `-catmax` | Max bytes for cat command | `LSGET_CATMAX=8192` |
| `LSGET_DEV_TEMPLATE` | `-dev-template` | Local development template | `LSGET_DEV_TEMPLATE=./index.html` |
| `LSGET_PID` | `-pid` | Path to PID file | `LSGET_PID=/var/run/lsget.pid` |
| `LSGET_LOGFILE` | `-logfile` | Path to log file for statistics | `LSGET_LOGFILE=/var/log/lsget.log` |
| `LSGET_SECURE_SESSION_COOKIE` | `-secure-session-cookie` | Require HTTPS for session cookies behind an HTTPS-only proxy | `LSGET_SECURE_SESSION_COOKIE=true` |
| `LSGET_BASEURL` | `-baseurl` | **Optional:** Public URL (see below) | `LSGET_BASEURL=https://files.example.com` |
| `LSGET_SHELL_CSP` | `-shell-csp` | Embedded application CSP mode | `LSGET_SHELL_CSP=enforce` |
| `LSGET_SITEMAP` | `-sitemap` | In-memory sitemap refresh interval (minutes) | `LSGET_SITEMAP=60` |
| `LSGET_TRUSTED_PROXIES` | `-trusted-proxies` | CIDRs for trusted HTTP proxies | `LSGET_TRUSTED_PROXIES=10.0.0.4/32,2001:db8::4/128` |

lsget validates configuration before accepting requests. Set `LSGET_CATMAX` and `LSGET_SITEMAP` to nonnegative whole numbers. An access log must be a writable regular file. lsget creates missing log directories when their parent allows it.

Keep the PID file and access log in writable state directories when serving read-only content. lsget creates the PID file exclusively and removes it after shutdown. If a crash leaves a stale PID file, verify that the previous service has exited before removing it and restarting. SIGINT and SIGTERM stop background refreshes and new connections, then give active HTTP requests up to five seconds to finish before closing their connections.

#### Sitemap

Set `LSGET_BASEURL` to generate `/sitemap.xml` with canonical URLs. The server keeps the generated XML in memory, so the served directory can be read-only. A positive `LSGET_SITEMAP` interval generates the sitemap at startup and refreshes it periodically. With the default interval of `0`, the first request generates the sitemap and the server caches it for its lifetime.

Without `LSGET_BASEURL`, you can serve a manually maintained `sitemap.xml` from the served directory.

#### About LSGET_ADDR vs LSGET_BASEURL

These serve different purposes and are kept separate for flexibility:

**`LSGET_ADDR`** (required) - Where the server **binds/listens**
- Network-layer configuration
- Examples: `0.0.0.0:8080`, `localhost:8080`, `127.0.0.1:3000`

**`LSGET_BASEURL`** (optional) - What URL users **access publicly**
- Application-layer configuration
- Full URL with protocol, no trailing slash
- Examples: `https://files.example.com`, `https://cdn.company.com`

**When you DON'T need `LSGET_BASEURL`** (auto-detection works):
- ✅ Running locally for development
- ✅ Direct access to the server without reverse proxy
- ✅ Using localhost or direct IP access
- Share links use the HTTP Host and the connection's TLS scheme

**When you DO need `LSGET_BASEURL`**:
- 🔧 Behind reverse proxy (Nginx, Caddy, Traefik) with different public URL
- 🔧 Docker with port mapping to a different domain
- 🔧 Generating sitemap.xml with specific canonical URLs
- 🔧 Using `url`/`share` commands for consistent shareable links

#### Proxy metadata and access logs

Set `LSGET_BASEURL` to use the public origin and optional path prefix for share links and generated sitemaps. Set `LSGET_TRUSTED_PROXIES` only to the addresses or networks of proxies you control. Without trusted proxies, the log uses the socket peer address. A trusted peer can supply a single `X-Forwarded-For` list of up to 16 plain IPv4 or IPv6 addresses. lsget walks the list from the right through trusted proxies to the first untrusted address, or uses the leftmost address if every hop is trusted. Missing, repeated, malformed or oversized lists use the socket peer. Configure the edge proxy to remove client-supplied forwarding fields and each later trusted proxy to append its observed peer to the same list.

For share links without a configured base URL, the trusted peer may set one `X-Forwarded-Proto: https` value when forwarding over plain HTTP. Configure that peer to set the public scheme itself. `X-Forwarded-Host` and `Forwarded` are ignored. Forwarded metadata has no effect on browser-origin checks or session cookie settings.

Each request produces one combined-format record on stdout and in the configured access log. Recognized download, share and checksum events also produce one command record in the configured file with the command name, resolved virtual file or archive description, and HTTP status. Request records omit query strings. Logs include the selected IP, request path, bounded user agent and referrer origin. Cookie and Authorization headers, raw terminal input and referrer credentials, paths and queries are omitted. `outcome=canceled`, `outcome=aborted` or `outcome=write_error` identifies interrupted responses, with `000` when no final HTTP status was written. Archive failures retain the `archive=incomplete` marker. Rotate by renaming the log. The next request opens the original path again, and file write failures are reported to stderr at most once per minute.

**Example usage with environment variables:**

```bash
# Set environment variables
export LSGET_ADDR="0.0.0.0:8080"
export LSGET_DIR="/var/www/files"
export LSGET_LOGFILE="/var/log/lsget.log"

# Run lsget (uses env vars)
./lsget

# Or use with Docker
docker run -e LSGET_ADDR=0.0.0.0:8080 -e LSGET_DIR=/data lsget
```

**Priority order:** Command-line flags > Environment variables > Default values

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
- **Port mapping**: `8080:8080` (customizable via `LSGET_PORT` env var)

**Security & Permissions:**

The Docker image uses **Google's Distroless base** for maximum security:
- ✅ **10.9MB** image size (65% smaller than Alpine)
- ✅ No shell, no package manager
- ✅ Minimal attack surface
- ✅ Runs as non-root user (UID 65532)

**Permission Setup for Volumes:**

⚠️ **IMPORTANT**: Since the container runs as UID 65532 (`nonroot` user), mounted volumes **must be writable** for:
- `/data` - Serving files (read access sufficient)
- `/logs` - **Writing access logs** (required for `stats` command to work)

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

⚠️ If `stats` command shows "no activity logged yet":
1. **Check volume permissions** - Logs directory must be writable by UID 65532
2. **Coolify**: Volumes are usually auto-mounted, but check persistent storage settings
3. **Manual fix**: SSH into the server and run `chmod 777 /path/to/mounted/logs`
4. **Verify**: Check server logs for "Logging to: /logs/access.log" message on startup

**Example 1: Simple setup (no baseurl needed):**

```bash
# Basic configuration - baseurl auto-detects
LSGET_PORT=8080
LSGET_CATMAX=262144
# LSGET_BASEURL is empty - auto-detection works fine!
```

**Example 2: Behind reverse proxy (baseurl needed):**

```bash
# When lsget runs on http://localhost:8080
# But users access via https://files.example.com
LSGET_ADDR=127.0.0.1:8080
LSGET_BASEURL=https://files.example.com
LSGET_SITEMAP=60
```

**Example 3: Docker with custom domain:**

```bash
# Container binds to 0.0.0.0:8080
# But accessed via custom domain
LSGET_PORT=8080
LSGET_BASEURL=https://cdn.mycompany.com
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

**Go dependencies**: lsget has **zero direct Go dependencies** - it uses only the Go standard library. All dependencies in `go.mod` are indirect and only for development tools (air, golangci-lint).

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

#### Statistics & Help

**`stats`**
Display access statistics showing file shares, downloads, and checksum operations. Requires the `-logfile` flag to be set when starting lsget.

**`help`**
Display the list of available commands.

#### Special Features

- **Tab completion** — Press `Tab` to autocomplete file and directory names
- **Command history** — Use `↑` and `↓` arrow keys to navigate through previous commands
- **Session isolation** — Each browser maintains its own current working directory via cookies


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

Please first take a look at the [Dyne.org - Contributor License Agreement](CONTRIBUTING.md) then

1.  🔀 [FORK IT](../../fork)
2.  Create your feature branch `git checkout -b feature/branch`
3.  Commit your changes `git commit -am 'feat: New feature\ncloses #398'`
4.  Push to the branch `git push origin feature/branch`
5.  Create a new Pull Request `gh pr create -f`
6.  🙏 Thank you


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
