# tpod

Go CLI for disposable, reproducible dev environments in a Podman/Docker container with a persistent [mise](https://mise.jdx.dev/) toolchain. `tpod <profile>` mounts your cwd, runs the profile's command, and removes the container on exit; shared volumes keep mise installs and package caches warm across runs.

## Build & test
- `make install` — `go install ./cmd/tpod`
- `go test ./...` — full test suite (Go 1.25, CGO off in releases)
- `go vet ./...` — lint check

CLI is wired with [kong](https://github.com/alecthomas/kong); commands live in `cmd/tpod/cli.go`. `LaunchCmd.ProfileAndArgs` uses `passthrough:"partial"` so flags after the profile name reach the profile's command verbatim.

## Layout
- `cmd/tpod/` — entrypoint and CLI (`main.go`, `cli.go`, e2e/profile/cli tests).
- `pkg/tpod/` — public launch/spec/types API used by the CLI and tests.
- `internal/profile/` — YAML schema, `extends` deep-merge, validation, fragment rules.
- `internal/catalog/` — embedded built-in profiles/fragments (YAML). Add new agents/tools here, not at runtime.
- `internal/runtime/` — Docker-API client (docker.go, run/exec/prepare), attach, test fake; `docker_build.go` synthesizes derived images for `packages:`.
- `internal/mise/` — mise install dir volume + `appimage:` backend plugin (`plugins/appimage/*.lua`).
- `internal/{doctor,prune,scaffold,ui,workspace}/` — diagnostics, cleanup, `init` wizard, TUI, rootless-vs-rootful detection.
- `docs/` — design notes.
- No base-image Dockerfile: profiles use `debian:13-slim` directly; derived images install everything via `packages:`/`repos:`.

## Conventions
- **Profiles vs fragments:** profiles carry `image`/`command` identity; fragments are composable mounts/caches/credentials and may only `extends` other fragments. User YAML in `~/.config/tpod/{profiles,fragments}/` shadows built-ins; names are globally unique (a name clash is a hard catalog-load error).
- **Merge** (`internal/profile/merge.go`): scalars—child wins; maps—key-by-key (`null` deletes an inherited key); `command`—replaced; `packages`—append+dedup (`null` clears); `repos`—key-by-key; `image`/`build`—single slot.
- **System deps (`packages:`/`repos:`):** `docker_build.go` derives a content-addressed image `tpod/packages:<hash>` from `(base image ID, sorted packages, sorted repos)`, built/reused in `Prepare`; `prune` removes catalog-unused derived images. `repos:` v1 is `extrepo: <name>` only (custom URLs are schema-ready but `checkExtrepoOnly` rejects them). The base image ships no `extrepo`; `extrepo.go` reimplements `extrepo enable` in Go at build time — reads Debian codename from the base image, fetches the extrepo catalog, and COPYs deb822 `.sources` + sha256-verified signing keys into the derived image. The base image has no ca-certificates, so the Dockerfile bootstraps `ca-certificates` (Debian archive is http) before the repo COPYs; the derived-tag hash stays name-based, so `prune` needs no network.
- **Shared caches:** mise installs into the shared `tpod-cache-mise` volume (the `mise` profile's cache); the npm backend store lives in the parallel `aube` cache volume (`~/.aube`). Compilation runs inside a per-profile derived image, so a tool built under a profile declaring a runtime lib (e.g. `php`'s `libxml2`) may fail to load under another profile — profiles running the same tool should declare the same runtime libs.
- **`files:`:** inline-content files are written between ContainerCreate and ContainerStart via CopyToContainer (tar built by `tarFiles` in `docker_run.go`). `{{ }}` templates supported; targets absolute or `~`-prefixed, never `..`. Owned by the execution user; missing parents auto-created and bootstrap chown covers `$HOME` parents.
- **Prune/doctor image matching:** engines qualify `RepoTags` with a registry (`docker.io/`, `localhost/`, …), so `listTpodImages`/`checkDerivedImages` normalize via `runtime.DerivedRef` (`github.com/distribution/reference`, matching the `tpod/packages` path) — never a bare string `HasPrefix`.
- **Templates:** `{{ }}` in `mounts`, `environment`, `command` resolve `.Env` (host env), `uid`, and `.Ports` (container→host ports). Empty resolution leaves the var unset.
- **Catalog is embedded:** built-in profiles/fragments ship in the binary; add YAML under `internal/catalog/` and re-build, never load from disk at runtime.
- **No comments** unless the code doesn't make something apparent.

## Runtime notes
- Primary target is rootless Podman on Linux (workspace mounted at host absolute path, runs as host user). Docker/rootful Podman work but mount at `/workspace` as root.
- `tpod doctor` reports active mode, volumes, mise, and config.

## Workspace rules
You are in an isolated environment. Trust user information if you cannot verify. Create worktrees in `.worktrees`. All directories outside of the project are ephemeral.

## Comments
Comments should explain intent, not implementation — business rules, design rationale, edge cases, assumptions, trade-offs, and public API contracts. Skip comments that restate code, are stale, or leave commented-out code; prefer clear names and simple code. When a comment is needed, explain why the code exists or is shaped that way.
