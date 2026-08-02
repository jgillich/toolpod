# tpod

Go CLI for disposable, reproducible dev environments in a Podman/Docker container with a persistent [mise](https://mise.jdx.dev/) toolchain. `tpod <profile>` mounts your cwd, runs the profile's command, and removes the container on exit; shared volumes keep mise installs and package caches warm across runs.

## Build & test
- `make install` — `go install ./cmd/tpod`
- `go test ./...` — full test suite (Go 1.25, CGO off in releases)
- `go vet ./...` — lint check

CLI is wired with [kong](https://github.com/alecthomas/kong); commands live in `cmd/tpod/cli.go`. `LaunchCmd.ProfileAndArgs` uses `passthrough:"partial"` so flags after the profile name reach the profile's command verbatim.

## Layout
- `cmd/tpod/` — entrypoint and CLI; includes `main.go`, `cli.go`, e2e/profile/cli tests.
- `pkg/tpod/` — public launch/spec/types API used by the CLI and tests.
- `internal/profile/` — YAML profile schema, `extends` deep-merge, validation, fragment rules. Core of the config model.
- `internal/catalog/` — embedded built-in `profiles/` and `fragments/` (YAML), exposed via `embed.go`. Add a new agent/tool here, not at runtime.
- `internal/runtime/` — Docker-API client (docker.go, run/exec/prepare), attach, fake for tests; `docker_build.go` synthesizes derived images for `packages:`.
- `internal/mise/` — mise install dir volume + `appimage:` backend plugin (`plugins/appimage/*.lua`).
- `internal/{doctor,prune,scaffold,ui,workspace}/` — diagnostics, cleanup (`prune` removes catalog-unused volumes/derived images), `init` wizard, TUI, rootless-vs-rootful mode detection.
- `docs/` — design notes.
- There is no base-image Dockerfile: profiles use `debian:13-slim` directly and derived images install everything via `packages:`/`repos:`.

## Conventions
- **Profiles vs fragments:** profiles carry `image`/`command` identity; fragments are composable mounts/caches/credentials and may only `extends` other fragments. Both are YAML; user profiles in `~/.config/tpod/profiles/` shadow built-ins, user fragments live in the parallel `~/.config/tpod/fragments/` (names are globally unique — a profile and fragment sharing a name is a hard catalog-load error).
- **Merge semantics** (in `internal/profile/merge.go`): scalars—child wins; maps—key-by-key (set `null` to delete an inherited key); `command` list—replaced; `packages` list—additive (append with dedup; `packages: null` clears); `repos` map—key-by-key like mounts; `image`/`build`—single slot.
- **System deps via `packages:`:** profiles/fragments declare apt package names; `internal/runtime/docker_build.go` derives a content-addressed tag `tpod/packages:<hash>` from `(base image ID, sorted packages, sorted repos)` and builds/reuses a derived image in `Prepare`. `internal/runtime/` is where the build + caching lives; prune (`internal/prune/prune.go`) removes catalog-unused derived images. `repos:` enables extra apt sources before install — v1 supports `extrepo: <name>` only (custom URL repos are schema-ready but `checkExtrepoOnly` in `Prepare` rejects them). The extrepo package is **not** shipped in the base image; `internal/runtime/extrepo.go` reimplements `extrepo enable` in Go at build time — it reads the base image's Debian codename from `/etc/os-release` (`readImageFile` via a created-not-started probe container + `CopyFromContainer`), fetches the per-version catalog index from `extrepo-team.pages.debian.net`, and resolves each repo to a deb822 `.sources` + signing key (sha256-verified against the catalog) that ride into the derived image via the build context (`tarBuildContext`), replacing the old `extrepo enable` chain with COPYs. The base image is bare (no ca-certificates), so `synthesizeDockerfile` emits a bootstrap RUN installing `ca-certificates` before the repo COPYs when repos are present — the Debian archive is http, so the bootstrap needs no certs; the derived-tag hash stays name-based, so `tpod prune` needs no network. Caveat: mise installs tools into the **shared** `tpod-mise` volume, but compilation now runs inside a per-profile derived image. A tool built under a profile that declares a runtime lib (e.g. `php`'s `libxml2`) lives in the shared volume and may fail to load under a profile whose derived image lacks that lib — so fragment-scoped runtime libs should generally also be declared by any profile expected to run the same tool.
- **`files:`:** profiles/fragments write inline-content files into the
  ephemeral container at launch (between ContainerCreate and ContainerStart
  via CopyToContainer with a tar built by `internal/runtime/docker_run.go`'s
  `tarFiles`). Content supports `{{ }}` templates; targets are absolute or
  `~`-prefixed and must not contain `..`. Files are owned by the execution
  user; missing parent dirs are created automatically and the bootstrap chown
  is extended with `$HOME` parents of file targets so the execution user can
  write them.
- **Prune/doctor derived-image matching:** engines qualify `RepoTags` with a registry (`docker.io/`, `localhost/`, ...), so `listTpodImages`/`checkDerivedImages` normalize via `runtime.DerivedRef` (parses with `github.com/distribution/reference` and matches on the `tpod/packages` path) — never a bare string `HasPrefix`.
- **Templates:** `{{ }}` in `mounts`, `environment`, `command` resolve `.Env` (host env), `uid`, and `.Ports` (container→host ports). An empty resolution leaves the var unset.
- **Catalog is embedded:** built-in profiles/fragments ship in the binary. To add an agent, add YAML under `internal/catalog/` and re-build; do not load them from disk at runtime.
- **No comments** unless the code doesn't make something apparent.

## Runtime notes
- Primary target is rootless Podman on Linux (workspace mounted at host absolute path, runs as host user). Docker/rootful Podman work but mount at `/workspace` as root.
- `tpod doctor` reports active mode, volumes, mise, and config.

## Workspace rules
You are in a isolated environment. Trust user information if you cannot verify. Create worktrees in `.worktrees`. All directories outside of the project are ephemeral.


## Comments

Comments should explain intent, not implementation. Use comments to document business rules, design rationale, edge cases, assumptions, performance trade-offs, and public API contracts—information that cannot be inferred from the code itself. Avoid comments that merely restate what the code does, duplicate obvious syntax, become stale, or leave behind commented-out code. Prefer clear names, simple control flow, and well-factored functions that make the code self-explanatory. When a comment is necessary, ensure it provides lasting value by explaining why the code exists or why it is implemented in a particular way.
