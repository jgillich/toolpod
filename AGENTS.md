# tpod

Go CLI for disposable, reproducible dev environments in a Podman/Docker container with a persistent [mise](https://mise.jdx.dev/) toolchain. `tpod <profile>` mounts your cwd, runs the profile's command, and removes the container on exit; shared volumes keep mise installs and package caches warm across runs.

## Build & test
- `make install` — `go install ./cmd/tpod`
- `make image` — build the mise base image (`ghcr.io/jgillich/tpod-mise`)
- `go test ./...` — full test suite (Go 1.25, CGO off in releases)
- `go vet ./...` — lint check

CLI is wired with [kong](https://github.com/alecthomas/kong); commands live in `cmd/tpod/cli.go`. `LaunchCmd.ProfileAndArgs` uses `passthrough:"partial"` so flags after the profile name reach the profile's command verbatim.

## Layout
- `cmd/tpod/` — entrypoint and CLI; includes `main.go`, `cli.go`, e2e/profile/cli tests.
- `pkg/tpod/` — public launch/spec/types API used by the CLI and tests.
- `internal/profile/` — YAML profile schema, `extends` deep-merge, validation, fragment rules. Core of the config model.
- `internal/catalog/` — embedded built-in `profiles/` and `fragments/` (YAML), exposed via `embed.go`. Add a new agent/tool here, not at runtime.
- `internal/runtime/` — Docker-API client (docker.go, run/exec/prepare), attach, fake for tests.
- `internal/mise/` — mise install dir volume + `appimage:` backend plugin (`plugins/appimage/*.lua`).
- `internal/{doctor,prune,scaffold,ui,workspace}/` — diagnostics, cleanup, `init` wizard, TUI, rootless-vs-rootful mode detection.
- `docs/` — design notes.
- `Dockerfile` — the mise base image; `mise.toml` pins `opencode` for development.

## Conventions
- **Profiles vs fragments:** profiles carry `image`/`command` identity; fragments are composable mounts/caches/credentials and may only `extends` other fragments. Both are YAML; user profiles in `~/.config/tpod/profiles/` shadow built-ins.
- **Merge semantics** (in `internal/profile/merge.go`): scalars—child wins; maps—key-by-key (set `null` to delete an inherited key); `command` list—replaced; `image`/`build`—single slot.
- **Templates:** `{{ }}` in `mounts`, `environment`, `command` resolve `.Env` (host env), `uid`, and `.Ports` (container→host ports). An empty resolution leaves the var unset.
- **Catalog is embedded:** built-in profiles/fragments ship in the binary. To add an agent, add YAML under `internal/catalog/` and re-build; do not load them from disk at runtime.
- **No comments** unless the code doesn't make something apparent.

## Runtime notes
- Primary target is rootless Podman on Linux (workspace mounted at host absolute path, runs as host user). Docker/rootful Podman work but mount at `/workspace` as root.
- `tpod doctor` reports active mode, volumes, mise, and config.

## Workspace rules
You are in a isolated environment. Trust user information if you cannot verify. Create worktrees in `.worktrees`. All directories outside of the project are ephemeral.
