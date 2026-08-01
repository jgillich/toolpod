# toolpod — Design Doc

**Status:** Draft for review
**Date:** 2026-07-30
**Author:** jgillich (via brainstorming session)

## 1. Purpose

Every developer eventually writes shell scripts or Makefiles that launch containers with the right mounts, SSH keys, caches, tool versions, and AI agents. Those scripts are project-specific, hard to share, and painful to maintain across many repositories. toolpod replaces those ad hoc wrappers with **user-owned, reusable profiles** built on disposable containers and a shared [mise](https://mise.jdx.dev/) environment.

In one line:

```
container  +  workspace  +  persistent mise  =  toolpod
```

`toolpod` provides a disposable, reproducible development environment with a persistent mise environment shared across runs. Tools — AI agents (opencode, codex), language toolchains (node, python, rust), and project dependencies — are all managed through mise. The container provides the OS and mise; everything else is a tool.

This is a **user-owned** environment, not a project-owned one. toolpod describes how *you* prefer to work across all of your projects, without requiring changes to any repository. A maintainer should not have to merge your AI agent, your mise config, your SSH keys, and your caches into the project. toolpod keeps your environment yours; the project remains untouched.

AI coding agents are the flagship use case — `toolpod opencode` spins up a container, mounts your workspace, runs the agent, and removes the container on exit. But because the architecture is general, `toolpod shell` gives you a disposable, project-aware shell with the correct tools on PATH, useful even without an agent.

Example usage:

```
$ cd ~/projects/myapp
$ toolpod opencode --model foo
# spins up a container, mounts $PWD, runs `opencode --model foo`, removes container on exit
```

It ships with built-in profiles for popular agents (opencode, codex, plus a `shell` catch-all) and allows users to add or override profiles by dropping YAML files into a user config directory. Profiles can inherit from one another via an `extends:` field.

The container engine (Docker or Podman) is selected via `DOCKER_HOST` and is an implementation detail of the runtime layer. **Best experience: rootless Podman** (see §5), which gives correct file ownership and host-path parity for free.

The core functionality is abstracted behind a reusable library package so a GUI (or tests, or other tooling) can drive the same logic in the future; the CLI is a thin wrapper.

## 2. Scope

### In scope (v1)

- Launching a profile in an ephemeral container that mounts the caller's workspace read-write.
- **Workspace mount and user-mirroring, rootless-Podman mode:** when `DOCKER_HOST` points at a rootless Podman socket, the workspace is mounted at its host absolute path and the agent runs as the host user (near no-op mirroring, full path/ownership parity).
- **Fallback mode:** on any other runtime (Docker, rootful Podman), the workspace is mounted at `/workspace` read-write and the agent runs as root; files written are root-owned on the host (documented limitation). Full cross-platform UID remapping deferred to v1.1.
- Plain-YAML profiles with `extends:` inheritance and deep-merge semantics. Lists converted to maps keyed by natural identity (target, env var, tool name) so children can override/add/remove individual entries without redeclaring the whole list. Null-to-delete rule for removing inherited keys.
- `version:` field on every config (config schema version, for future migrations).
- Built-in configs for opencode, codex, and a `shell` catch-all, embedded in the binary. (t3 code is deferred — it is not in mise's registry; it will be added when a mise registry entry or a `build:` config is available.)
- User override/shadow configs in `~/.config/toolpod/` (Linux/macOS, XDG) or `%APPDATA%\toolpod\` (Windows).
- Image reference **or** Dockerfile build per profile. **mise is the foundation:** agents available in mise's registry (opencode, codex) use a pure-mise config — `image:` is the mise base image, and the agent itself is a `tools:` entry (e.g. `opencode: latest`), installed via mise like any other tool. No per-profile Dockerfile needed. Agents not in mise's registry (e.g. t3 code) use `build:` with a Dockerfile embedded in the binary (via `go:embed`), built on demand and tagged locally (`toolpod/<name>:latest`). User Dockerfiles can `FROM toolpod/<name>:latest` to layer on our bases; the user declares build dependencies explicitly via `build.depends_on`.
- Tilde (`~/`) expansion on mount `source:` values (expands to host `$HOME`) and on mount/cache/mise **target** paths (expands to the runtime user's home — `/root` in Mode B, the mirrored host user's home in Mode A), so built-in configs are portable across both modes without hardcoding `/root/...`. No other interpolation or expressions.
- Additional read-only-by-default mounts (docker `-v` / `--mount` style, as map entries keyed by target), with optional `read_only: false` for writable mounts.
- Environment variables as a map keyed by variable name (empty value = passthrough, literal value = set).
- `caches:` map for shared named-volume-backed cache directories (npm, cargo, pip, go…) reused across profiles.
- `tools:` map backed by mise, auto-installed at launch into a shared volume; project-local `mise.toml`/`.tool-versions` auto-installed as well.
- Transparent passthrough of CLI args to the agent.
- Ephemeral container lifecycle: create, run, remove on exit (success or failure).
- Runtime selection via `DOCKER_HOST` (Docker or Podman's Docker-compatible service) using the Docker Engine Go SDK.
- `toolpod config show <name>` to print the resolved config.
- `toolpod doctor` to diagnose the environment (runtime reachability, rootless detection, BuildKit, mise, shared volumes, permissions, config validity, workspace writability) — surfaces misconfiguration before a confusing launch failure.
- `toolpod prune` to remove toolpod-managed volumes and images that accumulate over time.

### Out of scope (v1)

- Instruction/prompt injection into agent files (e.g. appending to `AGENTS.md`). Out of scope for v1; the project's own `AGENTS.md` and mise's presence on PATH cover the real need. May return in v1.1 as a per-profile `instructions:` field with append/replace modes.
- System-package management (apt/dnf). mise manages tools (rust, python, node…); system libraries belong in the image/Dockerfile.
- Persistent/reused containers. Each run creates a fresh container and removes it on exit. State persists only in mounted host paths, the shared mise volume, and named cache volumes.
- Kubernetes as a backend.
- A GUI. The architecture supports one in the future, but v1 ships only the CLI.
- Generic `files:` block for writing files into the container at launch. Dropped; stacked-mount approach is viable mechanically but not worth the complexity for v1.
- **Cross-platform UID remapping / full user-mirroring on Docker and rootful Podman.** v1 only mirrors on rootless Podman (where it's near-free). Other runtimes fall back to `/workspace` + root. Defer proper UID mapping to v1.1.
- **Project generation / project-owned config.** toolpod never writes `.devcontainer`, `Dockerfile`, `docker-compose.yml`, or any config into the project repo. The environment is user-owned; the project remains untouched. This is a core design property, not a missing feature.

## 3. Architecture

### 3.1 Package layout

```
toolpod/
  cmd/toolpod/        # thin CLI entrypoint: arg parsing, stdio, exit codes
  internal/
    config/                # config loading, merging, extends resolution, validation
    runtime/               # the Runtime interface + Docker API SDK implementation
    catalog/               # embedded built-in profiles (go:embed)
    build/                 # on-demand image build, depends_on resolution, drift-detection hints
    ui/                    # CLI output / progress rendering (TTY-aware)
    mise/                  # mise integration: ensure-tools, shared volume mgmt
    caches/                # named-volume-backed cache dir management (npm/cargo/pip/go)
    workspace/             # workspace mount + rootless-Podman detection + mode selection
  pkg/toolpod/         # public, reusable library: Launch(opts) -> Result
  configs/                 # source YAML for embedded built-in profiles
  docs/                    # design docs, user-facing docs
```

### 3.2 Key abstraction — `Runtime` interface

The runtime layer is the single point of indirection between "resolved config + invocation options" and "talk to a container engine." v1 has one implementation (Docker API SDK); a future GUI or alternate engine plugs in here without touching the rest.

```go
// internal/runtime/runtime.go

type Runtime interface {
    // Prepare does everything before the profile starts: ensures the image
    // (pull or build), ensures the mise volume and cache volumes exist,
    // installs config tools into the mise volume, and activates mise.
    // Reports progress to w.
    //
    // Prepare is not transactional: it may persist side effects (pulled
    // images, created volumes, installed tools) that survive a later Run
    // failure. Re-running Prepare is safe (idempotent where possible).
    Prepare(ctx context.Context, spec Spec, w ProgressWriter) error

    // Run creates an ephemeral container, sets up mounts/env/user,
    // attaches stdio (with TTY if requested), starts it, waits for exit,
    // removes the container, and returns the exit code.
    Run(ctx context.Context, spec Spec) (int, error)
}
```

`pkg/toolpod.Launch` orchestrates: resolve config → `Prepare` (image + mise volume + tools) → `Run` (container + attach + exit). The CLI (`cmd/toolpod`) just wires `os.Args` → `toolpod.Launch` and propagates the exit code. A future GUI imports `pkg/toolpod` with a custom `ProgressWriter` and programmatic stdio.

### 3.3 Runtime implementation: Docker Engine Go SDK

Single implementation against `github.com/docker/docker/client`, which honors `DOCKER_HOST`. Whatever socket `DOCKER_HOST` points at (Docker daemon, or Podman's Docker-compatible service via `podman system service`) is what we talk to. No `runtime:` config field, no auto-selection logic — the user controls the engine by setting `DOCKER_HOST` (its default is the Docker socket; Podman users set it to the Podman socket).

**Podman caveat:** Podman must be running its Docker-compatible service (`podman system service --time=0 unix://...` or a systemd unit). If we detect a connection failure and `DOCKER_HOST` points at a Podman socket, we print a clear hint to start the service. We do **not** auto-start it.

**Interactive attach is the trickiest part of this approach.** We must:
- Use `ContainerAttach` with the hijacked connection.
- Manually pump stdin→container and container→stdout/stderr.
- Handle terminal resize events: on `SIGWINCH`, call `ContainerResize` with the new rows/cols (only when TTY is active).
- Forward signals: `SIGINT`/`SIGTERM` from the host are sent to the container via `ContainerKill` (or the API's signal endpoint) rather than terminating toolpod itself, so the agent can clean up.
- Ensure the container is always removed in a `defer`/cleanup path, even on signal or error, unless the user explicitly passes `--keep` (future; not in v1).

This is well-trodden territory (the Docker CLI does it; examples exist in the SDK) and is treated as a dedicated, tested module in the implementation.

### 3.4 Image build system (escape hatch for custom images)

mise is the foundation: agents available in mise's registry (opencode, codex) use a pure-mise config — `image:` is the mise base image and the agent is a `tools:` entry (see §6). No per-profile Dockerfile, no build step, no registry to host.

The `build:` path is an **escape hatch** for users who need a custom image — typically because they need system packages (`libssl-dev`, `build-essential`) that mise can't provide, or because they want to layer on top of a vendor image. Built-in v1 configs do not use `build:`; it exists for user profiles and future built-ins for agents not in mise's registry.

**Layout (v1 — no built-in Dockerfiles):**

```
configs/
  opencode.yaml          # pure-mise: image: <mise base>, tools: { opencode: latest, ... }
  codex.yaml             # pure-mise: image: <mise base>, tools: { codex: latest, ... }
  shell.yaml             # image: <mise base>, command: ["sh"], no profile-specific tool
```

**User `build:` config example** (escape hatch):

```yaml
# ~/.config/toolpod/myagent.yaml
version: 1
build:
  dockerfile: ./my.Dockerfile
  depends_on: []            # or list configs whose images must be built first
command: ["myagent"]
```

**Local tagging.** Built images are tagged `toolpod/<config-name>:latest` locally. No registry push, no credentials. During `Prepare`, `image:`-based profiles ensure the referenced image is available (pulling if necessary), while `build:`-based profiles ensure the local image exists (building if missing or when `--rebuild` is set).

**`--rebuild` flag** forces a rebuild (pull fresh base + rebuild). Without it, the cached local image is reused. v1 does not auto-check for base-image updates at launch (too slow); `--rebuild` is the explicit refresh path.

**User Dockerfiles that `FROM` our bases.** A user overriding `build:` with a Dockerfile can `FROM toolpod/<name>:latest`. To build required bases first, the user declares them explicitly via `build.depends_on`:

```yaml
build:
  dockerfile: ./my.Dockerfile
  depends_on: [opencode]            # build the `opencode` config's image first
```

**Drift detection.** If a build fails with an "image not found" error referencing a `toolpod/*` tag that isn't in `depends_on`, we print a hint: "this Dockerfile references `toolpod/<name>:latest` — add it to `build.depends_on`."

`depends_on` entries are resolved recursively (a dependency may itself have `depends_on`); cycles are detected and rejected. Dependencies are built in dependency order before the config's own image is built.

## 4. Config Schema

Plain YAML, one file per profile. The file name (sans extension) is the profile name used on the CLI. Built-in profiles live in `configs/` and are embedded via `go:embed`; user overrides live in the user config dir.

### 4.1 Full schema

```yaml
# Config schema version. Required. v1 is the current schema. Allows future
# migrations with backwards-compatible loading.
version: 1

# Optional: inherit from another config (built-in or user), then deep-merge
# overrides on top. Cycles are detected and rejected at load time.
extends: opencode

# Image source. Exactly one of `image` or `build` is required in the
# *resolved* config (after inheritance). These two are treated as a single
# "image source" slot under extends (see §4.3).
image: ghcr.io/opencode/opencode:latest
# build:
#   dockerfile: ./Dockerfile          # path relative to the config file dir.
#                                     # For built-in configs using build:: a
#                                     # name resolved against the embedded
#                                     # configs dir.
#   context: .                        # build context dir, relative to config file dir
#   depends_on: [opencode]            # optional: build these configs' images
#                                     # first (recursive; cycles rejected).
#                                     # Used when this Dockerfile FROMs an
#                                     # toolpod/<name>:latest image.

# Required: the command to run inside the container. First element is the
# binary; trailing elements are default args used only when the user passes
# none on the CLI. User args replace the defaults (binary stays). Stays a
# list — rarely overridden, no natural key.
command: ["opencode"]

# Additional mounts, as a map keyed by container target path. Keyed by
# target so a child extending this config can override or remove a single
# mount without redeclaring the rest. Default read-only; set read_only: false
# for writable. Both target and source support a leading ~/ tilde:
#   - target ~/... resolves to the runtime user's home dir at launch
#     (/root in Mode B fallback, the mirrored host user's home in Mode A
#     rootless-Podman). This keeps built-ins portable across both modes.
#   - source ~/... resolves to the host user's $HOME.
# No other interpolation. (Set a key to null in a child config to delete an
# inherited mount — see §4.3.)
mounts:
  ~/.config/opencode:                 # ~ → runtime user home (Mode A: /home/me, Mode B: /root)
    source: ~/.config/opencode        # ~ → host $HOME
    read_only: true
  ~/.cache/opencode:
    source: ~/.cache/opencode
    read_only: false
  ~/.gitconfig:
    source: ~/.gitconfig
    read_only: true

# Environment variables, as a map keyed by variable name. Empty string ("")
# means "pass through the host env value if set"; a literal value sets it.
# null is NOT a passthrough synonym — null means delete-on-inherit (see
# §4.3) everywhere, including environment. No interpolation.
environment:
  OPENCODE_API_KEY: ""        # passthrough (empty string, NOT null)
  GIT_EDITOR: vim             # literal

# Shared named-volume-backed cache directories, as a map keyed by a cache
# name. Each entry mounts a Docker named volume `toolpod-cache-<name>`
# at the given target path, read-write, shared across all profiles. Target
# supports ~/ tilde (resolves to runtime user's home — see mounts above).
caches:
  npm: ~/.npm
  cargo: ~/.cargo
  pip: ~/.cache/pip
  go: ~/go

# Optional labels for the created container (informational, for `docker ps`).
labels:
  profile: opencode

# Network mode passed through to the runtime.
network: bridge             # bridge (default) | host | none | <custom name>

# Resource hints (best-effort; runtime may ignore). Optional; built-in
# profiles do NOT set resources, so the container gets the host's full
# resources by default. Set these only to constrain a profile.
# resources:
#   memory: 4g
#   cpus: "4.0"

# TTY. Default: auto (true when stdout is a TTY). Can be forced.
tty: auto                  # auto | true | false

# Optional: mise-managed tools to ensure are installed in the shared
# mise volume before launching the profile. Map keyed by tool name; value is
# the version. Idempotent (skips installed). Built-ins intentionally use
# `latest`; users are encouraged to pin (e.g. `opencode: "0.11.2"`) when
# reproducibility matters. Project-local mise.toml / .tool-versions is
# handled by mise natively (see §6.3) — not managed here.
tools:
  node: "20"
  python: "3.12"
```

### 4.2 Workspace mount (CLI, not config)

The workspace mount is a CLI concern, not a per-profile config field. The CLI always mounts the caller's current directory (or `--workspace <path>`) read-write. The mount target and user-mirroring behavior depend on the runtime mode (see §5):

- **Mode A (rootless Podman):** workspace mounted at its **absolute host path** (e.g. `/home/me/projects/myapp` → `/home/me/projects/myapp`). Paths the agent shows the user match host paths exactly. The agent runs as the host user, so file ownership is correct.
- **Mode B (Docker, rootful Podman):** workspace mounted at `/workspace`. Paths the agent shows (`/workspace`) do **not** match host paths — the user must mentally translate. The agent runs as root, so files written to the workspace are root-owned on the host (documented limitation; clean up with `sudo chown` or switch to rootless Podman for Mode A).

The container working directory is set to the workspace mount target (the host path in Mode A, `/workspace` in Mode B).

### 4.3 Inheritance and merge semantics

**`extends: <name>`** resolves against the full merged catalog (built-ins + user configs). Cycles are detected and rejected at load time. Inheritance can chain (a base can itself extend), with cycle detection across the full chain.

**Merge rules:**
- **Scalars** (strings, numbers, bools): child replaces parent's.
- **Maps** (`mounts`, `environment`, `tools`, `caches`, `labels`, `resources`): merged key-by-key. A child key overrides the parent's value at that key. **Null-to-delete:** a child key set to `null` removes that key from the merged result entirely (lets a child drop an inherited mount, env var, cache, or tool without redeclaring the map). This is why the lists-converted-to-maps use natural keys (target path, var name, tool name, cache name): it gives stable identity per entry so override and delete are precise.
- **Lists** (`command`): **replaced**, not concatenated. These are rarely overridden and have no natural key, so lists remain. A child that sets `command` fully replaces the parent's (uncommon; usually inherited).
- **`image`/`build` — the image-source slot — replace-semantics.** Because exactly one of `image` or `build` must be present in the resolved config, these two fields are treated as a single slot, not independent fields:

  | Parent has        | Child sets       | Resolved result                            |
  |-------------------|------------------|--------------------------------------------|
  | `image: foo`      | `image: bar`     | `image: bar`                               |
  | `image: foo`      | `build: {...}`   | `build: {...}` (parent `image` dropped)    |
  | `build: {...}`    | `image: bar`     | `image: bar` (parent `build` dropped)      |
  | `build: {...}`    | `build: {...}`   | child's `build` (parent's dropped)         |
  | `image: foo`      | (neither)        | `image: foo` (inherited)                   |

  After merge, validation enforces "exactly one of `image`/`build` present."

- **`version`:** not merged — the child's `version` (if present) wins; if absent, the parent's is inherited. All configs in an `extends` chain should declare the same `version` (validation warns on mismatch).
- **Validation runs on the resolved config**, not the raw child. A raw child may omit required fields if they come from the parent.

### 4.4 Config discovery and precedence

1. **Load embedded built-in configs** (from `configs/`, via `go:embed`) into a name→raw-config map.
2. **Load user config dir** (`~/.config/toolpod/*.yaml` on Linux/macOS via XDG; `%APPDATA%\toolpod\*.yaml` on Windows). User entries are keyed by file basename without extension.
3. **Shadowing:** a user config with the same name as a built-in shadows the built-in entry. The shadowing user config may still `extends: <built-in-name>` to amend the built-in rather than replace it wholesale.
4. **`extends:` resolves at use time** against the final merged map, so a user override of a base name also affects configs that extend that base. Resolution is performed lazily when a config is launched (or when `config show`/`config check` is invoked), not eagerly at load time, so overrides to a base propagate to extenders.
5. An explicit `--config-dir <path>` flag (and `TOOLPOD_CONFIG_DIR` env var) overrides the user config dir location for debugging/testing. Built-in configs are always loaded; the flag only adds/overrides the user layer.
6. **Reserved names.** Profile names that collide with subcommands are rejected at load time: `config`, `doctor`, `help`, `version`, `completion`, `prune`. A user config file named e.g. `doctor.yaml` fails validation with a clear message.

### 4.5 Example user override

`~/.config/toolpod/opencode.yaml` — pin the opencode tool version and add a mount, inheriting everything else from the built-in `opencode`. Maps let us add one mount or override one tool without redeclaring the rest:

```yaml
version: 1
extends: opencode
tools:
  opencode: "0.9.0"            # pin a specific version (overrides inherited "latest")
mounts:
  ~/.local/share/opencode/knowledge:   # ~ target → runtime user home (§5.6)
    source: ~/work/shared-knowledge    # ~ source → host $HOME
    read_only: false
  # If we wanted to drop an inherited mount instead:
  # ~/.cache/opencode: null
```

A user who needs system packages (the `build:` escape hatch) overrides `build:` with a custom Dockerfile and declares `depends_on` if it `FROM`s one of our local tags — see §3.4.

## 5. Workspace mount and host user mirroring

This is a runtime-layer concern, performed for every launch regardless of config. v1 has **two modes** selected by detecting whether `DOCKER_HOST` points at a rootless Podman socket.

### 5.1 Why mirror the user

If the container runs as root (Mode B), files created by the agent in the mounted workspace are owned by root on the host — unusable by the host user without `sudo` cleanup, and the workspace is at `/workspace` (paths the agent shows don't match host paths). Mirroring the host user's UID/GID into the container and mounting the workspace at its host absolute path (Mode A) ensures files written to the workspace are owned by the host user and paths match. v1 only achieves this on rootless Podman (Mode A); Docker and rootful Podman fall back to Mode B (see §5.5 for the v1.1 plan).

### 5.2 Mode A — rootless Podman (full mirroring)

When `DOCKER_HOST` points at a rootless Podman socket (detected via the Podman `info` API `rootless` field), mirroring is nearly a no-op:

- Rootless Podman already runs as the host user; the agent runs as that user automatically.
- The workspace is mounted at its **absolute host path** (e.g. `/home/me/projects/myapp` → `/home/me/projects/myapp` inside the container). Paths the agent shows match host paths.
- File ownership is correct by construction (the in-container user *is* the host user).
- The container working directory is set to the workspace mount target.
- `HOME` is the host user's home dir, so `~/.config/...` mounts resolve correctly.

No entrypoint injection or UID remapping is required in this mode — the rootless runtime gives it to us for free. This is the "good" path and the recommended runtime for v1.

### 5.3 Mode B — fallback (Docker, rootful Podman, anything else)

For any runtime that is not rootless Podman, we use a simple, robust fallback:

- The workspace is mounted at `/workspace` read-write (the conventional fixed path). Paths the agent shows (`/workspace/...`) do **not** match host paths — the user must mentally translate between the two.
- The agent runs as **root** inside the container.
- The container working directory is `/workspace`.
- Files written to the workspace are **root-owned on the host.** This is a documented limitation: the user can clean up ownership with `sudo chown`, or run a rootless Podman socket instead to get Mode A (host-path mount + correct ownership).

### 5.4 Detecting rootless Podman

At launch, `internal/runtime` inspects the engine behind `DOCKER_HOST`:
1. Call the `info`-equivalent endpoint. Podman's Docker-compatible API exposes a `rootless` boolean in its system info.
2. If `rootless: true` → Mode A. Otherwise → Mode B.
3. If the API doesn't expose `rootless` (plain Docker), → Mode B.

The detection result is logged at startup (e.g. "runtime: rootless podman → workspace at host path" or "runtime: docker → workspace at /workspace (root)") so the user understands which mode is active.

### 5.5 Defer to v1.1

Full cross-platform UID remapping for Docker and rootful Podman (matching the devcontainer `updateRemoteUserUID` / `distrobox` entrypoint-injection approach) is deferred to v1.1. v1 targets Linux; macOS/Windows Docker-Desktop UID parity is also v1.1. The architecture (Runtime interface, mount/user setup in one place) makes adding this later straightforward without rearchitecting.

### 5.6 Tilde resolution for container targets

Because the two modes run as different users with different home directories, built-in configs must not hardcode `/root/...` paths — those only work in Mode B. A single `~` prefix convention covers mounts, caches, and the mise volume:

- **Mount targets** (`mounts:` map keys) and **cache targets** (`caches:` map values) starting with `~/` are resolved at launch to the **runtime user's home directory**: `/root` in Mode B, the mirrored host user's home (e.g. `/home/me`) in Mode A. So `~/.config/opencode` becomes `/root/.config/opencode` (Mode B) or `/home/me/.config/opencode` (Mode A), matching the agent's actual `$HOME`-relative reads.
- **Mount sources** (`mounts:` `source:` values) starting with `~/` are resolved to the **host user's `$HOME`** (always the host, regardless of mode).
- **The mise volume** is mounted at `~/.local/share/mise`, resolved the same way.
- No other interpolation or expression — just leading `~/` on these path fields. Hardcoded absolute paths (e.g. `/workspace`) are left as-is.

This is the one mechanism that keeps built-in configs portable across both modes without per-mode config duplication.

## 6. mise integration (the foundation)

### 6.1 Problem

Profiles run against arbitrary projects with arbitrary tool needs (rust, python, node, go…). Baking every tool into every profile image is infeasible, and per-language profiles (`opencode-rust`, `opencode-python`) multiply the combinatorial problem. Per-project system packages are out of scope (see §2), but per-project *tool versions* are tractable via mise. Crucially, the agents themselves (opencode, codex) are also available in mise's registry — so mise is the foundation for both the profile and project tools.

### 6.2 Approach

[mise](https://mise.jdx.dev/) (formerly rtx) is a polyglot version manager — asdf's modern, fast successor. It manages node/python/rust/go/ruby etc. via plugins, reads version requirements from `.tool-versions` or `mise.toml` in the project dir, and installs tools to a shared directory. Agents available in mise's registry (opencode, codex) are installed the same way. This maps cleanly onto the problem:

- **No combinatorial image explosion.** One mise base image for everything. The agent itself is a `tools:` entry (`opencode: latest`), and per-project needs come from the project's own `mise.toml`/`.tool-versions`, which mise auto-detects when the agent's shell enters the workspace. A Rust project just has `.tool-versions` saying `rust 1.74.1`; the agent's shell gets rust on PATH automatically. No `opencode-rust` profile needed.
- **Agents are just tools.** `opencode@latest` or `opencode@0.9.0` works exactly like `node@20`. Unified version management. Adding a new agent is a `tools:` entry, not a Dockerfile (for agents in mise's registry). This is why v1 has no per-profile Dockerfiles.
- **Cached across runs.** Tools install to a Docker *named volume* mounted at mise's install dir (`~/.local/share/mise` — the `~` target resolves to the runtime user's home per §5.6, so `/root/.local/share/mise` in Mode B or `/home/<user>/.local/share/mise` in Mode A). First launch of a given tool is slow (minutes); every subsequent launch is instant because the volume is warm. A volume beats "install fresh each run."
- **Project-aware for free.** Because mise reads `.tool-versions` from the cwd, the *same* profile works across all projects without per-project config — the project declares what it needs.

### 6.3 Config

A `tools:` map in the config (see §4.1) declares the tools to ensure are present in the shared volume before launching — the agent itself and any baseline tools (e.g. `opencode: latest`, `node: "20"`). Idempotent (skips already-installed). `--tool name=version` on the CLI adds ad-hoc entries, merged with the config's map.

At launch, `internal/mise` runs `mise install` against the config's `tools:` map only. **Project-local tool files (`mise.toml`/`.tool-versions`) are not handled by toolpod** — mise's native `mise activate` (which the runtime invokes to set up the shell) auto-detects these when the shell enters the workspace and installs/activates them itself. We deliberately do not reimplement project-file detection; mise already does it correctly and we lean on that behavior.

**Concurrent installs.** Because the mise volume is shared across all profiles, two concurrent launches (e.g. `toolpod opencode` and `toolpod shell` in different terminals) could both try to install into the same volume simultaneously. `Prepare` acquires an exclusive lock (an advisory file lock on a sentinel file in the mise volume) before running `mise install`; other launches wait until the install completes. This prevents partial/corrupted installs without relying on mise's own concurrency behavior.

`mise activate` is then set up so the shell PATH includes both the config's tools and any project-declared tools, resolved at the workspace directory.

### 6.4 Named volumes

Internally, the mise volume and all `caches:` entries are the same abstraction — a **named volume**: a Docker named volume, mounted at a target path in the container, shared across all profile launches. One internal type (`NamedVolume { name, target }`) covers both. The `caches:` map (§4.1) declares user-visible cache volumes; the mise volume (`toolpod-mise`) is a built-in named volume that mise's install dir happens to live in.

All named volumes are shared across *all* profiles (not per-profile). Tools and caches are installed once and reused — no point installing node twice for opencode and codex. mise's install layout version-isolates (`installs/node/20.x/`, `installs/python/3.12.x/`), so sharing is safe. Volumes are owned by the mirrored user (chown'd at launch in rootless-Podman mode; root-owned in fallback mode). A corrupt install only affects that one volume; the user can `docker volume rm toolpod-mise` (or `toolpod-cache-<name>`) to reset.

Built-in configs declare common caches (npm, cargo, pip, go) so users get warm package-manager caches for free.

### 6.5 Image requirements

Built-in configs use the mise base image directly (`image:`), so mise is always present for them — the agent itself is installed as a `tools:` entry. A user config that uses `image:` with a non-mise image and sets `tools:` or has a workspace with `mise.toml` must include mise on PATH — otherwise we **fail fast** with a clear message ("image must include `mise` on PATH when `tools:` is set or the workspace uses mise; use `build:` with a Dockerfile that installs mise, or `extends:` a built-in"). This is a documented requirement for custom `image:`-based configs.

### 6.6 System packages

Explicitly out of scope for v1. mise manages tools (rust, python, node…). It does not handle apt/system packages like `libssl-dev`, `build-essential`, `pkg-config`. Those belong in the image/Dockerfile. If a project needs system libraries, the user builds a custom image (via `build:` in their config) on top of our base, or maintains their own.

## 7. CLI surface

```
toolpod <profile-name> [args...]            # launch a profile (default command)
toolpod config show <name> [--resolved]    # print a config (raw or fully resolved/merged)
toolpod config list                         # list available config names (built-in + user)
toolpod config check [name]                # validate config(s); print errors with file:line
toolpod doctor                              # diagnose the environment (runtime, mise, caches, permissions)
toolpod prune                               # remove toolpod-managed volumes and images
toolpod --help
toolpod --version
```

### 7.1 Launch command (default)

```
toolpod <profile-name> [args...]
```

- `<profile-name>` selects a profile from the merged catalog (built-in + user).
- Everything after `<profile-name>` is passed **verbatim** to the profile's `command` as args. `toolpod opencode --model foo --prompt bar` runs `opencode --model foo --prompt bar` inside the container (i.e. `command` + passthrough args).
- `toolpod`-owned flags must come *before* `<profile-name>` (e.g. `toolpod --workspace ~/p2 opencode`). This keeps passthrough transparent and avoids flag collisions.

Global flags (before the profile name):

- `--workspace <path>` — workspace to mount (default: `$PWD`). Mounted RW at its absolute path.
- `--config-dir <path>` — override user config dir (also `TOOLPOD_CONFIG_DIR` env var).
- `--tool <name>=<version>` — add a mise tool (repeatable); merged with config `tools:` (e.g. `--tool node=20`).
- `--rebuild` — force a rebuild of the config's image (pull fresh base + rebuild). Without it, the cached local image is reused.
- `--dry-run` — print the resolved config and the container spec (image, mounts, env, command) without launching.
- `--verbose` / `-v` — print the resolved container spec (image, mounts, env, user, runtime mode) before launching, then launch normally. Useful for debugging unexpected behavior.
- `--keep` — *(future, not v1)* keep the container after exit for debugging.

### 7.2 Config subcommands

- `config show <name>` — print the raw config as YAML.
- `config show <name> --resolved` — print the fully resolved config (after `extends` merge and validation) as YAML, with `~` tilde paths expanded to absolute host/container paths so users can verify exactly what will be mounted where.
- `config list` — list available config names (built-ins marked, user overrides marked).
- `config check [name]` — validate one or all configs; print errors with file:line. Non-zero exit on any invalid config.

### 7.3 `doctor` command

`toolpod doctor` diagnoses the local environment and reports what's working, what's missing, and what mode will be active. Designed to save bug reports by surfacing misconfiguration before a launch fails confusingly. Prints a checklist with pass/fail/warn per item and a summary. Exit code 0 if all critical checks pass, non-zero if any fail.

**Checks performed:**

1. **Runtime reachable.** Ping the engine at `DOCKER_HOST` (Docker API `info` endpoint). Report which engine (Docker / Podman / unknown) and the API version. If unreachable: fail with a hint ("is the Docker daemon running?" or "for Podman, start `podman system service`").
2. **Rootless detection.** If the engine is Podman, query the `rootless` field from `info`. Report "rootless podman → Mode A (full mirroring)" or "docker/rootful → Mode B (/workspace fallback)". This tells the user which workspace/mirroring mode they'll get before they launch.
3. **BuildKit available.** Check whether the engine supports `docker build` (BuildKit). Needed for configs using `build:`. Warn (not fail) if unavailable — pure-mise configs don't need it.
4. **mise base image present.** Check whether the mise base image (used by built-in configs) is present locally. If not, report "will be pulled on first launch" (info, not a failure).
5. **mise functional.** Run `mise version` inside a throwaway container from the base image to confirm mise is on PATH and working. If the base image isn't present yet, skip this check with a note ("pull the base image first with `toolpod <name>` or `docker pull <image>`").
6. **Shared volumes.** Check that the mise volume (`toolpod-mise`) and cache volumes (`toolpod-cache-<name>`) either exist or can be created. Report existing volumes and their size; flag any that are corrupt or inaccessible.
7. **Permissions.** Verify the user can create containers, create volumes, and (if Mode A / rootless Podman) that the socket is accessible. On permission failure: fail with a specific hint ("add user to docker group" / "use rootless Podman" / "check socket permissions").
8. **Config validity.** Run the same validation as `config check` across all loaded configs (built-in + user). Report any invalid configs with file:line. This catches config errors that would only surface at launch time.
9. **Detected project tools.** If the workspace has `mise.toml` or `.tool-versions`, parse and list the tools mise would install/activate for this project (e.g. `node@22`, `python@3.13`, `rust@1.90`). Helps debug "why did it install python 3.12?" before launching.
10. **Workspace writability.** Check that the current directory (or `--workspace`) is writable by the host user — a common failure (read-only mounts, permission issues) that produces confusing errors inside the profile otherwise.

**Output format:** a checklist, one line per check, colored pass/fail/warn (green/red/yellow) when stdout is a TTY, plain text otherwise. Example:

```
[pass] runtime: docker 27.0 at unix:///var/run/docker.sock
[pass] rootless: no → Mode B (/workspace fallback)
[warn] buildkit: available
[info] mise base image: not present (will pull on first launch)
[skip] mise functional: skipped (base image not yet pulled)
[pass] volumes: toolpod-mise (1.2 GB), toolpod-cache-npm (480 MB)
[pass] permissions: can create containers and volumes
[pass] configs: 3 built-in, 1 user override — all valid
[pass] project tools: node@22, python@3.13, rust@1.90 (from mise.toml)
[pass] workspace: /home/me/projects/myapp is writable

Summary: 1 warning, all critical checks passed.
```

**Not a launch gate.** `doctor` is informational; a failing check doesn't prevent `toolpod <name>` from running. It's a diagnostic tool the user runs when something is wrong, or proactively to verify their setup. Launch-time errors still produce targeted hints (e.g. the Podman-service hint, the mise-missing hint).

### 7.4 `prune` command

`toolpod prune` removes toolpod-managed state that accumulates over time: named volumes (`toolpod-mise`, `toolpod-cache-*`) and locally built images (`toolpod/*:latest`). Flags select what to remove:

- `--volumes` — remove toolpod-managed named volumes.
- `--images` — remove toolpod-tagged local images.
- `--all` — remove both (default if no flag is given).

`prune` lists what it will remove and prompts for confirmation (or accepts `-y` / `--force` to skip the prompt) before deleting anything. It only touches resources tagged/named with the `toolpod-` / `toolpod/` prefixes; it never removes unrelated volumes or images.

## 8. Container lifecycle

- **Ephemeral.** Each `toolpod <name>` invocation creates a fresh container, runs the profile's command, and removes the container on exit — success, failure, or signal. Container state never leaks between runs; persistent state lives only in mounted host paths (workspace, config dirs, caches) and the shared mise volume.
- Container name: `toolpod-<profile-name>-<short-id>` (random suffix) so concurrent launches and `docker ps` are unambiguous.
- Cleanup is guaranteed via a `defer` in `Runtime.Run` plus signal handlers, so `SIGINT`/`SIGTERM` still remove the container.
- `--keep` (future, not v1) would skip removal for debugging.

## 9. Built-in configs

Shipped embedded in the binary (`configs/`, via `go:embed`) — pure-mise YAML configs (no Dockerfiles in v1):

1. **`opencode`** — `image:` the mise base image; `tools: { opencode: latest }`; `command: ["opencode"]`; mounts `~/.config/opencode` read-only, `~/.cache/opencode` read-write, `~/.gitconfig` read-only (targets use `~` so they resolve correctly in both Mode A and Mode B — see §5.6). Does **not** mount `~/.ssh` by default.
2. **`codex`** — `image:` the mise base image; `tools: { codex: latest }`; `command: ["codex"]` (or per codex's CLI); mounts its config dir + `~/.gitconfig` read-only. Same no-`~/.ssh` default.
3. **`shell`** — `image:` the mise base image; `command: ["sh"]`; no agent-specific tool or mounts. Useful as a base to `extends:` for new profiles, or as a disposable, project-aware shell with the correct tools on PATH.

(t3 code is deferred from v1 — it is not in mise's registry. It will be added when a mise registry entry exists, or via a `build:` config if a Dockerfile path is preferred.)

All built-ins declare `version: 1` and share the same mise base image. The exact base image reference, tool versions, and mount sets will be pinned at implementation time. The schema, merge semantics, `~` resolution, and behavior are fixed; only the per-profile values are pending.

## 10. Error handling

- **Config errors** (parse, merge-cycle, validation): printed with file path and line number where possible (YAML parser provides positions; merge errors reference the resolving config). Exit code 2.
- **Runtime errors** (image pull/build failure, container create/start failure, engine unreachable): printed with the underlying error and a hint where known (e.g. "is the Docker daemon running?"; "for Podman, start `podman system service`"). Exit code 3.
- **Profile exit code:** propagated as the `toolpod` exit code (exit 0 → 0, exit N → N). Distinguishes profile-command failure from toolpod failure (2/3) by reserving 2/3 for our own errors.
- **Signals:** `SIGINT`/`SIGTERM` forwarded to the container; `toolpod` waits for the container to exit, then removes it and exits with the container's exit code. `SIGWINCH` triggers `ContainerResize` (TTY only).
- **Fail-fast for missing prerequisites:** missing `mise` when `tools:` set, unreachable engine — all detected before we create the container, so we never start a container only to die inside it.

## 11. Testing

- **Unit tests** for config loading, merge semantics (including the `image`/`build` slot replace rule, null-to-delete, and cycle detection), validation, and `extends` resolution against a mock catalog.
- **Unit tests** for the workspace path-mirroring and user-mirror logic (pure functions over host metadata), and for rootless-Podman detection (Mode A) vs fallback (Mode B) selection.
- **Unit tests** for build dependency resolution: `depends_on` ordering (recursive), cycle detection/rejection, and the "image not found → hint" drift-detection error path.
- **Integration tests** for `Runtime` against a real Docker daemon (gated; skipped if `DOCKER_HOST` unavailable) covering: pull, build (from a user `build:` config, `--rebuild`), create with mounts/env/user, attach+run with TTY and non-TTY, exit-code propagation, signal forwarding, container removal on exit and on signal. Rootless-Podman-mode integration (gated on a rootless Podman socket being available) verifies Mode A workspace-at-host-path + host-user ownership. Fallback-mode integration verifies Mode B `/workspace` + root.
- **Integration smoke test** that actually launches the `shell` config (`command: ["sh", "-c", "echo hi"]`) and asserts exit 0 and that the workspace mount is RW. Mode-specific assertions: in Mode A, verify the workspace is at the host path and owned by the host user; in Mode B, verify it is at `/workspace` and owned by root.
- The runtime layer is behind an interface, so unit tests for orchestration (`pkg/toolpod.Launch`) use a fake `Runtime`.

## 12. Future (not v1)

- Instruction/prompt injection (`instructions:` field with append/replace modes, per-profile `instructions_path:`). Out of scope for v1.
- Generic `files:` block (write/overlay files into the container). Dropped; stacked mounts are mechanically viable but not worth v1 complexity.
- Persistent named containers (`--keep` / reuse).
- Kubernetes backend.
- GUI built on `pkg/toolpod`.
- System-package management alongside mise.
- Config expressions / derived values (Starlark/HCL/CUE were explored; v1 stays plain YAML).
- **Cross-platform UID remapping for Docker and rootful Podman** (devcontainer `updateRemoteUserUID` / distrobox entrypoint-injection approach) and macOS/Windows Docker-Desktop UID parity. v1 only mirrors on rootless Podman; other runtimes fall back to `/workspace` + root.