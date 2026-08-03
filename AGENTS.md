# tpd

Go CLI for disposable, reproducible dev environments in a Podman/Docker container with a persistent [mise](https://mise.jdx.dev/) toolchain. `tpd <profile>` mounts your cwd, runs the profile's command, and removes the container on exit; shared volumes keep mise installs and package caches warm across runs.

## Build & test
- `make install` — `go install ./cmd/tpd`
- `go test ./...` — full test suite (Go 1.25, CGO off in releases)
- `go vet ./...` — lint check

CLI is wired with [kong](https://github.com/alecthomas/kong); commands live in `cmd/tpd/cli.go`. `LaunchCmd.ProfileAndArgs` uses `passthrough:"partial"` so flags after the profile name reach the profile's command verbatim.

## Layout
- `cmd/tpd/` — entrypoint and CLI (`main.go`, `cli.go`, e2e/profile/cli tests).
- `pkg/tpd/` — public launch/spec/types API used by the CLI and tests.
- `internal/profile/` — YAML schema, `extends` deep-merge, validation, fragment rules.
- `internal/catalog/` — embedded built-in profiles/fragments (YAML). Add new agents/tools here, not at runtime.
- `internal/runtime/` — Docker-API client (docker.go, run/exec/prepare), attach, test fake; `docker_build.go` synthesizes derived images for `packages:`.
- `internal/mise/` — mise install dir volume + `appimage:` backend plugin (`plugins/appimage/*.lua`).
- `internal/{doctor,prune,scaffold,ui,workspace}/` — diagnostics, cleanup, `init` wizard, TUI, rootless-vs-rootful detection.
- `docs/` — design notes.
- No base-image Dockerfile: profiles use `debian:13-slim` directly; derived images install everything via `packages:`/`repos:`.

## Conventions
- **Profiles vs fragments:** profiles carry `image`/`command` identity; fragments are composable mounts/caches/credentials and may only `extends` other fragments. User YAML in `~/.config/tpd/{profiles,fragments}/` shadows built-ins; names are globally unique (a name clash is a hard catalog-load error).
- **Merge** (`internal/profile/merge.go`): scalars—child wins; maps—key-by-key (`null` deletes an inherited key); `command`—replaced; `packages`—append+dedup (`null` clears); `repos`—key-by-key; `image`/`build`—single slot.
- **System deps (`packages:`/`repos:`):** `docker_build.go` derives a content-addressed image `tpd/packages:<hash>` from `(base image ID, sorted packages, sorted repos)`, built/reused in `Prepare`; `prune` removes catalog-unused derived images. `repos:` v1 is `extrepo: <name>` only (custom URLs are schema-ready but `checkExtrepoOnly` rejects them). The base image ships no `extrepo`; `extrepo.go` reimplements `extrepo enable` in Go at build time — reads Debian codename from the base image, fetches the extrepo catalog, and COPYs deb822 `.sources` + sha256-verified signing keys into the derived image. The base image has no ca-certificates, so the Dockerfile bootstraps `ca-certificates` (Debian archive is http) before the repo COPYs; the derived-tag hash stays name-based, so `prune` needs no network.
- **Shared caches:** the `mise` cache is one volume (`tpd-cache-mise`) covering the mise data dir (`~/.local/share/mise`), its cache dir (`~/.cache/mise`), and the npm backend store (`~/.aube`); multi-path caches mount the shared volume per path via `VolumeOptions.Subpath` on engines that honor it (podman ≥6, docker ≥27.1), else a hashed `tpd-cache-<name>-<hash>` volume per path. Subpath subdirs are pre-created by a helper container (`ensureCacheSubpaths`), never through the volume's host mountpoint, so nothing writes to the host. Compilation runs inside a per-profile derived image, so a tool built under a profile declaring a runtime lib (e.g. `php`'s `libxml2`) may fail to load under another profile — profiles running the same tool should declare the same runtime libs.
- **`files:`:** inline-content files are written between ContainerCreate and ContainerStart via CopyToContainer (tar built by `tarFiles` in `docker_run.go`). `{{ }}` templates supported; targets absolute or `~`-prefixed, never `..`. Owned by the execution user; missing parents auto-created and bootstrap chown covers `$HOME` parents.
- **Prune/doctor image matching:** engines qualify `RepoTags` with a registry (`docker.io/`, `localhost/`, …), so `listTpdImages`/`checkDerivedImages` normalize via `runtime.DerivedRef` (`github.com/distribution/reference`, matching the `tpd/packages` path) — never a bare string `HasPrefix`.
- **Ownership labels & prune:** every volume, derived image, and launched container tpd creates for a launch carries `tpd.managed=true` (`runtime.OwnershipLabel`, `internal/runtime/labels.go`); derived images also carry `tpd.build=1` build provenance. Transient diagnostic/helper resources (the doctor's `tpd-diag-*` volume and container probes, cache-subpath helper, extrepo read) are unlabeled, so a failed-cleanup straggler is invisible to the label-filtered running-container protection and leak check. `prune` removes only labeled resources — unlabeled `tpd-*` volumes/images are reported to stderr as "not tpd-owned" and never auto-removed, and nothing referenced by a running container is removed.
- **`--pull`:** `tpd launch --pull <profile>` re-pulls the base image even when present, refreshing mutable tags; the derived tag hashes the local base image ID, so a new base changes the derived tag and triggers a rebuild.
- **Templates:** `{{ }}` in `mounts`, `environment`, `command` resolve `.Env` (host env), `uid`, and `.Ports` (container→host ports). Empty resolution leaves the var unset.
- **Catalog is embedded:** built-in profiles/fragments ship in the binary; add YAML under `internal/catalog/` and re-build, never load from disk at runtime.
- **No comments** unless the code doesn't make something apparent.

## Runtime notes
- Primary target is rootless Podman on Linux (workspace mounted at host absolute path, runs as host user). Docker/rootful Podman work but mount at `/workspace` as root.
- `tpd doctor` reports active mode, volumes, mise, and config.

## Workspace rules
You are in an isolated environment. Trust user information if you cannot verify. Create worktrees in `.worktrees`. All directories outside of the project are ephemeral.

## Comments
Comments should explain intent, not implementation — business rules, design rationale, edge cases, assumptions, trade-offs, and public API contracts. Skip comments that restate code, are stale, or leave commented-out code; prefer clear names and simple code. When a comment is needed, explain why the code exists or is shaped that way.
