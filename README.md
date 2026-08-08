# tpd

<picture>
  <source media="(prefers-color-scheme: light)" srcset="./assets/banner-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/banner-dark.svg">
  <img alt="tpd disposable, reproducible development environments" src="./assets/banner-dark.svg">
</picture>

<br>

tpd is a CLI that runs your tools inside disposable containers. You define a profile — the tools, mounts, and caches you need — once. Each launch creates a fresh container, mounts your current directory, runs the profile's command, and removes the container when it exits. Tools are installed with [mise](https://mise.jdx.dev/) and stored in shared volumes, so installs and caches persist between runs.

> **Beta.** tpd is early and currently targets **rootless** containers on **Linux**. Rootful containers are supported on a best-effort basis. Profile and fragment configuration formats are not stable and may change between releases.

## Why

Every developer eventually writes scripts that launch containers with the right mounts, config files, caches, tool versions, and AI agents. tpd replaces them with **user-owned, reusable profiles** that follow you to every project without repo changes.

Unlike [devcontainers](https://containers.dev/) (project-owned, checked into the repo), tpd profiles are user-owned:

- Live in `~/.config/tpd/` — no repo changes needed.
- Declare tools via mise entries, not image layers — no rebuild when a version bumps.
- Fresh container each run, removed on exit; shared volumes keep installs and caches warm.

## Install

tpd is installed through [mise](https://mise.jdx.dev/), so install mise first:

```sh
curl https://mise.jdx.dev/install.sh | sh
```

This installs mise to `~/.local/bin` and wires up shell activation; restart your shell (or see the [installation docs](https://mise.jdx.dev/installing-mise.html)). Then install tpd:

```sh
mise use -g github:jgillich/tpd
```

Or build from source (requires Go):

```
go install github.com/jgillich/tpd/cmd/tpd@latest
```

Enable shell completions:

```sh
echo 'source <(tpd completion bash)' >> ~/.bashrc && source ~/.bashrc
```

tpd uses the Docker API, so configure `DOCKER_HOST` for the engine you want to use. For the recommended rootless Podman setup, start the user socket and point the client at it:

```sh
systemctl --user enable --now podman.socket
export DOCKER_HOST="unix://$XDG_RUNTIME_DIR/podman/podman.sock"
```

See [Runtime modes](#runtime-modes) for the differences between rootless and rootful engines.

## Basic usage

Run a profile with `tpd run <profile>`. For most profiles, you can drop the `run` and just type `tpd <profile>`:

```sh
$ tpd run opencode      # run the opencode agent
$ tpd bash              # shorthand: run a bash shell
```

The first launch pulls the base image, builds the profile's derived image when system packages are declared, and installs tools (slow). Subsequent launches reuse these resources when possible.

`tpd run --pull <profile>` re-pulls the base image even when it is already present locally, refreshing mutable tags (`latest`); the derived image is rebuilt automatically when the new base's ID changes its content hash.

### `tpd init`

`tpd init` generates a user profile that merges a base profile and selected **fragments** (SSH keys, git config, package caches).

![](assets/init.gif)

The generated file extends the chosen bases; when none of them provides a command, it defaults to `bash` so the profile runs. `tpd show --resolved <name>` prints the fully merged result, so you can see exactly what the container will get.

### `tpd edit`

`tpd edit <name>` opens the user file for a profile or fragment in `$EDITOR` (default `nano`). If no user file exists yet, tpd seeds one in `profiles/` (or `fragments/`): a stub that `extends:` the built-in. Settings you write are merged on top of the built-in per the [merge semantics](#merge-semantics), so change only what you need. Closing without saving removes the seed, leaving no shadow file behind.

### Other commands

```sh
tpd doctor              # diagnose runtime, mise, volumes, configs, workspace
tpd prune               # remove catalog-unused tpd resources (volumes, derived images, and the tpd-services network)
```

## Profiles

A profile is a YAML file. Built-ins are embedded in the binary; user profiles live in `~/.config/tpd/profiles/` and shadow built-ins of the same name.

```yaml
# ~/.config/tpd/profiles/myagent.yaml
version: 1
extends: opencode          # inherit everything, then override below
command: ["opencode", "--verbose"] # replaces the inherited command
tools:
  opencode: "0.11.2"       # pin a version (overrides inherited "latest")
  node: "22"
packages:
  - libxml2-dev            # apt packages installed in the derived image
mounts:
  ~/src/shared-lib:        # mount a directory into the container home
    source: ~/src/shared-lib
    read_only: true
caches:
  npm: ~/.npm
environment:
  OPENAI_API_KEY: '{{ .Env.OPENAI_API_KEY }}'   # forward a host variable
```

If a project has its own `mise.toml`, tpd's bash profile picks it up as an override; otherwise the profile's `tools:` map stands alone.

### Built-in profiles

The full catalog with source YAML for every built-in profile and fragment is in [docs/catalog.md](docs/catalog.md).

<!-- BEGIN tpd profiles -->

| Profile | What it is |
| --- | --- |
| [`amp`](internal/catalog/profiles/amp.yaml) | Sourcegraph Amp coding agent |
| [`bash`](internal/catalog/profiles/bash.yaml) | Disposable bash shell with shell completion |
| [`buzz`](internal/catalog/profiles/buzz.yaml) | Buzz, Block's desktop AI agent (GUI) |
| [`claude`](internal/catalog/profiles/claude.yaml) | Anthropic Claude Code |
| [`codewhale`](internal/catalog/profiles/codewhale.yaml) | CodeWhale, a terminal coding agent |
| [`codex`](internal/catalog/profiles/codex.yaml) | OpenAI Codex CLI |
| [`copilot`](internal/catalog/profiles/copilot.yaml) | GitHub Copilot CLI |
| [`crush`](internal/catalog/profiles/crush.yaml) | Crush, the Charmbracelet terminal coding agent |
| [`gemini`](internal/catalog/profiles/gemini.yaml) | Google Gemini CLI |
| [`goose`](internal/catalog/profiles/goose.yaml) | Goose, an extensible AI coding agent |
| [`mise`](internal/catalog/profiles/mise.yaml) | The mise toolchain base with common CLI tools |
| [`opencode`](internal/catalog/profiles/opencode.yaml) | The opencode AI agent |
| [`opencode-desktop`](internal/catalog/profiles/opencode-desktop.yaml) | The opencode desktop app (GUI) |
| [`pi`](internal/catalog/profiles/pi.yaml) | Pi, the minimal terminal coding agent |
| [`powershell`](internal/catalog/profiles/powershell.yaml) | Disposable PowerShell shell |
| [`qwen`](internal/catalog/profiles/qwen.yaml) | Qwen Code CLI (Alibaba) |
| [`t3code`](internal/catalog/profiles/t3code.yaml) | T3 Code desktop app — agent harness control surface |
| [`trivy`](internal/catalog/profiles/trivy.yaml) | Trivy vulnerability scanner |

<!-- END tpd profiles -->

Most agent built-ins extend the shared `mise` base profile and install their agent as a `tools:` entry. `mise` is the shared base and `bash` is the general-purpose shell profile.

### Schema reference

Every launchable profile needs `version`, `image`, and `command`. Fragments only need `version` and may omit the profile identity fields.

| Field | Type | Description |
| --- | --- | --- |
| `version` | int | Config schema version. Currently `1`. |
| `extends` | string \| list | Inherit from another profile or fragment, then deep-merge. Cycles are rejected; fragments may only extend fragments. |
| `image` | string | Container image. |
| `packages` | string[] | Apt packages installed in a derived image, built on first use and reused. |
| `repos` | map | Extra apt sources (`extrepo: <name>`), resolved at build time for the base image's Debian version. v1 supports extrepo entries; custom URL repositories are not yet buildable. |
| `files` | map | Files written into the container at launch, keyed by target path. Each entry: `content` (inline, `{{ }}` templates), `mode` (default `0644`). |
| `command` | string[] | Command to run. User args on the CLI replace the default args. |
| `mounts` | map | Bind mounts, keyed by container target. Host binds use `source`, `read_only` (default `true`), `optional`, `create`; service-socket mounts use `service` + `socket`. `~` in `source` → host `$HOME`; `~` as key → runtime home. |
| `services` | map | Companion daemon containers that start before the main container and stop after it, exposing sockets the main container mounts. See [Companion services](#companion-services-services). |
| `caches` | map | Named-volume-backed cache dirs, shared across all profiles. |
| `tools` | map | mise-managed tools, keyed by name; value is the version. `appimage:` tools stay on `latest` and are digest-verified at install (against GitHub's per-asset digest or a checksum sidecar); an explicit `sha256` or per-arch `sha256: {amd64, aarch64}` is optional. |
| `environment` | map | Env vars. Forward a host variable with `'{{ .Env.FOO }}'`. |
| `ports` | map | Publish container ports to the host. Key is the container port; `host` optional (`0` = random). Allocated host ports are available to templates via `.Ports`. |
| `devices` | map | Attach host device nodes (e.g. `/dev/fuse`). Optional `permissions` (`r`/`rw`/`rwm`) and `cgroup`. |
| `labels` | map | Container labels (`profile` is set automatically). |
| `network` | string | `bridge` (default), `host`, `none`, or a custom name. |
| `resources` | object | Optional resource limits: `{ memory, cpus }`, enforced as container resource limits (Docker `--memory`/`--cpus` semantics). |
| `tty` | string | `auto` (default), `true`, or `false`. |
| `meta` | object | Optional entry metadata: `{ description, tags }`. Describes the entry itself and is never inherited through `extends`; surfaced in `tpd list`, the init wizard, and the generated [catalog docs](docs/catalog.md). |
| `dbus` | object | Session-bus allowlist: `talk` / `own`, each a map of bus names. |

### Merge semantics

- **Scalars:** child replaces parent.
- **Maps:** merged key-by-key; set a key to `null` to delete an inherited entry.
- **`command`:** replaced, not concatenated.
- **`packages`:** additive with dedup; `packages: null` clears the inherited list.

### System packages (`packages:`)

The base image ships the bare OS. Per-profile system libraries are installed into a **derived image** (base + the profile's package list) that tpd builds on first use and reuses; profiles with identical lists share one image. Packages outside Debian's archive need a supported `repos:` entry:

```yaml
repos:
  mise:
    extrepo: mise # enables https://mise.jdx.dev/deb
```

Custom URL repositories are schema-ready but currently rejected during image preparation.

If your engine can't build images, use a custom `image:` that already includes mise and the required packages, and clear inherited package/repository declarations:

```yaml
image: my-image:latest
packages: null
repos: null
```

### Writing files at launch (`files:`)

`files:` writes inline-content files into the ephemeral container before the command runs — owned by the execution user, gone when the container exits. Targets are absolute or `~`-prefixed; content is a `{{ }}` template (`.Env`, `uid`, `.Ports`).

### Companion services (`services:`)

A profile can declare companion containers that run alongside the main one: they start before it, keep running while it does, and are stopped after it exits. Use them for background daemons your tools need — a package registry, a database, a proxy:

```yaml
services:
  registry:
    image: registry:2
    command: ["registry", "serve", "/etc/docker/registry/config.yml"]
    caches:
      registry-data: /var/lib/registry
    exposes:
      registry: /run/registry/registry.sock
mounts:
  /run/registry/registry.sock:
    service: registry
    socket: registry
```

A service is a mini-profile with its own `image`, `command`, `packages`/`repos`, `caches`, `mounts`, `environment`, `labels`, `files`, `exposes`, and optional `privileged`. Each `exposes:` entry declares a socket the service creates: tpd prepares the path on the host — `/run/user/<uid>/tpd-svc-<name>/` in rootless mode, `/tmp/tpd-svc-<name>-<uid>/` in rootful mode — and bind-mounts the parent directory into the service container, so the socket the daemon creates in the container appears on the host. The main profile then references it with `mounts:` keys that use `service: <name>` and `socket: <key>` instead of `source:`.

- Service containers never see your workspace.
- Every service joins the shared `tpd-services` bridge network under the stable alias `tpd-svc-<name>`; the main container reaches it via `TPD_SERVICE_<NAME>_HOST`.
- `network`, `tty`, `resources`, `tools`, `dbus`, `ports`, `devices`, `version`, `extends`, and nested `services` are rejected inside a service.
- Service caches share the same `tpd-cache-<name>` volumes as the main profile and other services.
- A running service is reused while its definition hash matches. If the definition changes, the old container is replaced — even under live consumers, who are named in a warning — accepting a brief outage so the new launch gets the updated service immediately.

### Inspecting profiles

```sh
$ tpd show bash            # profile definition before resolving extends
$ tpd list                  # every profile and fragment
$ tpd edit myagent          # open in $EDITOR
```

## Fragments

Fragments are small, composable building blocks — a tool's cache, a host config mount, or a credential set. `tpd init` merges selected fragments into a user profile. Built-in fragment mounts are `optional: true`, so missing host paths are skipped with a warning. Fragments may extend other fragments but never profiles.

User configuration lives below `$XDG_CONFIG_HOME/tpd/` (normally `~/.config/tpd/`): profiles are in `profiles/` and fragments are in `fragments/`. User profiles shadow built-ins with the same name; name collisions between profiles and fragments are errors.

Project-local `mise.toml` and `.tool-versions` files are discovered after tpd changes into the workspace, and override profile-level `tools:` for that launch.

## Security

Profiles are user-owned configuration, but they can grant substantial host access. Review mounts, forwarded environment variables, credential files, devices, published ports, GUI/D-Bus access, and container sockets before launching a profile. `files:` writes only into the ephemeral container; bind mounts and named caches can persist or expose host data.

Because built-in profiles can carry fields that tpd gates — mounts, devices, environment variables, ports, D-Bus access, `network`, and `services` — the first launch of a profile with any such field prompts you to approve or deny each item; choices are stored per profile and re-prompted only when the profile changes. Fields your own user profile declares are never gated.

GUI support is split into three capability fragments under `gui/`: `display` mounts the display, `/dev/dri`, and the specific Wayland socket; `portal` wires the filtered D-Bus to the desktop portal (and ships the `xdg-open` wrapper); `session` additionally mounts the entire `$XDG_RUNTIME_DIR` (needed by buzz/t3code). Prefer `display` (+ `portal` when the app opens URLs) unless the app needs the runtime dir.

Container engines are split the same way: `infra/docker` and `infra/podman` expose your host engine's socket to the profile (read-write — with great power...), while `service/podman` runs an isolated **nested** Podman engine as a service inside the container, with no host daemon access. The nested sidecar runs privileged (required: an unprivileged sidecar cannot run a nested engine — the kernel blocks the nested user namespace's `/proc` mount), but in a rootless engine that privilege stays inside the container's user namespace. Extend `service/podman` when you want a self-contained engine that persists between launches; extend `infra/podman`/`infra/docker` only when the profile genuinely needs your host containers.

See [the security model](docs/2026-08-03-security-model.md) for the trust model, ownership labels and prune semantics, the AppImage digest-verification policy, and the accepted trade-offs (extrepo TLS trust anchor, SELinux `label=disable`, setpriv-absent root fallback, host-port allocation).

## Runtime modes

tpd talks to a Docker-API-compatible engine via `DOCKER_HOST`:

- **Rootless Podman (recommended):** workspace is mounted at its host absolute path and the command runs as your host user. Paths and file ownership match exactly.
- **Docker / rootful Podman:** workspace is mounted at `/workspace`; tpd drops to the host UID when the image provides `setpriv`, with a root fallback when it does not. This mode cannot provide the same host-path parity as rootless Podman.

`tpd doctor` reports which mode is active.

Named caches are stored in engine-managed volumes and shared across profiles by cache name. On engines without volume-subpath support, tpd uses a separate fallback volume for each cache path.

## License

Licensed under the [Mozilla Public License Version 2.0](LICENSE). Copyright (c) 2026 Jakob Gillich.
