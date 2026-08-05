# Services: companion containers

Date: 2026-08-04

## Motivation

Some dev workflows need a long-lived process that owns state a profile
container consumes. The general shape: a companion container runs a
daemon that owns a warm cache (storage, image store, registry mirror),
and N profile containers talk to it over a socket as clients. Without
this, each launch either gets cold storage (re-pulling/rebuilding every
time) or corrupts a shared volume by running two daemons against it.

A concrete motivating case is nested OCI builder daemons, but the
schema and runtime are generic — any socket-exposing infrastructure
daemon fits. The first real consumer will be wired up in a follow-up;
this spec lands the primitive.

## Design

### New field: `services:`

A `services:` block declares one or more companion containers that
start before the main container and stop after it. Each entry is a
mini-profile: it has its own image, packages, repos, caches, files,
mounts, env, and command, plus an `exposes:` map naming in-service
socket paths the main container can mount.

```yaml
version: 1
extends: mise
command: ["bash", "-l"]

services:
  registry:
    image: debian:13-slim
    packages: [docker-registry]
    command: ["registry", "serve", "/etc/docker/registry/config.yml"]
    caches:
      registry-data:
        - /var/lib/registry
    exposes:
      registry: /run/registry/registry.sock

mounts:
  /run/registry/registry.sock:
    service: registry
    socket: registry
environment:
  REGISTRY_SOCKET: unix:///run/registry/registry.sock
```

Services are intended for **infrastructure daemons** (registry
mirrors, build agents, OCI builder daemons) — processes whose state is
a warm cache shared across workspaces. They are not a replacement for
docker-compose-style application services; a `services: postgres:`
entry would share one database across every workspace, which is rarely
what a user wants. A future `namespace:`/`prefix:` setting can split
services when true isolation is needed; it is out of scope here.

### Service struct

```go
type Service struct {
    Image    string              `yaml:"image,omitempty"`
    Packages []string            `yaml:"packages,omitempty"`
    Repos    map[string]Repo     `yaml:"repos,omitempty"`
    Files    map[string]File     `yaml:"files,omitempty"`
    Command  []string            `yaml:"command,omitempty"`
    Caches   map[string]CachePaths `yaml:"caches,omitempty"`
    Mounts   map[string]Mount    `yaml:"mounts,omitempty"`
    Env      map[string]string   `yaml:"environment,omitempty"`
    Labels   map[string]string   `yaml:"labels,omitempty"`
    Exposes  map[string]string   `yaml:"exposes,omitempty"`
}
```

`Service` reuses the profile field set minus identity/launch fields
(`version`, `extends`, `ports`, `devices`, `network`, `resources`,
`tty`, `tools`, `dbus`, nested `services`). A service cannot launch the
workspace, publish ports, declare devices, or declare tools (mise runs
in the main container). `command` is required (the service's
long-running process). `image` is required (a service always runs in
its own image; it cannot inherit the profile's image because that
would couple service lifetime to the profile's package set).

`devices:` is rejected in v1 because a service needing host device
access (e.g. `/dev/fuse` for overlayfs) is a nested-daemon concern
that brings userns/caps/device-cgroup questions the first consumer
will work through. Adding `devices:` later is a scoped extension, not
a schema break.

A service's `mounts:` supports the same host-bind form as a profile
(`source:` host path, plus `read_only`/`optional`/`create`). The one
restriction: a service mount **must not** use the `service:`/`socket:`
form — services cannot depend on other services in v1 (no inter-service
dependencies, see Out of scope). Validation rejects a `service:`/`socket:`
mount inside a `services:` entry.

### Mount source discriminator

`Mount` gains two optional fields:

```go
type Mount struct {
    Source   string `yaml:"source,omitempty"`
    Service  string `yaml:"service,omitempty"`
    Socket   string `yaml:"socket,omitempty"`
    ReadOnly bool   `yaml:"read_only,omitempty"` // UnmarshalYAML defaults to true for bind mounts, false for service-socket mounts
    Optional bool   `yaml:"optional,omitempty"`
    Create   bool   `yaml:"create,omitempty"`
}
```

The discriminator is implicit:

- `Source` set (and `Service`/`Socket` unset) → host bind (today's only form). `read_only` defaults to `true` (today's behavior).
- `Service`+`Socket` set (and `Source` unset) → service socket mount; tpd resolves the host run-dir path for that service's exposed socket and bind-mounts it. `read_only` defaults to `false` (a unix-socket bind mounted read-only fails `connect()` with `EACCES`; the primary use case needs a writable socket).
- Both kinds set, or `Service` without `Socket` (or vice versa) → schema error.
- `Create: true` on a service mount → schema error (the service, not the main container, owns the socket; tpd ensures it exists before `Run`).
- `Optional: true` on a service mount → schema error (the service either started or tpd errors before `Run`; an optional service-socket mount has no useful semantics).

The existing object form (`/path: { source: /host-path, ... }`) is
unchanged; it just gains two optional keys. Marshaling emits only the
set kind, so round-tripping keeps the compact form.

### Service instance scope: global

A service instance is **global**: one companion container per service
name across all launches, regardless of workspace. The service
container is named `tpd-svc-<name>` (rootless) or
`tpd-svc-<name>-<uid>` (rootful, to avoid cross-user collisions on a
shared daemon) and labeled `tpd.managed=true`, `tpd.service=<name>`,
`tpd.service-role=sidecar`, `tpd.service-hash=<hash>`.

**Multi-user rootful is not supported.** The primary target is
single-user rootless Podman on Linux. Rootful services are reachable in
the single-user case (see "Socket ownership in rootful mode"). The
rootful `<uid>` suffix prevents name/socket-dir collisions if two users
happen to share a rootful daemon, but tpd does not test or support that
multi-user configuration.

Service caches are regular shared named volumes
(`tpd-cache-<cachename>`), shared across all launches that use the
same service — exactly like today's profile caches.

**Service names are validated** against the same charset as profile
names (`profileNameRe`): the name goes into a container name and a
filesystem path, so `/`, `:`, and whitespace are rejected.

### Definition hash and recreation on config change

A service container carries a `tpd.service-hash` label set to the
first 8 hex chars of SHA-256 over its **unresolved** definition — the
raw `image`, `packages`, `repos`, `files`, `command`, `env`,
`exposes`, and `mounts` fields as declared in the profile, before
template/tilde expansion. Hashing the unresolved definition means two
launches with different host env (`{{ .Env.X }}` resolving
differently) do not churn the hash and recreate a live shared daemon.

**One recreation rule** (replaces all earlier drafts):

1. Find a running container by the deterministic service name.
2. If none exists, create fresh (after removing any stopped
   same-named straggler).
3. If one exists and its `tpd.service-hash` label matches the current
   hash, reuse it.
4. If one exists but the hash differs:
   - Under the service lock, count running main containers labeled
     `tpd.uses-service` that include `<name>`.
   - Stop+remove the old container, create fresh. Cache volumes persist
     and warm the new instance. If other consumers are still attached, a
     warning names them — the old daemon is replaced regardless,
     accepting a brief outage for those consumers so the new launch gets
     the updated service immediately instead of being blocked by them.

This rule is uniform across same-profile-config-edits and
cross-profile-name-collisions: a changed config always wins on the next
launch, and consumers of a stale daemon are notified via the warning
rather than silently kept on it. No profile-identity label is needed.

### Lifecycle

tpd owns the entire service lifecycle; the user never starts or stops
services manually. Services are **disposable**: they stop when their
last consumer exits, matching tpd's "disposable environments"
philosophy — this is not docker-compose.

**Lockfile serialization.** All service start/stop operations are
serialized by a per-service `flock` on
`~/.local/share/tpd/svc-<name>.lock` (created on first use, mode 0700
parent dir). The kernel releases the lock on process death, so a
SIGKILL'd tpd never leaves a stale sentinel.

**Lock coverage.** The lock is held from the start/reuse decision
through the main container's `ContainerCreate` (which applies the
`tpd.uses-service` label). This closes the start→consumer-registration
gap: a concurrent stop step cannot see "zero consumers" between a
launch's service-start and its main-container-create, because the lock
is held throughout. The lock is released after `ContainerCreate`
returns, before `Run` (the actual execution).

**Pre-Run phase** (in `LaunchWithWriter`, after `Prepare` returns the
main image ref and before `Run`):

1. Acquire locks for all declared services.
2. For each service, find-or-start (see below).
3. Resolve every service-socket mount to a host path and rewrite it
   into the main container's mount list as a bind mount.
4. `ContainerCreate` the main container (with `tpd.uses-service`
   label).
5. Release all service locks.
6. Proceed to `Run` (ContainerStart, attach, wait).

**Starting a service** (step 2, under the lock):

1. Find a running container by the deterministic name.
   - If present and hash matches: reuse.
   - If present and hash differs: apply the recreation rule above.
   - If absent: detect a stopped same-named straggler (from a
     SIGKILL'd tpd) and remove it, then create fresh.
2. To create fresh:
   1. Create cache volumes and prepare subpaths via
      `ensureCacheSubpaths` — service caches use the same subpath
      machinery as profile caches (multi-path caches need the
      helper-container mkdir dance on engines that honor
      `VolumeOptions.Subpath`).
   2. If the service declares `packages:`/`repos:`, build/reuse its
      derived image (`DerivedTag` over the service's base image ID +
      packages + repos) — same path as profile derived images, hashed
      independently. `--pull` refreshes the service's base image too.
   3. Create the host run-dir (mode 0700) and each expose's parent
      directory inside it (see "Socket path allocation").
   4. Unlink any stale socket files in the run-dir (a force-removed
      service leaves sockets behind; the next launch's poll would see
      a dead socket).
   5. Create the service container with `tpd.service=<name>`,
      `tpd.service-hash=<hash>`, `tpd.managed=true`,
      `tpd.service-role=sidecar` labels (tpd's labels applied after
      user labels, same precedence as `buildSpec` applies today); its
      caches mounted; each expose's parent dir bind-mounted from the
      run-dir into the service container.
   6. Start the service container.
   7. Poll each exposed socket with a bounded timeout (e.g. 30s). The
      poll is a `connect()` probe (Unix socket), not a `stat()`: a
      stale socket file passes `stat` but fails `connect`, and a
      service that `touch`es the socket before `accept` is not yet
      ready. Readiness for v1 is defined as "the socket accepts a
      connection" — this is not a universal readiness mechanism, but
      it is correct for socket-daemon services.
   8. If any socket does not appear within the timeout, fail with a
      clear error ("service <name> did not expose socket <socket>
      within 30s") and remove the service container. No main
      container is created.

**Service container runtime model:**

- Runs as `0:0` (root). Services are trusted infrastructure; no
  setpriv drop. A service that needs to drop privileges can do so in
  its own `command`.
- `init=tini` (same as the main container, so signals drain cleanly).
- `WorkingDir=/`.
- Network: engine default (a daemon may need to pull images/data).
- No workspace mount (services don't touch the user's project). This
  means a service-side daemon resolves bind-mount paths against its
  own filesystem, not the main container's — the service is suited to
  socket-API workflows (build via context upload, run without project
  bind mounts), not to workflows requiring the service to see the
  workspace directly.
- Service `files:` are written as root (the service runs as root); no
  chown to the host user.

**Stopping a service** (after `Run` returns, on every exit path
including errors — see "Error handling" below):

1. Acquire the service's lockfile.
2. List running containers labeled `tpd.uses-service` that include
   `<name>`.
3. If none remain, stop and remove the service container (`--time=10`
   graceful, then force-remove).
4. Service cache volumes are **not** removed on service stop — they
   persist and are warmed for the next launch. They are removed only
   by `tpd prune`.
5. Release the lock.

**Error handling:** the stop step is `defer`'d in `LaunchWithWriter`
so it runs on every exit path: successful `Run`, `Run` error
(ContainerCreate/ContainerStart/attach failure), and pre-Run errors
after a service was started. If the pre-Run phase itself fails before
any service is started, the stop step is a no-op (no services to
stop). This prevents an unbounded service leak when the main container
fails to create after the service started.

**Failure modes:**

- Service container exits during `Run` (crashed companion): the main
  container's socket mount becomes stale; calls fail with a socket
  error. tpd does not auto-restart the service mid-launch. A future
  enhancement could health-check and restart; out of scope for v1.
- Service fails to start (socket never appears): pre-Run fails with
  the bounded-timeout error above; no main container is created. The
  `defer`'d stop step removes the failed service container.
- tpd is SIGKILL'd mid-launch: the lockfile is released by the kernel;
  the service container may be left running. The next launch detects
  it by name and reuses or recreates it. `tpd doctor` reports it (see
  below).

### Socket path allocation

Each service gets a deterministic host run-dir:

- Rootless: `/run/user/<uid>/tpd-svc-<name>/`
- Rootful: `/tmp/tpd-svc-<name>-<uid>/` (user-writable — the host tpd is not
  root; `/tmp` is transient, and the `<uid>` suffix keeps it distinct from a
  concurrent rootless run sharing `/run/user/<uid>`)

For each expose entry `<socket-name>: <container-path>`, tpd:

1. Creates `<run-dir><dirname(container-path)>/` on the host (mode
   0700). E.g. for `/run/registry/registry.sock`, creates
   `<run-dir>/run/registry/`.
2. Bind-mounts that host directory into the service container at
   `dirname(container-path)` (e.g. `<run-dir>/run/registry/` →
   `/run/registry/`). The service creates the socket file inside, which
   appears on the host at `<run-dir><container-path>`.
3. The main container bind-mounts the host socket file
   (`<run-dir><container-path>`) to its target (the `mounts:` key).

Stale cleanup (step 2.2.4 above) unlinks `<run-dir><container-path>`
if it exists before the service starts.

**Socket ownership in rootful mode:** a rootful service creates its socket
`root:root`, which the host tpd (a non-root user) cannot `connect()` to. On
first start, before the probe, tpd execs `chown <hostUID>:<hostGID>` +
`chmod 0770` inside the service container (running as root against the
bind-mounted file) the first time the host socket appears, so the existing
`connect()` probe works as in rootless. The chown's coverage is best-effort and
first-start-only: reuse never probes (see Lifecycle), and the main container's
bootstrap `chown` of its socket mount targets grants the consumer (host user
after setpriv) access regardless of the socket's state at mount time — that
bootstrap chown is what covers a daemon recreating its socket between launches.
The probe-time chown is not skipped for `privileged` services: privileges and
socket ownership are orthogonal. A failed chown exec (e.g. an image without
`chown`) fails the launch with the exec's exit code.

The run-dir is verified after creation (`ensureServiceRunDir`): a pre-existing
symlink or a directory owned by another user is rejected, since `/tmp` is
world-writable. systemd-tmpfiles may clear `/tmp` after ~10 days; a service
kept alive across launches that long would lose its socket dir (the daemon
keeps its bound socket, but the next launch's bind mount breaks). tpd services
are disposable, so this is accepted and documented rather than worked around.

### Template and tilde resolution

`ResolveTildes` recurses into `cfg.Services`: service `env`, `files`,
`command`, `mounts` (host-bind), and `caches` are tilde-expanded and
template-rendered with the same `.Env` and `uid` context as the main
profile (`.Ports` does not apply — services have no ports). The
skip-logic for service-socket mounts (which have `Source == ""` and
`optional` forbidden) applies inside services too, though
service-socket mounts are already schema-rejected inside `services:`;
the skip is a safety net.

The definition hash (above) is computed over the **unresolved** fields,
so template expansion does not cause hash churn.

### Prune

Service cache volumes are regular `tpd-cache-<name>` volumes. Default
`prune` keeps a volume if any resolvable profile declares it; the
walk in `computeUsed` must cover both `cfg.Caches` and
`cfg.Services[*].Caches` so service cache volumes are marked used.

Service derived images (`tpd/packages:<hash>` from a service's
`packages:`/`repos:`) must likewise be added to `computeUsed`: hash
each resolved service's `(base image ID, packages, repos)` and add the
resulting `DerivedTag` to `usedImages`. Without this, default prune
would delete every warm service image, guaranteeing a cold rebuild on
next launch.

`prune --all` removes service cache volumes and derived images
regardless.

Service containers themselves are labeled `tpd.managed=true`. `prune`
does not currently remove containers; a stopped straggler is reported
by `tpd doctor` and removed by the next launch's start step. A future
`tpd prune --containers` could sweep them; out of scope for v1.

### Doctor

`checkLeakedContainers` is extended to understand service containers.
A **running** sidecar (`tpd.service-role=sidecar`, running state) is
flagged if no running main container references it via
`tpd.uses-service` — this catches the SIGKILL'd-tpd orphan case (the
service is running but nobody is consuming it). A **stopped** sidecar
is always flagged (it should have been removed by the stop step). This
requires doctor to list running main containers and cross-reference
their `tpd.uses-service` labels against running sidecar names.

### Merge semantics

`Services` is a **first-class mergeable field** carried through the
extends chain, treated as a key-by-key map merge (child wins per key),
identical to `mounts` or `caches`. A child profile's `services: foo:
{ ... }` replaces a parent's `services.foo` entirely (the child's
entry substitutes; there is no per-field deep merge of service bodies
— same rule as `mounts` today). `null` deletes an inherited service
key (consistent with the existing null-to-delete convention);
`collectNullKeys` must track `services` so null-to-delete works.

The resolved `Services` map is carried on `Profile`/`RawProfile` and
travels through `resolveChain` like any other mergeable field. There
is no two-phase resolution: services are always just map entries
merged onto the body, and their inlining into the runtime `Spec`
happens in `buildSpec`.

`services:` is allowed in **both profiles and fragments**. A fragment
declaring a service is the composition vehicle: a `registry` fragment
can declare its service, and every profile extending it gets the
daemon for free. The same merge semantics and validation rules apply.

### Runtime Spec

`runtime.Spec` gains a `Services []ServiceSpec` field. `buildSpec`
converts each resolved `profile.Service` into a `ServiceSpec`
carrying image, packages, caches, mounts, env, command, and the
resolved expose list. The runtime's pre-Run phase iterates services,
finds-or-starts each, and rewrites service-socket `MountSpec`s with
the resolved host socket path before constructing the main container's
mount list.

`MountSpec` gains the same two optional fields (`Service`, `Socket`)
so `buildSpec` can carry them through to the runtime; the runtime
rewrites them to bind mounts in the pre-Run phase.

`RenderSpec` (dry-run/verbose) shows declared services and their
exposes, and shows service-socket mounts with their `service`/`socket`
fields (unresolved — dry-run does not start services).

The `Runtime` interface gains the service lifecycle methods;
`FakeRuntime` (`internal/runtime/fake.go`) grows stub implementations
so existing tests keep passing, and new fakes support service-lifecycle
assertions.

### Validation

New validation rules in `internal/profile/validate.go`:

- A `services:` entry must set `image` and `command` (schema error
  otherwise).
- A `services:` entry must not set any of the rejected identity/launch
  fields: `version`, `extends`, `ports`, `devices`, `network`,
  `resources`, `tty`, `tools`, `dbus`, nested `services`.
- A mount inside a `services:` entry must not use the `service:`/`socket:`
  form (no inter-service dependencies in v1). Host-bind mounts
  (`source:`) are allowed and follow the same rules as profile mounts.
- `exposes:` values must be absolute paths (the service writes the
  socket there; a relative path would be relative to the service's
  working dir, which is not part of the service contract).
- A mount with `service:` must also set `socket:`; a mount with
  `socket:` must also set `service:`.
- A mount with `service:`/`socket:` must not set `source:`,
  `create:`, or `optional:`.
- A mount's `service:` must reference a service declared in the same
  profile (after merge). Error message names the missing service.
- A mount's `socket:` must reference an entry in that service's
  `exposes:` map. Error message names the missing expose.
- A service `name` must match `profileNameRe` (it goes into a
  container name and a filesystem path).

### Out of scope for v1

- Standalone reusable services as catalog entries (services are
  inline-only, defined in a profile or fragment).
- Inter-service dependencies (a service cannot declare another
  service; only a profile/fragment can).
- `devices:` on services (needed for nested daemons requiring
  `/dev/fuse` etc.; deferred with the first nested-daemon consumer).
- Non-socket exposes (files, ports, generic dirs). `exposes:` is
  socket files only in v1.
- Health checks / auto-restart of crashed services mid-launch.
- A `tpd services` command (list/stop/logs); lifecycle is fully
  automatic. A straggler service is visible to `tpd doctor` and
  removed by the next launch.
- Per-workspace or per-namespace service scoping. A future
  `namespace:`/`prefix:` setting on the profile or config can split
  services and caches when true isolation is needed; the global model
  is the v1 default.
- A built-in catalog profile/fragment wiring up a real service. The
  schema and runtime support land first; the first real consumer
  (e.g. a nested OCI builder daemon) is a follow-up.

## Files

- `internal/profile/types.go`: add `Service` struct and `Services
  map[string]Service` field on `Profile`/`RawProfile`; extend `Mount`
  with `Service`/`Socket` fields and update its custom
  `UnmarshalYAML`/`MarshalYAML` to handle the discriminator and the
  per-kind `read_only` default.
- `internal/profile/validate.go`: add the service validation rules
  above, including service-name regex validation.
- `internal/profile/merge.go`: add `Services` to `MergeProfiles`
  (key-by-key map merge, child wins per key, `null` deletes).
- `internal/profile/catalog.go`: add `services` to the mergeable-fields
  list and to `collectNullKeys` so null-to-delete works. Services are
  allowed in both profiles and fragments; do not add `services` to the
  fragment rejected-field set.
- `internal/profile/paths.go`: `ResolveTildes` recurses into
  `cfg.Services` (env, files, command, mounts, caches) and skips
  service-socket mounts (identified by `Service != ""`) in both the
  main profile's mounts and service mounts.
- `internal/prune/prune.go`: `computeUsed` walks `cfg.Services` in
  addition to `cfg.Caches` for cache volumes, and hashes each
  service's `(base image ID, packages, repos)` for derived images.
- `pkg/tpd/spec.go` (`buildSpec`): convert resolved `cfg.Services` to
  `runtime.ServiceSpec`; pass service-socket mounts through to
  `MountSpec`. Apply tpd managed labels after user-supplied service
  labels (same precedence as the main profile).
- `pkg/tpd/launch.go`: drive the pre-Run service phase (acquire locks,
  find-or-start each service, resolve socket paths, rewrite mounts,
  `ContainerCreate` the main container, release locks, then `Run`).
  `defer` the stop step so it runs on every exit path including
  errors. The lock and stop logic lives in the runtime package;
  launch.go orchestrates the phases.
- `pkg/tpd/dryrun.go` (`RenderSpec`): show declared services and
  unresolved service-socket mounts.
- `internal/runtime/runtime.go`: add `ServiceSpec` and the
  `Services []ServiceSpec` field on `Spec`; extend `MountSpec` with
  `Service`/`Socket`; extend the `Runtime` interface with service
  lifecycle methods.
- `internal/runtime/docker_services.go` (new): implement the pre-Run
  service phase (lock, find-or-start, stale-container cleanup,
  stale-socket unlink, derived image build, cache subpath prep,
  socket connect-probe, mount rewriting) and the post-Run service
  stop (lock, label-based consumer check). Reuse the existing
  `EnsureVolume`/`ensureCacheSubpaths`/`ContainerCreate`/
  `ContainerStart` helpers.
- `internal/runtime/fake.go`: grow stub service-lifecycle methods so
  existing tests keep passing; add fakes for service assertions.
- `internal/runtime/labels.go`: add `tpd.service`,
  `tpd.service-role=sidecar`, `tpd.service-hash`, and
  `tpd.uses-service` label constants.
- `internal/doctor/checks.go`: `checkLeakedContainers` flags running
  sidecars with zero consumers and stopped sidecars; requires listing
  running main containers and cross-referencing `tpd.uses-service`.
- Tests across `internal/profile/`, `pkg/tpd/`, and
  `internal/runtime/` for: service parse/validate, service-name
  regex, service-merge (child-replaces-parent, null-deletes),
  service-in-fragment, service-socket mount discriminator
  (both-kinds-rejected, missing-service-rejected,
  missing-socket-rejected, create-on-service-mount-rejected,
  read-only-defaults-false-for-service-mounts),
  paths.go-recurses-into-services-and-skips-service-socket-mounts,
  lifecycle (find-or-start, reuse-running-same-hash,
  recreate-on-hash-change-zero-consumers,
  warn-and-reuse-on-hash-change-with-consumers,
  stale-container-removed-before-create, stale-socket-unlinked,
  stop-when-no-consumers, stop-on-run-error-path,
  concurrent-consumers-share,
  lock-held-through-main-container-create,
  start-stop-race-does-not-kill-live-service),
  prune (`computeUsed` includes service caches and service derived
  images), doctor (running service with consumers not flagged,
  running service without consumers flagged, stopped service flagged).