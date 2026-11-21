# Native checks and binary releases

Pull requests to `main` run lint, offline browser bundle verification, Go tests, the race detector, and a Go vulnerability scan with a read-only token. A push to `main` can publish binaries after the native tests succeed. Semantic Release creates the version tag and release notes. The binary publish job fetches that tag and checks that it resolves to the tested push commit. GoReleaser adds the raw executables, browser license notices, and `lsget_v<VERSION>_checksums.txt` to the release. The publish job attests the subjects in that checksum file.

## Binary names

Each download is a single executable. The two browser license notices are separate release assets named `marked.LICENSE.md` and `datastar.LICENSE.md`. The release checksum file includes both notices and every executable.

| Go target | Download |
|---|---|
| `darwin/amd64` | `lsget-Darwin-x86_64` |
| `darwin/arm64` | `lsget-Darwin-arm64` |
| `linux/386` | `lsget-Linux-386` |
| `linux/amd64` | `lsget-Linux-x86_64` |
| `linux/arm` | `lsget-Linux-arm` |
| `linux/arm64` | `lsget-Linux-aarch64` |
| `linux/ppc64` | `lsget-Linux-ppc64` |
| `windows/386` | `lsget-Windows-386.exe` |
| `windows/amd64` | `lsget-Windows-amd64.exe` |
| `windows/arm64` | `lsget-Windows-arm64.exe` |

Go 1.24 marked the 32-bit `windows/arm` port broken. Go 1.24.5 omits it from `go tool dist list`, so it is outside this release matrix. Historical releases retain `lsget-Windows-arm.exe` where it was published.

## Refreshing release inputs

1. Resolve the intended GitHub Action release to its full commit SHA in the upstream repository. For an annotated tag, use its peeled commit. Review the upstream release notes, `action.yml`, and the code that handles downloaded tools, inputs, credentials, and permissions. Update the SHA and version comment in `workflows/release.yaml` together.
2. When changing Go, update `go.mod` and `mise.toml` together. Run `go tool dist list` with that version and verify the target list in `.goreleaser.yaml`. Preserve the download names for supported targets.
3. For Semantic Release or GoReleaser, verify the selected Linux amd64 download against its upstream release checksum file. Update the download URL and checked SHA-256 together. Review any Semantic Release plugin updates at their upstream GitHub releases and change the exact versions in the workflow. Keep the linter version matched to `go.mod`.
4. Run native checks, then run `goreleaser check` and `goreleaser release --snapshot --clean --skip=publish` in a disposable clean checkout with the pinned GoReleaser and Go versions. Compare `dist/artifacts.json` and every checksum against the generated files. Run `-version` on a binary built for the current platform. Confirm that pull requests still use read-only permissions and that tag creation, binary publication, and attestation all require a push to `main`.

## Vulnerability scan triage

CI installs a fixed `govulncheck` version and scans application source using the same Go toolchain as the release. The advisory database is queried at run time. The verbose job log includes advisory IDs, affected versions, and call traces. Reproduce locally from the repository root with the version specified in `workflows/release.yaml`.

For a reported advisory, record its `GO-` ID, CI run, affected version, fixed version, and whether the call path and platform are relevant to the application. Review the linked Go advisory. Fix standard-library findings by updating Go, and fix dependency findings by taking a narrowly scoped dependency update. Rerun the scan, suite, and release snapshot after the fix. Reserve the project’s private reporting channel for application-specific findings that need coordinated disclosure.

The scan is a separate CI check and fails when it finds reachable advisories or cannot complete. Tool, network, and database errors call for a rerun after correcting the cause. The Go 1.24.5 baseline already reports standard-library findings. Track the toolchain update and use the scan results when deciding whether to ship.
