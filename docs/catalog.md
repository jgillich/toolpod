# Catalog

The built-in profiles and fragments shipped in the tpd binary. Run `make docs` to regenerate this file and the README profiles table.

## Contents

- [Profiles](#profiles)
  - [amp](#amp)
  - [bash](#bash)
  - [buzz](#buzz)
  - [claude](#claude)
  - [codewhale](#codewhale)
  - [codex](#codex)
  - [copilot](#copilot)
  - [crush](#crush)
  - [gemini](#gemini)
  - [goose](#goose)
  - [mise](#mise)
  - [opencode](#opencode)
  - [opencode-desktop](#opencode-desktop)
  - [pi](#pi)
  - [powershell](#powershell)
  - [qwen](#qwen)
  - [t3code](#t3code)
  - [trivy](#trivy)
- [Fragments](#fragments)
  - [Other](#other)
    - [defaults](#defaults)
  - [cloud](#cloud)
    - [cloud/aws](#cloudaws)
    - [cloud/azure](#cloudazure)
    - [cloud/gcloud](#cloudgcloud)
    - [cloud/helm](#cloudhelm)
    - [cloud/kubernetes](#cloudkubernetes)
    - [cloud/terraform](#cloudterraform)
  - [gui](#gui)
    - [gui/display](#guidisplay)
    - [gui/portal](#guiportal)
    - [gui/session](#guisession)
  - [service](#service)
    - [service/podman](#servicepodman)
  - [sysutils](#sysutils)
    - [sysutils/docker](#sysutilsdocker)
    - [sysutils/nix](#sysutilsnix)
    - [sysutils/podman](#sysutilspodman)
    - [sysutils/ssh](#sysutilsssh)
    - [sysutils/vault](#sysutilsvault)
  - [toolchain](#toolchain)
    - [toolchain/android](#toolchainandroid)
    - [toolchain/bash](#toolchainbash)
    - [toolchain/c](#toolchainc)
    - [toolchain/cpp](#toolchaincpp)
    - [toolchain/dotnet](#toolchaindotnet)
    - [toolchain/elixir](#toolchainelixir)
    - [toolchain/go](#toolchaingo)
    - [toolchain/haskell](#toolchainhaskell)
    - [toolchain/java](#toolchainjava)
    - [toolchain/javascript](#toolchainjavascript)
    - [toolchain/julia](#toolchainjulia)
    - [toolchain/kotlin](#toolchainkotlin)
    - [toolchain/ocaml](#toolchainocaml)
    - [toolchain/perl](#toolchainperl)
    - [toolchain/php](#toolchainphp)
    - [toolchain/python](#toolchainpython)
    - [toolchain/ruby](#toolchainruby)
    - [toolchain/rust](#toolchainrust)
    - [toolchain/scala](#toolchainscala)
    - [toolchain/typescript](#toolchaintypescript)
    - [toolchain/zig](#toolchainzig)
  - [vcs](#vcs)
    - [vcs/git](#vcsgit)
    - [vcs/github](#vcsgithub)
    - [vcs/gitlab](#vcsgitlab)

## Profiles

### `amp`

Sourcegraph Amp coding agent

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Sourcegraph Amp coding agent
extends:
  - defaults
  - mise
command: ["amp"]
tools:
  amp: latest
mounts:
  ~/.config/amp:
    source: ~/.config/amp
    create: true
    read_only: false
    optional: true
```

</details>

### `bash`

Disposable bash shell with shell completion

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Disposable bash shell with shell completion
extends:
  - defaults
  - mise
  - toolchain/bash
command: ["bash", "-l"]
```

</details>

### `buzz`

Buzz, Block's desktop AI agent (GUI)

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Buzz, Block's desktop AI agent (GUI)
extends:
  - defaults
  - mise
  - gui/display
  - gui/portal
  - gui/session
  - codex
  - claude
command: ["buzz"]
# Host networking is required: buzz's auth flow opens a callback URL on a
# random host port that a published port cannot predict or map.
network: host
tools:
  "appimage:block/buzz": latest
mounts:
  ~/.local/share/xyz.block.buzz.app:
    source: ~/.local/share/xyz.block.buzz.app
    create: true
    read_only: false
  ~/.config/xyz.block.buzz.app:
    source: ~/.config/xyz.block.buzz.app
    create: true
    read_only: false
  ~/.buzz:
    source: ~/.buzz
    create: true
    read_only: false
dbus:
  talk:
    org.freedesktop.Notifications: {}
  own:
    xyz.block.buzz.app: {}
```

</details>

### `claude`

Anthropic Claude Code

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Anthropic Claude Code
extends:
  - defaults
  - mise
command: ["claude"]
tools:
  claude-code: latest
mounts:
  ~/.claude:
    source: ~/.claude
    create: true
    read_only: false
  ~/.cache/claude-code:
    source: ~/.cache/claude-code
    create: true
    read_only: false
```

</details>

### `codewhale`

CodeWhale, a terminal coding agent

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: CodeWhale, a terminal coding agent
extends:
  - defaults
  - mise
command: ["codewhale"]
tools:
  github:Hmbown/CodeWhale: latest
mounts:
  ~/.codewhale:
    source: ~/.codewhale
    create: true
    read_only: false
```

</details>

### `codex`

OpenAI Codex CLI

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: OpenAI Codex CLI
extends:
  - defaults
  - mise
command: ["codex"]
tools:
  codex: latest
mounts:
  ~/.codex:
    source: ~/.codex
    create: true
    read_only: false
```

</details>

### `copilot`

GitHub Copilot CLI

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: GitHub Copilot CLI
extends:
  - defaults
  - mise
command: ["copilot"]
tools:
  copilot: latest
mounts:
  ~/.copilot:
    source: ~/.copilot
    create: true
    read_only: false
    optional: true
```

</details>

### `crush`

Crush, the Charmbracelet terminal coding agent

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Crush, the Charmbracelet terminal coding agent
extends:
  - defaults
  - mise
command: ["crush"]
tools:
  crush: latest
mounts:
  ~/.config/crush:
    source: ~/.config/crush
    create: true
  ~/.local/share/crush:
    source: ~/.local/share/crush
    create: true
    read_only: false
```

</details>

### `gemini`

Google Gemini CLI

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Google Gemini CLI
extends:
  - defaults
  - mise
command: ["gemini"]
tools:
  gemini: latest
mounts:
  ~/.gemini:
    source: ~/.gemini
    create: true
    read_only: false
```

</details>

### `goose`

Goose, an extensible AI coding agent

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Goose, an extensible AI coding agent
extends:
  - defaults
  - mise
command: ["goose"]
tools:
  aqua:aaif-goose/goose: latest
mounts:
  ~/.config/goose:
    source: ~/.config/goose
    create: true
    read_only: false
  ~/.local/share/goose:
    source: ~/.local/share/goose
    create: true
    read_only: false
  ~/.cache/goose:
    source: ~/.cache/goose
    create: true
    read_only: false
  ~/.agents:
    source: ~/.agents
    create: true
    read_only: false
```

</details>

### `mise`

The mise toolchain base

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: The mise toolchain base
extends: defaults
image: debian:13-slim
command: ["/usr/bin/mise"]
repos:
  mise:
    extrepo: mise
packages:
  - mise
mounts:
  /etc/mise:
    source: ~/.config/mise
    optional: true
caches:
  mise:
    - ~/.local/share/mise
    - ~/.cache/mise
    - ~/.aube
files:
  /etc/profile.d/mise.sh:
    content: |
      if command -v mise >/dev/null 2>&1; then
        eval "$(mise hook-env)"
      fi
```

</details>

### `opencode`

The opencode AI agent

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: The opencode AI agent
extends:
  - defaults
  - mise
command: ["opencode"]
tools:
  opencode: latest
mounts:
  ~/.config/opencode:
    source: ~/.config/opencode
    create: true
  ~/.cache/opencode:
    source: ~/.cache/opencode
    create: true
    read_only: false
  ~/.local/share/opencode:
    source: ~/.local/share/opencode
    create: true
    read_only: false
```

</details>

### `opencode-desktop`

The opencode desktop app (GUI)

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: The opencode desktop app (GUI)
extends:
  - defaults
  - mise
  - gui/display
  - gui/portal
  - gui/session
command: ["opencode", "--no-sandbox", "--disable-dev-shm-usage", "--ozone-platform=wayland"]
tools:
  "appimage:anomalyco/opencode": latest
mounts:
  ~/.config/opencode:
    source: ~/.config/opencode
    create: true
    read_only: false
  ~/.cache/opencode:
    source: ~/.cache/opencode
    create: true
    read_only: false
  ~/.local/share/opencode:
    source: ~/.local/share/opencode
    create: true
    read_only: false
  ~/.config/ai.opencode.desktop:
    source: ~/.config/ai.opencode.desktop
    create: true
    read_only: false
```

</details>

### `pi`

Pi, the minimal terminal coding agent

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Pi, the minimal terminal coding agent
extends:
  - defaults
  - mise
command: ["pi"]
tools:
  pi: latest
mounts:
  ~/.pi:
    source: ~/.pi
    create: true
    read_only: false
```

</details>

### `powershell`

Disposable PowerShell shell

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Disposable PowerShell shell
extends:
  - defaults
  - mise
command: ["pwsh"]
packages:
  - libicu76
mounts:
  ~/.config/powershell:
    source: ~/.config/powershell
    create: true
    read_only: false
    optional: true
  ~/.local/share/powershell:
    source: ~/.local/share/powershell
    create: true
    read_only: false
    optional: true
tools:
  powershell-core: latest
```

</details>

### `qwen`

Qwen Code CLI (Alibaba)

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Qwen Code CLI (Alibaba)
extends:
  - defaults
  - mise
command: ["qwen"]
tools:
  qwen: latest
mounts:
  ~/.qwen:
    source: ~/.qwen
    create: true
    read_only: false
```

</details>

### `t3code`

T3 Code desktop app — agent harness control surface

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: T3 Code desktop app — agent harness control surface
extends:
  - defaults
  - mise
  - gui/display
  - gui/portal
  - gui/session
command: ["t3code", "--no-sandbox", "--disable-dev-shm-usage", "--ozone-platform=wayland"]
tools:
  "appimage:pingdotgg/t3code": latest
mounts:
  ~/.t3:
    source: ~/.t3
    create: true
    read_only: false
  ~/.config/t3code:
    source: ~/.config/t3code
    create: true
    read_only: false
environment:
  ELECTRON_ENABLE_LOGGING: "1"
```

</details>

### `trivy`

Trivy vulnerability scanner

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Trivy vulnerability scanner
extends:
  - defaults
  - mise
command: ["trivy"]
caches:
  trivy: ~/.cache/trivy
mounts:
  ~/.config/trivy:
    source: ~/.config/trivy
    create: true
    read_only: false
    optional: true
tools:
  trivy: latest
```

</details>

## Fragments

### Other

### `defaults`

Default launch policies with the common dev toolchain and a memory cap

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Default launch policies with the common dev toolchain and a memory cap
packages:
  - autoconf
  - bsdextrautils
  - file
  - curl
  - git
  - build-essential
  - cmake
  - python3
  - libssl-dev
  - libcurl4-openssl-dev
  - zlib1g-dev
  - libreadline-dev
  - libffi-dev
  - libsqlite3-dev
  - gettext
  - openssl
  - gdb
  - strace
tools:
  fd: latest
  git-lfs: latest
  jq: latest
  ripgrep: latest
  yq: latest
resources:
  memory: "{{ div .MemBytes 2 }}"
```

</details>

### cloud

### `cloud/aws`

AWS CLI

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: AWS CLI
tools:
  aws: latest
mounts:
  ~/.aws:
    source: ~/.aws
    optional: true
```

</details>

### `cloud/azure`

Azure CLI

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Azure CLI
tools:
  azure: latest
mounts:
  ~/.azure:
    source: ~/.azure
    create: true
    read_only: false
    optional: true
```

</details>

### `cloud/gcloud`

Google Cloud CLI

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Google Cloud CLI
tools:
  gcloud: latest
mounts:
  ~/.config/gcloud:
    source: ~/.config/gcloud
    create: true
    read_only: false
    optional: true
```

</details>

### `cloud/helm`

Helm with chart cache

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Helm with chart cache
tools:
  helm: latest
caches:
  helm: ~/.cache/helm
mounts:
  ~/.config/helm:
    source: ~/.config/helm
    create: true
    read_only: false
    optional: true
```

</details>

### `cloud/kubernetes`

kubectl, k9s, kustomize and kubectx

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: kubectl, k9s, kustomize and kubectx
tools:
  kubectl: latest
  k9s: latest
  kustomize: latest
  kubectx: latest
  kubens: latest
mounts:
  ~/.kube:
    source: ~/.kube
    optional: true
  ~/.config/k9s:
    source: ~/.config/k9s
    create: true
    read_only: false
    optional: true
```

</details>

### `cloud/terraform`

Terraform with plugin cache

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Terraform with plugin cache
tools:
  terraform: latest
caches:
  terraform: ~/.terraform.d/plugin-cache
mounts:
  ~/.terraformrc:
    source: ~/.terraformrc
    optional: true
```

</details>

### gui

### `gui/display`

Display, GPU and Wayland/X11 mounts

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Display, GPU and Wayland/X11 mounts
packages:
  - libatk-bridge2.0-0
  - libatk1.0-0
  - libatspi2.0-0
  - libcairo2
  - libcups2
  - libegl1
  - libfontconfig1
  - libgbm1
  - libgl1
  - libgles2
  - libgtk-3-0
  - libnss3
  - libnspr4
  - libpango-1.0-0
  - libx11-6
  - libx11-xcb1
  - libxcb1
  - libxcomposite1
  - libxcursor1
  - libxdamage1
  - libxext6
  - libxfixes3
  - libxkbcommon0
  - libxi6
  - libxrandr2
  - libxrender1
  - libxss1
  - libxtst6
  - libgstreamer1.0-0
  - libgstreamer-plugins-base1.0-0
  - gstreamer1.0-plugins-base
  - gstreamer1.0-plugins-good
  - gstreamer1.0-plugins-bad
  - gstreamer1.0-libav
  - gstreamer1.0-x
devices:
  /dev/dri: {}
mounts:
  /tmp/.X11-unix:
    source: /tmp/.X11-unix
    optional: true
  '{{ if and .Env.XDG_RUNTIME_DIR .Env.WAYLAND_DISPLAY }}{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}{{ end }}':
    source: '{{ if and .Env.XDG_RUNTIME_DIR .Env.WAYLAND_DISPLAY }}{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}{{ end }}'
    optional: true
environment:
  WAYLAND_DISPLAY: '{{ .Env.WAYLAND_DISPLAY }}'
  DISPLAY: '{{ .Env.DISPLAY }}'
```

</details>

### `gui/portal`

XDG desktop portal and dbus integration

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: XDG desktop portal and dbus integration
packages:
  - dbus
  - libdbus-1-3
  - xdg-utils
dbus:
  talk:
    org.freedesktop.portal.Desktop: {}
files:
  /usr/local/bin/xdg-open:
    content: |
      #!/bin/sh
      # Open URLs in the host browser by forwarding to the XDG desktop portal on the
      # session bus (via the launch's filtered dbus proxy); fall back to the real
      # xdg-open otherwise.
      portal_open() {
        timeout 10 env LD_LIBRARY_PATH= DBUS_SESSION_BUS_ADDRESS="$1" /usr/bin/dbus-send --session --print-reply \
          --dest=org.freedesktop.portal.Desktop /org/freedesktop/portal/desktop \
          org.freedesktop.portal.OpenURI.OpenURI \
          string:'' "string:$2" dict:string:variant: >/dev/null 2>&1
      }

      case "$1" in
        [a-zA-Z][a-zA-Z0-9+.-]*:*)
          for a in "${DBUS_SESSION_BUS_ADDRESS:-}" "unix:path=${XDG_RUNTIME_DIR:-/nonexistent}/bus"; do
            [ -n "$a" ] || continue
            case "$a" in
              unix:path=*)
                p="${a#unix:path=}"
                p="${p%%,*}"
                [ -S "$p" ] || continue
                ;;
            esac
            if portal_open "$a" "$1"; then
              exit 0
            fi
          done
          ;;
      esac
      case "$0" in
        */*) self=$(readlink -f "$0") ;;
        *) self=$(command -v "$0") ;;
      esac
      if [ -x "${self%/*}/xdg-open.real" ]; then
        exec "${self%/*}/xdg-open.real" "$@"
      fi
      exec /usr/bin/xdg-open "$@"
    mode: 0755
```

</details>

### `gui/session`

Full XDG_RUNTIME_DIR session mount (GUI)

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Full XDG_RUNTIME_DIR session mount (GUI)
packages:
  - libasound2
  - gstreamer1.0-alsa
  - gstreamer1.0-pulseaudio
mounts:
  '{{ .Env.XDG_RUNTIME_DIR }}':
    source: '{{ .Env.XDG_RUNTIME_DIR }}'
    optional: true
environment:
  XDG_RUNTIME_DIR: '{{ .Env.XDG_RUNTIME_DIR }}'
```

</details>

### service

### `service/podman`

Isolated nested podman engine as a service

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Isolated nested podman engine as a service
tools:
  docker-cli: latest
  podman: latest
  docker-compose: latest
  hadolint: latest
services:
  podman:
    image: debian:13-slim
    packages:
      - podman
      - aardvark-dns
      - nftables
      - iptables
      - iproute2
      - catatonit
      - ca-certificates
      - procps
    files:
      # The nested podman's default network (10.88.0.0/16) collides with the
      # outer podman network the service container itself is attached to, so
      # launch containers lose routing; pick a subnet the outer network does
      # not use.
      /etc/containers/containers.conf:
        content: |
          [network]
          default_subnet = "172.20.0.0/16"
    privileged: true
    caches:
      podman-storage:
        - /var/lib/containers/storage
    command:
      - podman
      - system
      - service
      - -t
      - "0"
      - unix:///run/podman/podman.sock
    exposes:
      podman: /run/podman/podman.sock
mounts:
  /var/run/docker.sock:
    service: podman
    socket: podman
environment:
  DOCKER_HOST: unix:///var/run/docker.sock
```

</details>

### sysutils

### `sysutils/docker`

Host docker socket

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Host docker socket
tools:
  docker-cli: latest
  docker-compose: latest
  hadolint: latest
mounts:
  /var/run/docker.sock:
    source: '{{ or (trimPrefix (index .Env "DOCKER_HOST") "unix://") "/var/run/docker.sock" }}'
    read_only: false
    optional: true
environment:
  DOCKER_HOST: unix:///var/run/docker.sock
```

</details>

### `sysutils/nix`

Nix package manager with persistent store cache

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Nix package manager with persistent store cache
packages:
  - nix-bin
caches:
  nix:
    - /nix
    - ~/.local/state/nix
mounts:
  ~/.config/nix:
    source: ~/.config/nix
    create: true
    read_only: false
    optional: true
files:
  /etc/profile.d/nix.sh:
    content: |
      export PATH="$HOME/.nix-profile/bin:$HOME/.local/state/nix/profiles/profile/bin:/nix/var/nix/profiles/default/bin:$PATH"
  /etc/nix/nix.conf:
    content: |
      extra-experimental-features = nix-command flakes
environment:
  # Prevents nix's chroot-store fallback (which requires NIX_STATE_DIR unset)
  # when the empty /nix cache volume has no state dir on first run.
  NIX_STATE_DIR: /nix/var/nix
```

</details>

### `sysutils/podman`

Host container engine socket (podman/docker)

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Host container engine socket (podman/docker)
tools:
  docker-cli: latest
  podman: latest
  docker-compose: latest
  hadolint: latest
mounts:
  /var/run/docker.sock:
    source: '{{ or (trimPrefix (index .Env "DOCKER_HOST") "unix://") (printf "/run/user/%s/podman/podman.sock" (uid)) }}'
    read_only: false
    optional: true
environment:
  DOCKER_HOST: unix:///var/run/docker.sock
```

</details>

### `sysutils/ssh`

SSH keys and client

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: SSH keys and client
mounts:
  ~/.ssh:
    source: ~/.ssh
    optional: true
  ~/.ssh/known_hosts:
    source: ~/.ssh/known_hosts
    read_only: false
    optional: true
packages:
  - openssh-client
```

</details>

### `sysutils/vault`

HashiCorp Vault CLI and token

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: HashiCorp Vault CLI and token
mounts:
  ~/.vault-token:
    source: ~/.vault-token
    optional: true
tools:
  vault: latest
```

</details>

### toolchain

### `toolchain/android`

Android SDK command-line tools (Java, Gradle, Kotlin)

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Android SDK command-line tools (Java, Gradle, Kotlin)
extends: toolchain/kotlin
tools:
  android-sdk: latest
```

</details>

### `toolchain/bash`

Bash shell config mounts and shellcheck

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Bash shell config mounts and shellcheck
tools:
  shellcheck: latest
mounts:
  ~/.bashrc:
    source: ~/.bashrc
    optional: true
  ~/.bash_profile:
    source: ~/.bash_profile
    optional: true
  ~/.bash_aliases:
    source: ~/.bash_aliases
    optional: true
  ~/.profile:
    source: ~/.profile
    optional: true
  ~/.inputrc:
    source: ~/.inputrc
    optional: true
packages:
  - bash
```

</details>

### `toolchain/c`

C toolchain (gcc, clang, make, cmake, ninja) with ccache

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: C toolchain (gcc, clang, make, cmake, ninja) with ccache
packages:
  - clang
  - cmake
  - gcc
  - gdb
  - make
  - ninja-build
  - pkgconf
tools:
  ccache: latest
caches:
  ccache: ~/.cache/ccache
```

</details>

### `toolchain/cpp`

C++ toolchain (g++, clang++) on top of C

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: C++ toolchain (g++, clang++) on top of C
extends: toolchain/c
packages:
  - g++
```

</details>

### `toolchain/dotnet`

.NET toolchain with NuGet cache

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: .NET toolchain with NuGet cache
tools:
  dotnet: latest
caches:
  nuget: ~/.nuget
```

</details>

### `toolchain/elixir`

Elixir and Erlang toolchain with hex and mix caches

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Elixir and Erlang toolchain with hex and mix caches
tools:
  elixir: latest
  erlang: latest
caches:
  hex: ~/.hex
  mix: ~/.mix
```

</details>

### `toolchain/go`

Go toolchain with GOPATH cache

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Go toolchain with GOPATH cache
caches:
  go: ~/go
tools:
  go: latest
```

</details>

### `toolchain/haskell`

Haskell toolchain (cabal, ghcup, stack)

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Haskell toolchain (cabal, ghcup, stack)
caches:
  cabal: ~/.cabal
  ghcup: ~/.ghcup
  stack: ~/.stack
tools:
  cabal: latest
  ghcup: latest
  stack: latest
```

</details>

### `toolchain/java`

Java toolchain with Maven and Gradle caches

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Java toolchain with Maven and Gradle caches
tools:
  java: latest
  gradle: latest
caches:
  gradle: ~/.gradle
  maven: ~/.m2
```

</details>

### `toolchain/javascript`

JavaScript toolchain (node, bun, deno) with npm caches

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: JavaScript toolchain (node, bun, deno) with npm caches
caches:
  bun: ~/.bun/install/global
  deno: ~/.cache/deno
  npm: ~/.npm
tools:
  bun: latest
  deno: latest
  node: latest
  npm:eslint: latest
  npm:prettier: latest
  pnpm: latest
```

</details>

### `toolchain/julia`

Julia toolchain

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Julia toolchain
caches:
  julia: ~/.julia
tools:
  julia: latest
```

</details>

### `toolchain/kotlin`

Kotlin toolchain (gradle) with gradle cache

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Kotlin toolchain (gradle) with gradle cache
extends: toolchain/java
tools:
  kotlin: latest
  gradle: latest
caches:
  gradle: ~/.gradle
```

</details>

### `toolchain/ocaml`

OCaml toolchain (opam)

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: OCaml toolchain (opam)
caches:
  opam: ~/.opam
tools:
  opam: latest
```

</details>

### `toolchain/perl`

Perl toolchain with cpan caches

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Perl toolchain with cpan caches
caches:
  cpan: ~/.cpan
  cpanm: ~/.cpanm
tools:
  perl: latest
```

</details>

### `toolchain/php`

PHP toolchain with composer cache and build deps

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: PHP toolchain with composer cache and build deps
packages:
  - libxml2-dev
  - libicu-dev
  - libonig-dev
  - libpq-dev
  - libxslt1-dev
  - libzip-dev
  - libmariadb-dev
  - libgd-dev
  - libpng-dev
  - libjpeg-dev
  - bison
  - re2c
tools:
  php: latest
caches:
  composer: ~/.cache/composer
```

</details>

### `toolchain/python`

Python toolchain (uv, pipx, pdm, poetry) with pip caches

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Python toolchain (uv, pipx, pdm, poetry) with pip caches
caches:
  pip: ~/.cache/pip
  pdm: ~/.cache/pdm
  poetry: ~/.cache/pypoetry
mounts:
  ~/.config/pypoetry:
    source: ~/.config/pypoetry
    create: true
    read_only: false
    optional: true
  ~/.config/pdm:
    source: ~/.config/pdm
    create: true
    read_only: false
    optional: true
tools:
  python: latest
  uv: latest
  pipx:black: latest
  pipx:pytest: latest
  pipx:ruff: latest
  pdm: latest
  poetry: latest
```

</details>

### `toolchain/ruby`

Ruby toolchain with gem cache

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Ruby toolchain with gem cache
tools:
  ruby: latest
caches:
  gem: ~/.gem
```

</details>

### `toolchain/rust`

Rust toolchain with cargo cache

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Rust toolchain with cargo cache
caches:
  cargo: ~/.cargo
tools:
  rust: latest
```

</details>

### `toolchain/scala`

Scala toolchain (sbt) with sbt, ivy and coursier caches

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Scala toolchain (sbt) with sbt, ivy and coursier caches
tools:
  scala: latest
  sbt: latest
caches:
  sbt: ~/.sbt
  ivy: ~/.ivy2
  coursier: ~/.cache/coursier
```

</details>

### `toolchain/typescript`

TypeScript toolchain (ts-node, tsx, typescript)

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: TypeScript toolchain (ts-node, tsx, typescript)
extends: toolchain/javascript
tools:
  biome: latest
  npm:ts-node: latest
  npm:tsx: latest
  npm:typescript: latest
```

</details>

### `toolchain/zig`

Zig toolchain

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Zig toolchain
caches:
  zig: ~/.cache/zig
tools:
  zig: latest
```

</details>

### vcs

### `vcs/git`

Git config and client

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: Git config and client
mounts:
  ~/.gitconfig:
    source: ~/.gitconfig
    optional: true
packages:
  - git
```

</details>

### `vcs/github`

GitHub CLI (gh)

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: GitHub CLI (gh)
tools:
  gh: latest
mounts:
  ~/.config/gh:
    source: ~/.config/gh
    create: true
    read_only: false
    optional: true
```

</details>

### `vcs/gitlab`

GitLab CLI (glab)

<details><summary>Source</summary>

```yaml
version: 1
meta:
  description: GitLab CLI (glab)
extends: vcs/git
mounts:
  ~/.config/glab-cli:
    source: ~/.config/glab-cli
    create: true
    read_only: false
    optional: true
tools:
  glab: latest
```

</details>

