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
Single binary, no need to install anything!

```bash
# Download
curl -fsSL  "https://github.com/dyne/lsget/releases/latest/download/lsget-$(uname -s)-$(uname -m)" -o lsget && chmod +x lsget
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
  -dir string
        directory to expose as root (default ".")
  -logfile string
        path to log file for statistics
  -pid string
        path to PID file
  -sitemap int
        generate sitemap.xml every N minutes (0 = disabled)
  -version
        Print the version of this software and exits
```

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

| Command | Aliases | Usage | Description |
|---------|---------|-------|-------------|
| `pwd` | - | `pwd` | Print the current working directory |
| `cd` | - | `cd [DIR]` | Change directory. Use `..` for parent, or path relative to current dir |
| `ls` | `dir` | `ls [-l] [-h]` | List files and directories<br>`-l` Long format with permissions and size<br>`-h` Human-readable file sizes (KB, MB, GB) |
| `tree` | - | `tree [-L<N>] [-a] [PATH]` | Display directory structure as a tree<br>`-L<N>` Limit depth to N levels (e.g., `-L2`)<br>`-a` Show hidden files (starting with `.`) |

#### File Operations

| Command | Aliases | Usage | Description |
|---------|---------|-------|-------------|
| `cat` | - | `cat FILE` | Display contents of a text file<br>For images: displays the image inline |
| `get` | `rget`, `wget`, `download` | `get FILE\|PATTERN` | Download a file or multiple files<br>Supports wildcards (e.g., `*.txt`)<br>Multiple files are zipped automatically |
| `url` | `share` | `url FILE` | Generate a shareable URL for a file<br>Copies the URL to your clipboard |
| `sum` | `checksum` | `sum FILE` | Calculate and display MD5 and SHA256 checksums |

#### Search & Discovery

| Command | Aliases | Usage | Description |
|---------|---------|-------|-------------|
| `find` | - | `find [PATH] [-name PATTERN] [-type f\|d]` | Search for files and directories<br>`-name` Match by name pattern (e.g., `*.go`)<br>`-type f` Find only files<br>`-type d` Find only directories |
| `grep` | - | `grep [-r] [-i] [-n] PATTERN [FILE...]` | Search for text patterns in files<br>`-r` Recursive search in directories<br>`-i` Case-insensitive search<br>`-n` Show line numbers |

#### Statistics & Help

| Command | Aliases | Usage | Description |
|---------|---------|-------|-------------|
| `stats` | - | `stats` | Display access statistics (requires `-logfile` flag)<br>Shows share counts, downloads, and checksums per file |
| `help` | - | `help` | Display the list of available commands |

#### Special Features

- **Tab completion**: Press `Tab` to autocomplete file and directory names
- **Command history**: Use `↑` and `↓` arrow keys to navigate through previous commands
- **Session isolation**: Each browser maintains its own current working directory via cookies


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
