# Host-socket fragment rename + nested podman service fragment

Date: 2026-08-05

## Goal

1. Rename the host-socket engine fragments `docker` and `podman` to `docker-host`
   and `podman-host` (content unchanged) so their names make explicit that they
   expose the host engine.
2. Reuse the freed `podman` name for a new fragment that gives the profile a
   **nested rootless podman engine running as a service** — an isolated engine
   with no host daemon access, the first real consumer of the services feature.

## Design

### Rename

`internal/catalog/fragments/docker.yaml` → `docker-host.yaml`,
`internal/catalog/fragments/podman.yaml` → `podman-host.yaml`. Both keep their
current host-socket mounts and `docker-host`/`podman-host` advisory (host engine
socket access, read-write). This is a breaking rename for existing user profiles
that `extends: [docker]`/`[podman]`; the advisory output names the replacement.

### New `podman` fragment

No host mounts. Wires a rootless nested podman daemon in a service sidecar:

- `tools:` — `podman` (mise CLI), `docker-compose`, `hadolint` so the main
  container has a client for the nested socket.
- `services.podman` (service name `podman`; the singleton container is
  `tpd-svc-podman`):
  - `image: debian:13-slim`; `packages: [podman, uidmap, slirp4netns,
    ca-certificates]`.
  - `files:` `/etc/subuid`, `/etc/subgid` (`podman:100000:65536`) so the nested
    engine can map a userns range (Debian `useradd` does not add these), and
    `/etc/containers/containers.conf` with `[storage] driver = "vfs"` — the
    sidecar has no `/dev/fuse` (services reject `devices:` in v1), so
    fuse-overlayfs is impossible.
  - `command:` a root shell that creates the `podman` user (uid 1000), chowns
    the cache volume and the expose socket dir to it, then
    `setpriv --reuid=1000 --regid=1000` with `HOME=/home/podman`,
    `XDG_RUNTIME_DIR=/run/podman` and execs
    `podman system service -t 0 unix:///run/podman/podman.sock`.
  - `caches: podman-storage: [/home/podman/.local/share/containers]` — the
    nested engine's image store persists across launches (warm cache).
  - `exposes: podman: /run/podman/podman.sock`.
- `mounts: /var/run/docker.sock: {service: podman, socket: podman}`.
- `environment: DOCKER_HOST: unix:///var/run/docker.sock`.

**Why rootless + vfs + setpriv:** the service runtime creates sidecars as root
with no `privileged`/`devices`/userns overrides. A rootful nested daemon needs
`CAP_SYS_ADMIN` it will not have. A rootless nested podman creates its own user
namespace (podman's default seccomp permits nested userns) and runs containers
unprivileged given a storage driver that needs no `/dev/fuse` — hence `vfs`.

**Advisory:** the nested engine grants no host access, so `podman` carries no
advisory. The `podman-host`/`docker-host` fragments keep their host-socket
advisories.

### References updated

- `internal/catalog/advisories.go` — `docker`→`docker-host`, `podman`→`podman-host`.
- `internal/catalog/catalog_test.go` — advisory list; new test that the `podman`
  fragment resolves to a service with the nested-engine shape.
- `cmd/tpd/cli_test.go`, `cmd/tpd/completion_test.go` — `docker`→`docker-host`.
- `docs/2026-08-03-security-model.md` — table rows for `docker-host`/`podman-host`;
  note the nested engine is contained with no host access.
- `README.md` — document the split if it names the fragments.

### Out of scope / caveats

- No podman host is available in this environment, so the nested engine cannot be
  verified end-to-end here. Schema/catalog tests validate the fragment; runtime
  verification (`podman run` through the nested socket) is a documented follow-up
  on a rootless-podman host.
- No runtime changes: services keep rejecting `privileged`/`devices`; if the
  nested engine later needs them, that is a separate extension.
