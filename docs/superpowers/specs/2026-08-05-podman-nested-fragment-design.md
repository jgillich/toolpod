# Host-socket fragment rename + nested podman service fragment

Date: 2026-08-05

## Goal

1. Rename the host-socket engine fragments `docker` and `podman` to `docker-host`
   and `podman-host` (content unchanged) so their names make explicit that they
   expose the host engine.
2. Reuse the freed `podman` name for a new fragment that gives the profile a
   **nested podman engine running as a service** — an isolated engine with no
   host daemon access, the first real consumer of the services feature.

## Design

### Rename

`internal/catalog/fragments/docker.yaml` → `docker-host.yaml`,
`internal/catalog/fragments/podman.yaml` → `podman-host.yaml`. Both keep their
current host-socket mounts and `docker-host`/`podman-host` advisory (host engine
socket access, read-write). This is a breaking rename for existing user profiles
that `extends: [docker]`/`[podman]`; the advisory output names the replacement.

### New `podman` fragment

No host mounts. Wires a **privileged, rootful** nested podman daemon in a service
sidecar:

- `tools:` — `podman` (mise CLI), `docker-compose`, `hadolint` so the main
  container has a client for the nested socket.
- `services.podman` (service name `podman`; the singleton container is
  `tpd-svc-podman`):
  - `image: debian:13-slim`; `packages: [podman, nftables, iptables, iproute2,
    catatonit, ca-certificates]`. The netavark networking stack needs `nft`
    (nftables), `iptables`, and `ip` (iproute2).
  - `privileged: true` — the service container runs podman as **root** (rootful)
    and is privileged.
  - `command: ["podman", "system", "service", "-t", "0",
    "unix:///run/podman/podman.sock"]`.
  - `caches: podman-storage: [/var/lib/containers/storage]` — the nested
    engine's image store persists across launches (warm cache).
  - `exposes: podman: /run/podman/podman.sock`.
- `mounts: /var/run/docker.sock: {service: podman, socket: podman}`.
- `environment: DOCKER_HOST: unix:///var/run/docker.sock`.

**Why privileged + rootful?** The services schema gained a `privileged: true`
field on service entries. It is load-bearing here: an **unprivileged** service
container cannot run a nested engine, for three independently-verified reasons:

1. **UID/GID mapping** — the nested rootless engine maps only a single uid, so
   unpacking images with other uids fails (`lchown /etc/gshadow: invalid
   argument`). The `[storage.options] ignore_chown_errors` workaround fixes
   unpack, but the remaining blockers are fatal.
2. **Networking** — the sidecar has no `/dev/net/tun`, so `pasta`/`slirp4netns`
   cannot create a network namespace (both fail with `Failed to open()
   /dev/net/tun`); only `--network=host` works.
3. **Nested `/proc` mounts** — `mount -t proc` is denied (`EPERM`) from the
   nested user namespace by the kernel's pid-namespace ownership rule, and
   `crun` cannot start any container without mounting `/proc`.

A privileged service container gets `CAP_SYS_ADMIN` + all devices inside its
user namespace, so a **rootful** nested podman creates containers directly in the
service's userns (no nested userns, no `/proc`/tap problems). Verified on the
reference host: `podman run ubuntu:24.04 id` works through the privileged
sidecar.

**Socket reachability:** the rootful engine creates the socket as the service
container's root, which maps to the host user in the outer rootless userns, so
tpd's probe and the main container connect without any chown/chmod.

**Advisory:** the nested engine grants no *host* access — the privileged grant is
contained in the service container's user namespace (a privileged rootless
container is not host root). So `podman` carries no advisory; `podman-host`/
`docker-host` keep theirs. The security model documents the privilege grant.

### Services schema addition: `privileged:`

`Service` (and `runtime.ServiceSpec`) gain `privileged: bool`; `createService`
passes it to `HostConfig.Privileged`. It is part of the service definition hash
(so a `privileged` flip recreates the service). The `devices:` field remains
rejected on services; `privileged` covers the whole namespace including devices.

### References updated

- `internal/catalog/advisories.go` — `docker`→`docker-host`, `podman`→`podman-host`.
- `internal/catalog/catalog_test.go` — advisory list; `TestPodmanNestedFragment`
  asserts the privileged rootful service shape.
- `cmd/tpd/cli_test.go`, `cmd/tpd/completion_test.go` — `docker`→`docker-host`.
- `internal/profile/types.go`, `internal/profile/hash.go`,
  `internal/runtime/runtime.go`, `pkg/tpd/spec.go`,
  `internal/runtime/docker_services.go`, `pkg/tpd/dryrun.go` — the
  `privileged` field.
- `docs/2026-08-03-security-model.md` — table rows for
  `docker-host`/`podman-host`; note the privileged nested engine.
- `README.md` — document the split if it names the fragments.
