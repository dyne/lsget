# Browser Shell (lsget)

> **Tiny Go-powered web server with a full‑screen, neon‑themed browser terminal.**

Serve any local directory as a mini *cloud* and poke around with familiar `pwd`, `ls`, `cd`, `cat`, and `download` commands — right from your browser. Hot‑reload your server with [Air](https://github.com/cosmtrek/air) via `task dev` for an instant Go hacking loop.

---

## Features

| Capability           | Details |
|----------------------|---------|
| Web‑based terminal   | Reactive UI powered by **[DataStar](https://github.com/starfederation/datastar)**; smooth key handling & history. |
| File ops             | `pwd`, `ls [-l]`, `cd DIR`, `cat FILE`, `download FILE`. |
| Smart autocompletion | Tab‑completes dirs/files (text‑only, size‑limited for `cat`). |
| Colourised `ls`      | Directories in bright blue (Ubuntu style) with trailing `/`. |
| Session isolation    | Per‑browser *in‑memory* CWD tracked via cookie. |
| Live reload          | `task dev` ⇒ [Air](https://github.com/cosmtrek/air) rebuilds `main.go` on save. |
| Zero‑config binary   | `go run .` or `go build` produces a single executable. |

---

## Quick start

```bash
# Clone & enter
git clone https://github.com/dyne/lsget.git
cd lsget

# Install Taskfile runner (once)
brew install go-task/tap/go-task  # macOS
# apt install task               # Debian/Ubuntu (snap)

# Dev mode with hot reload
task dev        # ⇢ http://localhost:8080

# Or run the server directly
GOFLAGS="-trimpath" go run . -addr :8080 -dir .
```

Open your browser at `http://localhost:8080` and enjoy the neon green shell.

---

## Configuration flags

| Flag        | Default            | Description |
|-------------|--------------------|-------------|
| `-addr`     | `localhost:8080`   | HTTP listen address. |
| `-dir`      | `.` (cwd)          | Directory to expose as `/`. |
| `-catmax`   | `262144` (256 KiB) | Max bytes printable via `cat` & completion filter. |

---

## Project structure

```text
main.go        # Go HTTP server / API
index.html     # DataStar UI (embedded + served from disk if present)
Taskfile.yml   # Taskfile targets (install‑air, dev)
```

---

## Contributing

1. Fork / branch from `main`.
2. `git commit -s` your changes.
3. `gh pr create -f` to open a PR.

We ♥️ issues and creative colour schemes — PRs welcome!

---

## License

MIT © 2025 Your Name

