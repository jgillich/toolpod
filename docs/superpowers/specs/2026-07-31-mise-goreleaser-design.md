# mise install via `github:` backend + GoReleaser

## Goal

Let users install toolpod with one command — `mise use -g github:jgillich/toolpod` — by publishing pre-built binaries to GitHub Releases and relying on mise's `github` backend asset autodetection. No mise registry PR (deferred). Also add a CI testing workflow.

## Background

toolpod is currently installed via `make install` (`go install ./cmd/toolpod`), which requires Go 1.25+ on the host. The mise `github` backend downloads release assets from a GitHub repo, autodetects the right archive for the current OS/arch, extracts it, and puts the binary on PATH. No plugin scripts, no asdf shim. mise also verifies checksums against `checksums.txt` when present and checks GitHub Artifact Attestations automatically if available.

Asset autodetection scores assets on OS/arch substring matches. Archive names of the form `toolpod_<version>_linux_<arch>.tar.gz` score highest for linux/amd64 and linux/arm64. mise strips OS/arch suffixes from the extracted binary automatically, so the binary lands as `toolpod` in the install dir.

## Architecture

### GoReleaser (`.goreleaser.yml`)

Single Go build, no CGO (the Docker SDK uses the HTTP API). Targets:
- `linux/amd64`
- `linux/arm64`

Each binary packed into a `.tar.gz` archive named `toolpod_<version>_linux_<arch>.tar.gz` (GoReleaser default archive name_template with `linux`/`amd64`/`arm64` tokens). A `checksums.txt` file (sha256) is generated and uploaded alongside the archives.

GoReleaser config fields:
- `project_name: toolpod`
- `builds`: one build entry, `main: ./cmd/toolpod`, `binary: toolpod`, `env: [CGO_ENABLED=0]`, `goos: [linux]`, `goarch: [amd64, arm64]`.
- `archives`: `id: default`, `name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"`, `format: tar.gz`. No `strip_components` needed — the archive contains the single `toolpod` binary at root.
- `checksum`: `name_template: checksums.txt`, `algorithm: sha256`.
- `changelog`: default (commit-based).
- `release`: GitHub, `github.owner: jgillich`, `github.name: toolpod`.

No brew/scoop/snap/docker/fpm — minimal.

### Release workflow (`.github/workflows/release.yml`)

- Trigger: push of tags matching `v*`.
- Runner: `ubuntu-latest`.
- Permissions: `contents: write` (for creating the GitHub Release).
- Steps: checkout (fetch-depth 0 for changelog), setup Go 1.25, `go run github.com/goreleaser/goreleaser@latest release --clean` (or install goreleaser binary). Uses default `GITHUB_TOKEN`.

### CI testing workflow (`.github/workflows/ci.yml`)

- Trigger: push to `main`, pull requests.
- Runner: `ubuntu-latest`.
- Permissions: `contents: read`.
- Job `test`:
  - `actions/checkout@v4`
  - `actions/setup-go@v5` (reads version from `go.mod`)
  - `actions/cache` for `~/.cache/go-build` and `~/go/pkg/mod`, keyed on `go.sum`
  - `go build ./...`
  - `go vet ./...`
  - `go test -race ./...` — runs unit tests with race detector; e2e tests in `cmd/toolpod/` run here too (Docker is available on the runner; they self-skip otherwise via `dockerAvailable()` and `testing.Short()`).

No separate e2e job — the tests are in the same package and self-gate on Docker availability.

### README update

Add to the Install section, above the `make install` block:

```
mise use -g github:jgillich/toolpod
```

Keep the existing `make install` block. Add a note that the Go 1.25+ requirement applies only to `make install`.

## Version model

Tags: `v0.1.0`, `v0.2.0`, etc. GoReleaser strips the `v` prefix for `{{ .Version }}`, so archive names become `toolpod_0.1.0_linux_amd64.tar.gz`. mise handles the `v` prefix natively — users write `mise use github:jgillich/toolpod@0.1.0` or `@latest`.

## Out of scope

- mise registry PR (deferred to a later task).
- macOS / Windows targets.
- Homebrew / scoop / AUR.
- SLSA / cosign signing (mise verifies checksums.txt; GitHub Artifact Attestations are checked by mise if GitHub provides them).
- golangci-lint (not selected by user).