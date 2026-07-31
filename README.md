# toolpod

> **Beta.** toolpod is early and currently only supports **Podman on Linux**. Docker and other platforms may work but are untested during this phase.

Disposable, reproducible development environments in a container, with a persistent [mise](https://mise.jdx.dev/) toolchain shared across runs.

```
container  +  workspace  +  persistent mise  =  toolpod
```

`toolpod opencode` spins up a container, mounts your current directory, runs the agent, and removes the container on exit. The next run is instant — mise, your tools, and your caches are already warm in shared volumes.

## Why

Every developer eventually writes shell scripts or Makefiles that launch containers with the right mounts, SSH keys, caches, tool versions, and AI agents. Those scripts are project-specific, hard to share, and painful to maintain across many repositories. toolpod replaces those ad hoc wrappers with **user-owned, reusable profiles**.

This is a **user-owned** environment, not a project-owned one. toolpod describes how *you* prefer to work across all of your projects, without requiring changes to any repository. A maintainer should not have to merge your AI agent, your mise config, your SSH keys, and your caches into the project. toolpod keeps your environment yours; the project remains untouched.

AI coding agents are the flagship use case, but because the architecture is general, `toolpod shell` gives you a disposable, project-aware shell with the correct tools on PATH — useful even without an agent.

## How it compares to devcontainers

[Devcontainers](https://containers.dev/) and tools like the VS Code Dev Containers extension solve a related but different problem: they define a **project-owned** environment checked into the repo (`.devcontainer/devcontainer.json`, a Dockerfile, and often a feature list). Every contributor who opens the project gets the same environment.

| | devcontainers | toolpod |
| --- | --- | --- |
| **Who owns it** | the project (checked into the repo) | the user (`~/.config/toolpod/`) |
| **What it describes** | one project's environment | how *you* work across *all* projects |
| **Requires repo changes** | yes (`.devcontainer/`) | never — the project is untouched |
| **Tool versions** | baked into the image or features | per-project via `mise.toml` / `.tool-versions`, shared across runs |
| **Lifecycle** | long-lived, attach/reattach | ephemeral — fresh container each run, removed on exit |
| **Engine** | Docker (VS Code extension) | any Docker-API engine (Docker or Podman via `DOCKER_HOST`) |

The two are complementary. Devcontainers are the right answer when a team wants a shared, reviewed environment for a single repo. toolpod is the right answer when *you* want your agents, your SSH keys, your caches, and your toolchain to follow you to every project without touching any of them. A project can have a `.devcontainer` for CI and new contributors, while you run `toolpod opencode` against the same checkout for your daily work.

What toolpod adds that devcontainers don't have:

- **mise as the foundation.** Tools (and the agents themselves) are `mise` entries, not image layers. One base image serves every project; per-project tool versions come from the project's own `mise.toml`, which mise auto-detects. No per-language image, no rebuild when a tool version bumps.
- **Persistent shared volumes.** The mise install dir and package caches (npm, cargo, pip, go…) live in Docker named volumes shared across *all* profiles and runs. First launch of a tool is slow; every subsequent launch is instant.
- **Rootless-Podman parity.** When `DOCKER_HOST` points at a rootless Podman socket, the workspace is mounted at its host absolute path and the agent runs as your host user — paths and file ownership match exactly. No `sudo chown` cleanup.

## A quick intro to mise

[mise](https://mise.jdx.dev/) (formerly rtx) is a polyglot version manager — the modern, fast successor to asdf. If you've never used it, the two ideas that matter for toolpod are:

1. **Tools are declared, not baked in.** A project lists what it needs in a `mise.toml` or `.tool-versions` file in its root:
   ```toml
   # mise.toml
   [tools]
   node = "22"
   python = "3.13"
   rust = "1.90"
   ```
   When a shell enters that directory, mise puts those exact versions on `PATH`. Different projects get different toolchains automatically, with no image rebuild.

2. **Tools install to a shared directory.** mise installs each tool once (e.g. `~/.local/share/mise/installs/node/22.x/`) and creates shims that point at the active version. The install is reused across every project that asks for the same version.

toolpod mounts that shared install directory in a Docker named volume, so the same property holds across container runs: install once, reuse forever. The AI agents themselves (`opencode`, `codex`, `claude-code`, `gemini`) are also in mise's registry, so they're installed the same way as `node` or `rust` — no per-agent Dockerfile.

You don't need to be a mise expert to use toolpod. If a project has a `mise.toml`, toolpod's shell picks up its tools automatically. If it doesn't, your profile's `tools:` map provides the baseline.

## Install

Requires Go 1.25+ and a Docker-compatible container engine (Docker, or Podman via `podman system service`).

```
make install   # go install ./cmd/toolpod
```

For the best experience (correct file ownership and host-path parity), point `DOCKER_HOST` at a **rootless Podman** socket. Docker and rootful Podman also work, but the workspace is mounted at `/workspace` and files are written as root — see [Runtime modes](#runtime-modes).

## Basic usage

toolpod-owned flags come **before** the profile name; everything after the profile name is passed verbatim to the profile's command. So agent flags like `--model foo` work with no escaping.

```sh
$ cd ~/projects/myapp

# Launch a built-in profile. Args after the profile name pass through verbatim.
$ toolpod opencode --model foo
# → spins up a container, mounts $PWD, runs `opencode --model foo`, removes on exit

# A disposable shell with the right tools on PATH.
$ toolpod shell

# Run a one-off command in the shell profile.
$ toolpod -c "make test" shell

# toolpod flags before the profile name; agent args after.
$ toolpod --workspace ~/p2 --verbose opencode --model foo
```

The first launch of a profile pulls the mise base image and installs tools into the shared volume (slow). Every subsequent launch reuses them (instant).

### `toolpod init`

Profiles become useful once they carry *your* mounts and caches — your SSH keys, your git config, the package caches for the languages you use. `toolpod init` generates a user profile override that extends a built-in and merges in selected **fragments** (pass `--presets` as an alias for `--fragments`):

```sh
# Interactive wizard (prompts for profile + fragments).
$ toolpod init

# Non-interactive: extend the opencode built-in, add fragments.
$ toolpod init opencode --fragments npm,go,gitconfig,ssh

# Preview the generated file without writing it.
$ toolpod init opencode --fragments npm,gh --dry-run
```

This writes `~/.config/toolpod/profiles/opencode.yaml`, which shadows the built-in `opencode` profile. The built-in provides the image, command, and agent tool; your file adds the mounts and caches you selected. Remove the file to restore the built-in default.

### Other commands

```sh
toolpod doctor              # diagnose runtime, mise, volumes, configs, workspace
toolpod prune --volumes     # remove toolpod-managed named volumes
toolpod prune --images      # remove toolpod-tagged local images
toolpod prune --force --volumes   # skip the confirmation prompt
```

`doctor` is informational, not a launch gate — it surfaces misconfiguration before a confusing launch failure. Run it once after install to verify your setup.

## Profiles

A profile is a plain YAML file. The file name (minus `.yaml`) is the profile name used on the CLI. Built-in profiles are embedded in the binary; user profiles live in `~/.config/toolpod/profiles/` and shadow built-ins of the same name.

```yaml
# ~/.config/toolpod/profiles/myagent.yaml
version: 1
extends: opencode          # inherit everything, then override below
tools:
  opencode: "0.11.2"       # pin a version (overrides inherited "latest")
  node: "22"
mounts:
  ~/.ssh:                  # ~ target → runtime user's home
    source: ~/.ssh         # ~ source → host $HOME
    read_only: true
  ~/.config/myagent:
    source: ~/.config/myagent
    read_only: false
caches:
  npm: ~/.npm
```

### Built-in profiles

| Profile | Command | What it is |
| --- | --- | --- |
| `opencode` | `opencode` | The opencode AI agent. Mounts `~/.config/opencode` (ro), `~/.cache/opencode` (rw), `~/.local/share/opencode` (rw). |
| `codex` | `codex` | OpenAI's Codex CLI. Mounts `~/.codex` (rw) for config, sessions, and auth. |
| `claude` | `claude` | Anthropic's Claude Code. Mounts `~/.claude` (rw) and `~/.cache/claude-code` (rw) so sessions and resume persist. |
| `gemini` | `gemini` | Google's Gemini CLI. Mounts `~/.gemini` (rw) so checkpoints and OAuth tokens persist. |
| `shell` | `bash` | A disposable, project-aware shell. No agent-specific tools or mounts. Useful as a base to `extends:`. |

All built-ins extend a shared `mise` base profile (`image: ghcr.io/jdx/mise:latest`) and install their agent as a `tools:` entry. None mount `~/.ssh` or `~/.gitconfig` by default — add those via `init` fragments or by hand.

### Schema reference

Every field is optional except `version` and `command` (required in the resolved profile).

| Field | Type | Description |
| --- | --- | --- |
| `version` | int | Config schema version. Currently `1`. |
| `extends` | string \| list | Inherit from another profile or fragment (built-in or user), then deep-merge. Accepts a single name (`extends: opencode`) for backward compatibility, or a list (`extends: [opencode, ssh, npm]`). Entries are resolved depth-first and merged left-to-right; the profile body always wins last. Cycles are rejected. |
| `image` | string | Container image to use. Mutually exclusive with `build`. |
| `build` | object | Escape hatch: `{ dockerfile, context, depends_on }` to build a custom image. |
| `command` | string[] | The command to run. CLI args are appended verbatim. |
| `args_if_none` | string[] | Default args used only if the user passes none. |
| `mounts` | map | Bind mounts, keyed by container target. Each entry has `source`, `read_only`, and `optional` (if true, the mount is skipped when the source doesn't exist). `~` in target → runtime home, in source → host `$HOME`. `{{ }}` template expressions are evaluated against the host environment via `.Env` (e.g. `{{ or (index .Env "DOCKER_HOST") "/var/run/docker.sock" }}`), `uid` (host user ID), and `trimPrefix`/`printf` helpers. |
| `caches` | map | Named-volume-backed cache dirs, keyed by cache name. Shared across all profiles. |
| `tools` | map | mise-managed tools to ensure installed, keyed by name. Value is the version. |
| `environment` | map | Env vars. Empty string = passthrough from host; literal = set. |
| `labels` | map | Container labels (informational; `profile` is set automatically). |
| `network` | string | Network mode: `bridge` (default), `host`, `none`, or a custom name. |
| `resources` | object | Optional hints: `{ memory, cpus }`. Best-effort. |
| `tty` | string | `auto` (default), `true`, or `false`. |

### Inheritance and merge semantics

- **Scalars:** child replaces parent.
- **Maps** (`mounts`, `environment`, `tools`, `caches`, `labels`): merged key-by-key. A child key overrides that key only. Set a key to `null` in a child to *delete* an inherited entry without redeclaring the whole map.
- **Lists** (`command`, `args_if_none`): replaced, not concatenated.
- **`image` / `build`:** treated as a single slot — setting either in a child clears the other from the parent.

`extends` accepts a single string (backward compatible) or a list:

```yaml
extends: [opencode, ssh, npm]   # resolved left-to-right; body wins last
```

All extends entries (and fragments) are composable building blocks: a profile inherits from one or more of them and then applies its own body on top. This lets you extend a built-in and change one mount or one tool version without redeclaring everything.

### Inspecting profiles

`profile show`, `profile edit`, and `profile list` let you inspect what toolpod resolves for a profile without launching a container.

```sh
# Print the raw (on-disk) profile, exactly as written.
$ toolpod profile show shell

# Print the fully merged profile, with all extends inlined.
$ toolpod profile show --resolved shell

# List every profile and fragment, labeled built-in / user / shadow / fragment.
$ toolpod profile list

# Open your user profile file in $EDITOR (creates an override for a built-in
# via `toolpod init <name>`).
$ toolpod profile edit myagent
```

`profile show --resolved` walks the extends chain depth-first (see [Inheritance and merge semantics](#inheritance-and-merge-semantics)) and prints the merged result — handy for debugging why a mount or tool isn't what you expect.

## Fragments

**Fragments are small, composable building blocks representing a single concern** — one tool's cache, one host config mount, or one credential set. Each fragment is a self-contained piece of a profile (mounts, caches, tools, env, labels) with no `extends`/`image`/`command` of its own. They live in the catalog alongside profiles, under globally unique names, and are merged into a user profile by `toolpod init`.

```sh
$ toolpod init opencode --fragments npm,go,gitconfig,ssh   # --presets is accepted as an alias
```

### Available fragments

**Package caches** (tool + shared cache volume):

| Preset | Tool | Cache |
| --- | --- | --- |
| `npm` | `node` | `~/.npm` |
| `bun` | `bun` | `~/.bun/install/global` |
| `cargo` | `rust` | `~/.cargo` |
| `deno` | `deno` | `~/.cache/deno` |
| `go` | `go` | `~/go` |
| `pip` | `python` | `~/.cache/pip` |
| `ruby` | `ruby` | `~/.gem` |
| `php` | `php` | `~/.composer/cache` |
| `java` | `java` | `~/.m2` |
| `helm` | `helm` | `~/.cache/helm` |
| `terraform` | `terraform` | `~/.terraform.d/plugin-cache` |

**Host config / credentials** (mount + tool):

| Preset | Tool | Mounts |
| --- | --- | --- |
| `aws` | `aws` | `~/.aws` (ro) |
| `gh` | `gh` | `~/.config/gh` (rw — auth tokens refresh) |
| `gcloud` | `gcloud` | `~/.config/gcloud` (rw — tokens refresh) |
| `az` | `az` | `~/.azure` (rw — MSAL token cache) |
| `kubectl` | `kubectl` | `~/.kube` (ro) |
| `gitconfig` | — | `~/.gitconfig` (ro) |
| `netrc` | — | `~/.netrc` (ro) |
| `ssh` | — | `~/.ssh` (ro) + `~/.ssh/known_hosts` (rw) |
| `docker` | `docker-cli` | `{{ or (trimPrefix (index .Env "DOCKER_HOST") "unix://") "/var/run/docker.sock" }}` (rw — host Docker socket) |
| `podman` | `podman` | `{{ or (trimPrefix (index .Env "DOCKER_HOST") "unix://") (printf "/run/user/%s/podman/podman.sock" (uid)) }}` (rw — host Podman socket) |

All fragment mounts are marked `optional: true` — if the source path doesn't exist on the host, the mount is silently skipped (with a stderr warning) rather than failing the launch. User-authored mounts default to required.

There is no `all` shorthand — name fragments explicitly. You can add your own fragments by dropping a YAML fragment into the catalog, but the common case is to let `init` handle it.

## Runtime modes

toolpod talks to any Docker-API-compatible engine via `DOCKER_HOST`. The workspace mount and user differ by mode:

- **Mode A (rootless Podman):** `DOCKER_HOST` points at a rootless Podman socket. The workspace is mounted at its **host absolute path** (e.g. `/home/me/projects/myapp`), and the agent runs as your host user. Paths the agent shows you match host paths exactly; file ownership is correct. This is the recommended setup.
- **Mode B (Docker, rootful Podman):** any other engine. The workspace is mounted at `/workspace`, and the agent runs as root. Paths don't match host paths (you must mentally translate), and files written to the workspace are root-owned on the host. Clean up with `sudo chown`, or switch to rootless Podman.

`toolpod doctor` reports which mode is active. Tilde (`~/`) expansion in profiles resolves to the runtime user's home in both modes, so built-in profiles are portable without per-mode duplication.

## Project layout

```
toolpod/
  cmd/toolpod/        # thin CLI: arg parsing, stdio, exit codes
  internal/
    catalog/          # embedded built-in profiles + fragments (go:embed)
    profile/          # profile loading, extends merge, validation
    runtime/          # Docker Engine SDK: Prepare (image+tools) + Run (container+attach)
    mise/             # mise integration: ensure-tools, shared volume mgmt
    build/            # on-demand image build, depends_on resolution
    scaffold/         # `toolpod init`: fragment selection + profile generation
    doctor/           # environment diagnostics
    prune/            # volume/image cleanup
    ui/               # CLI output (TTY-aware)
  pkg/toolpod/        # public library: Launch(opts) -> Result
```

The CLI is a thin wrapper over `pkg/toolpod.Launch`, which orchestrates: resolve profile → `Prepare` (pull image, ensure volumes, install tools) → `Run` (create container, attach stdio, forward signals, wait, remove). A future GUI or other tooling can import `pkg/toolpod` with a custom progress writer and programmatic stdio.

## License

See the repository for license information.