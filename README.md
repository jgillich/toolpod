# toolpod

> **Beta.** toolpod is early and currently only supports **Podman on Linux**. Docker and other platforms may work but are untested during this phase.

Disposable, reproducible development environments in a container, with a persistent [mise](https://mise.jdx.dev/) toolchain shared across runs.

`toolpod opencode` spins up a container, mounts your current directory, runs the agent, and removes the container on exit. The next run is instant — mise, your tools, and your caches are already warm in shared volumes.

## Why

Every developer eventually writes shell scripts or Makefiles that launch containers with the right mounts, SSH keys, caches, tool versions, and AI agents. Those scripts are project-specific, hard to share, and painful to maintain. toolpod replaces them with **user-owned, reusable profiles** that follow you to every project without requiring repo changes.

This is the key difference from [devcontainers](https://containers.dev/), which define a **project-owned** environment checked into the repo:

| | devcontainers | toolpod |
| --- | --- | --- |
| **Who owns it** | the project (`.devcontainer/`) | the user (`~/.config/toolpod/`) |
| **Requires repo changes** | yes | no |
| **Tool versions** | baked into the image or features | declared in profiles or your project, shared across runs |
| **Lifecycle** | long-lived, attach/reattach | ephemeral — fresh container each run, removed on exit |

The two are complementary. What toolpod adds:

- **mise as the foundation.** Tools (and the agents themselves) are `mise` entries declared in profiles, not image layers. One base image serves every profile; no per-language image, no rebuild when a tool version bumps. Per-project versions are optional via the project's own `mise.toml`.
- **Persistent shared volumes.** The mise install dir and package caches (npm, cargo, pip, go…) live in Docker named volumes shared across *all* profiles and runs. First launch of a tool is slow; every subsequent launch is instant.
- **Rootless-Podman parity.** When `DOCKER_HOST` points at a rootless Podman socket, the workspace is mounted at its host absolute path and the agent runs as your host user — paths and file ownership match exactly. No `sudo chown` cleanup.

AI coding agents are the flagship use case, but because the architecture is general, `toolpod shell` gives you a disposable, project-aware shell with the correct tools on PATH — useful even without an agent.

## A quick intro to mise

[mise](https://mise.jdx.dev/) is a polyglot version manager — and in toolpod, profiles are where tools get declared:

- A profile's `tools:` map lists what to install and which version; the agents themselves are entries too. No per-agent Dockerfile, no rebuild when a version bumps.
- mise installs each tool into a shared directory that toolpod mounts via a Docker named volume — install once, reuse across every profile and run.

Per-project versions are optional: if a project has its own `mise.toml`, toolpod's shell picks it up automatically as an override; otherwise the profile's `tools:` map stands alone. You don't need to be a mise expert to use either.

## Install

Using mise:

```
mise use -g github:jgillich/toolpod
```

Alternatively, build from source (requires Go):

```
go install github.com/jgillich/toolpod/cmd/toolpod@latest
```

For the best experience (correct file ownership and host-path parity), point `DOCKER_HOST` at a **rootless Podman** socket. Docker and rootful Podman also work, but the workspace is mounted at `/workspace` and files are written as root — see [Runtime modes](#runtime-modes).

## Basic usage

toolpod-owned flags come **before** the profile name; everything after is passed verbatim to the profile's command.

```sh
$ toolpod opencode --model foo    # → spin up, run `opencode --model foo`, remove on exit
$ toolpod shell                   # → disposable shell with the right tools on PATH
$ toolpod -c "make test" shell    # → one-off command
$ toolpod --workspace ~/p2 --verbose opencode --model foo
```

The first launch pulls the mise base image and installs tools (slow). Subsequent launches reuse them (instant).

### `toolpod init`

Profiles become useful once they carry *your* mounts and caches — SSH keys, git config, package caches. `toolpod init` generates a user profile override that merges a base profile and selected **fragments**:

```sh
$ toolpod init                                # interactive wizard (pick a profile or create "New")
$ toolpod init opencode --extends javascript,go,ssh
$ toolpod init myagent --extends opencode,podman,ruby,javascript,go
$ toolpod init opencode --extends javascript,github --dry-run
```

A name matching a built-in profile shadows it (`init opencode` extends the built-in `opencode`). Any other name creates a brand-new profile; by default it extends the shared `mise` base — pass `--extends` to start from any built-in or user profile, or leave it out and let the wizard or a later edit pick bases. `--extends` accepts profiles and fragments alike; run `toolpod init` to see the full list of available fragments.

### Other commands

```sh
toolpod doctor              # diagnose runtime, mise, volumes, configs, workspace
toolpod prune --volumes     # remove toolpod-managed named volumes
toolpod prune --images      # remove toolpod-tagged local images
toolpod prune --force --volumes   # skip the confirmation prompt
```

## Profiles

A profile is a plain YAML file. Built-in profiles are embedded in the binary; user profiles live in `~/.config/toolpod/profiles/` and shadow built-ins of the same name.

```yaml
# ~/.config/toolpod/profiles/myagent.yaml
version: 1
extends: opencode          # inherit everything, then override below
tools:
  opencode: "0.11.2"       # pin a version (overrides inherited "latest")
  node: "22"
mounts:
  ~/.ssh:
    source: ~/.ssh         # ~ → host $HOME (source) / runtime home (target)
caches:
  npm: ~/.npm
```

### Built-in profiles

| Profile | Command | What it is |
| --- | --- | --- |
| `opencode` | `opencode` | The opencode AI agent |
| `opencode-web` | `opencode web` | opencode web UI, published on a random host port |
| `codex` | `codex` | OpenAI Codex CLI |
| `claude` | `claude` | Anthropic Claude Code |
| `gemini` | `gemini` | Google Gemini CLI |
| `pi` | `pi` | Pi, the minimal terminal coding agent (earendil-works) |
| `crush` | `crush` | Crush, the Charmbracelet terminal coding agent |
| `qwen` | `qwen` | Qwen Code CLI (Alibaba) |
| `t3code` | `t3code` | T3 Code desktop app (AppImage) — agent harness control surface |
| `shell` | `bash` | Disposable, project-aware shell. Useful as a base to `extends:` |

All built-ins extend a shared `mise` base profile and install their agent as a `tools:` entry. None mount `~/.ssh` or `~/.gitconfig` by default — add those via `init` fragments or by hand.

### Schema reference

Every field is optional except `version`, `image`, and `command`.

| Field | Type | Description |
| --- | --- | --- |
| `version` | int | Config schema version. Currently `1`. |
| `extends` | string \| list | Inherit from another profile or fragment, then deep-merge. List form: `extends: [opencode, ssh, javascript]` (resolved left-to-right; body wins last). Cycles are rejected. Fragments may only extend other fragments, never profiles. |
| `image` | string | Container image. |
| `command` | string[] | Command to run. First element is the binary; the rest are default args used only when the user passes none on the CLI. User args replace the defaults. |
| `mounts` | map | Bind mounts, keyed by container target. `source`, `read_only` (default `true` — omit or set `read_only: false` for writable), `optional`, `create`. `~` → runtime home (target) / host `$HOME` (source). `{{ }}` template expressions evaluated against `.Env`, `uid`, and `trimPrefix`/`printf` helpers. `create: true` mkdirs a missing source directory before launch. |
| `caches` | map | Named-volume-backed cache dirs, shared across all profiles. |
| `tools` | map | mise-managed tools, keyed by name. Value is the version. Tools without a mise backend can use the built-in `appimage:` backend — `"appimage:pingdotgg/t3code": latest` downloads and extracts the stable GitHub-release AppImage (e.g. desktop apps). |
| `environment` | map | Env vars. Forward a host variable explicitly with a template, e.g. `DISPLAY: '{{ .Env.DISPLAY }}'`; a value that resolves to empty is left unset in the container. |
| `labels` | map | Container labels (`profile` is set automatically). |
| `network` | string | `bridge` (default), `host`, `none`, or a custom name. |
| `resources` | object | Optional hints: `{ memory, cpus }`. Best-effort. |
| `tty` | string | `auto` (default), `true`, or `false`. |
| `dbus` | object | Flatpak-style session-bus allowlist: `talk` / `own`, each a map of bus names to `true`. `talk` allows contacting those names, `own` allows registering them. Merged key-by-key across `extends` (set a name to `null` to clear an inherited entry). |

### ports / devices

`ports` publishes container ports to the host. The key is the container port;
`host` is optional (missing or `0` = random, auto-allocated host port).

```yaml
ports:
  8080: {}          # random host port -> 8080/tcp
  5432:
    host: 5432      # fixed host port
  53:
    protocol: udp
  5433:
    host: 5433
    host_ip: 127.0.0.1
```

Allocated (and fixed) host ports are available to `{{ }}` templates via
`.Ports` — keyed by container port — in `command` and `environment`:

```yaml
command: ["opencode", "web", "--port", "{{ index .Ports \"8080\" }}"]
environment:
  PORT: '{{ index .Ports "8080" }}'
```

Host environment variables are exposed to templates as `.Env` — use this to
forward a host value into the container (`'{{ .Env.FOO }}'`). A template that
resolves to an empty value leaves the variable unset.

`devices` attaches host device nodes into the container:

```yaml
devices:
  /dev/fuse: {}            # source defaults to the same path on the host
  /dev/nvidia0: { permissions: rw }
  /dev/bus/usb: { source: /dev/bus/usb, cgroup: true }
```

`source` defaults to the key path; `permissions` is `r`, `rw`, or `rwm`
(default `rwm`); `cgroup: true` additionally emits a device-cgroup allow-rule
scoped to the device's major:minor (advanced; leave off unless the container
needs to create arbitrary device nodes).

### Merge semantics

- **Scalars:** child replaces parent.
- **Maps** (`mounts`, `environment`, `tools`, `caches`, `labels`, `dbus`): merged key-by-key. Set a key to `null` to delete an inherited entry.
- **Lists** (`command`): replaced, not concatenated.
- **`image` / `build`:** single slot — setting either in a child clears the other.

A profile that declares `dbus` must also mount `$XDG_RUNTIME_DIR` (the `gui` fragment does) — the session bus address points at a proxy socket under that dir, so without the mount the container can't reach the bus.

In GUI containers the session bus is a **filtered view**, not a raw passthrough: toolpod spawns a per-launch host-side `xdg-dbus-proxy` and only the profile's `talk`/`own` names pass through it. The host system bus socket is not mounted into GUI containers at all.

### Inspecting profiles

```sh
$ toolpod profile show shell           # raw on-disk profile
$ toolpod profile show --resolved shell # fully merged with all extends inlined
$ toolpod profile list                 # every profile and fragment
$ toolpod profile edit myagent         # open in $EDITOR
```

## Fragments

Fragments are small, composable building blocks — a tool's cache, a host config mount, a credential set. `toolpod init` merges selected fragments into a user profile (via `--extends`, or the interactive wizard).

Built-in fragment mounts are `optional: true`, so missing host paths are skipped with a warning rather than failing the launch. Fragments may extend other fragments (e.g. `typescript` builds on `javascript`) but never profiles — a fragment can't pull in profile identity like `image` or `command`.

## Runtime modes

toolpod talks to any Docker-API-compatible engine via `DOCKER_HOST`:

- **Rootless Podman (recommended):** workspace mounted at its host absolute path, agent runs as your host user. Paths and file ownership match exactly.
- **Docker / rootful Podman:** workspace mounted at `/workspace`, agent runs as root. Files are root-owned on the host — clean up with `sudo chown`, or switch to rootless Podman.

`toolpod doctor` reports which mode is active.

## License

Licensed under the [Mozilla Public License Version 2.0](LICENSE). Copyright (c) 2026 Jakob Gillich.
