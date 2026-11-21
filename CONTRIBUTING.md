# Contributing to lsget

Read the [Dyne.org Contributor Agreement](https://github.com/dyne/.github/blob/main/CONTRIBUTING.md) before submitting a contribution. Use the contact in the agreement for questions about its terms.

Fork [dyne/lsget](https://github.com/dyne/lsget), create a feature branch, and open a pull request against `main`. Describe the behavior that changed and include the checks you ran. Use [GitHub Issues](https://github.com/dyne/lsget/issues) for ordinary bugs and feature requests. For suspected vulnerabilities, follow the [private reporting instructions](#report-a-security-issue).

## Build and check a checkout

From the repository root, use the toolchain in [go.mod](go.mod), which CI uses and [mise.toml](mise.toml) mirrors. Run `mise install` or install the Go and golangci-lint versions listed in `mise.toml` on your PATH. Use `gofmt` on any edited Go files.

```sh
go version
go build -o lsget .
./lsget -version
go test -count=1 ./...
go vet ./...
golangci-lint run ./...
sh scripts/vendor-js.sh check
```

CI also runs the race detector. Run the same command on a supported platform with cgo and a C compiler.

```sh
go test -race -count=1 ./...
```

Use the [local quick start](README.md#-quick-start) to run the binary against a dedicated fixture. Use a directory without its own `index.html` when testing the terminal shell. For browser changes, check the interactive terminal and the `?nojs=1` directory view. The [browser test harness](tests/browser/rendering.test.mjs) contains the browser setup and invocation. The [vendor notes](assets/js/README.md) list the checked-in JavaScript revisions and their update procedure.

## Project layout

| Area | Files |
| --- | --- |
| Startup, configuration, and shutdown | [startup.go](startup.go) and [pidfile.go](pidfile.go) |
| HTTP routing, origin checks, proxy trust, and access logs | [routes.go](routes.go), [http_policy.go](http_policy.go), [proxy.go](proxy.go), and [access_log.go](access_log.go) |
| Sessions, visible paths, published pages, and sitemap generation | [main.go](main.go) and [work_budget.go](work_budget.go) |
| Terminal commands and download handling | [commands.go](commands.go), [commands_navigation.go](commands_navigation.go), [commands_files.go](commands_files.go), [commands_search.go](commands_search.go), and [download.go](download.go) |
| Embedded shell and browser behavior | [shell_assets.go](shell_assets.go), [index.html](index.html), [terminal.js](assets/js/terminal.js), and [terminal-view.js](assets/js/terminal-view.js) |
| Handler and file visibility fixtures | [handlers_test.go](handlers_test.go), [visibility_test.go](visibility_test.go), and [artifact_transfers_test.go](artifact_transfers_test.go) |

Keep HTTP access to served content read-only. Keep browser paths slash-delimited and scoped to the served root. For file-access changes, check containment and `.lsgetignore` in each affected browsing, search, serving, or download route. For terminal changes, check the browser's working directory and API response fields. When command syntax or configuration changes, update the in-app help, [README](README.md), and affected [native deployment examples](deploy/QUICKSTART.md).

## Release workflow

Pull requests targeting `main` run [the native CI workflow](.github/workflows/release.yaml). Releases are published from tested pushes to `main` when the release workflow creates a version tag. See the [release guide](.github/RELEASING.md) for binary names, checksums and attestations, snapshot verification, and vulnerability scan triage.

## Report a security issue

On the [lsget Security page](https://github.com/dyne/lsget/security), choose **Report a vulnerability** to send a private report to the maintainers. Include the affected version, what you observed, and the potential impact. Keep the details out of public issues and pull requests.

If you cannot use the GitHub form, follow the [Dyne.org Foundation security policy](https://dyne.org/security/) and use its security contact. Identify `dyne/lsget` in your report so the Foundation can route it.
