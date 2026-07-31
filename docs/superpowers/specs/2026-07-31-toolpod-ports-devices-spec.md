# `ports` + `devices` Profile Schema — Spec

**Status:** Draft for review
**Date:** 2026-07-31
**Validated against:** toolpod working tree: `internal/profile`
(`types.go`, `merge.go`, `validate.go`, `catalog.go`, `paths.go`), `pkg/toolpod`
(`spec.go`, `launch.go`, `dryrun.go`), `internal/runtime/runtime.go`,
`internal/runtime/docker_run.go`, `internal/catalog`.

## Summary

Add two optional, escape-hatch schema fields to the profile config so a profile
can publish ports to the host and attach host devices (GPU, USB, `/dev/fuse`,
etc.) to the container. Both are **opt-in**: existing profiles and built-ins are
unaffected; the fields default to empty and are absent from `shell`/`opencode`/
`codex`.

Auto-allocated (random) host ports are exposed to the existing
`{{ }}` template machinery as a `.Ports` map (keyed by container port), so a
profile's `command`/`environment` can reference the host port the instance
actually got. Combined with the existing random container names, this lets
multiple instances of the same profile run concurrently, each on its own port —
the opencode-web multi-instance use case.

```yaml
version: 1
extends: shell
command: ["opencode", "web", "--port", "{{ index .Ports \"8080\" }}"]
ports:
  8080: {}          # container 8080/tcp; host port auto-allocated
  9000:
    host: 5173      # fixed host port 5173 -> container 9000
  53:
    protocol: udp   # container 53/udp; host port auto-allocated
  5432:
    host: 5432
    host_ip: 127.0.0.1   # bind only on loopback (default: all interfaces)
  # null removes an inherited binding:  8080: null
devices:
  /dev/fuse: {}             # source defaults to the key path on the host
  /dev/nvidia0: { permissions: rwm }
  /dev/bus/usb: { source: /dev/bus/usb, cgroup: true }  # see cgroup note
  # null removes an inherited device:  /dev/fuse: null
```

---

## Goals

1. Add `ports` as a container-port-keyed map with optional `host`,
   `host_ip`, and `protocol` properties. Missing `host` (or `host: 0`)
   allocates an unused host port.
2. Add `devices` supporting container-device-keyed entries with an optional
   host `source`, `permissions` (rwm), and an opt-in `cgroup` flag for broad
   device-cgroup allow-rules (advanced: DinD/GPU-enum). The common case needs
   only `source`/`permissions` — see the cgroup note.
3. Integrate with the existing merge semantics (keyed maps, null-to-delete)
   so `extends` works transparently.
4. Expose allocated host ports to the existing `{{ }}` template machinery
   (`.Ports` keyed by container port) and extend template rendering to
   `environment` values and `command`/`args_if_none` args so profiles can
   launch commands on the port they were given.
5. Keep built-ins minimal — no field added to `shell`/`opencode`/`codex`.
6. Surface bindings in `--dry-run`/`--verbose` output and in `doctor`/`config
   show --resolved`.

## Non-Goals

1. No new `ports:`/`devices:` on built-in profiles.
2. No validation that host devices/ports exist at `config check` time — those
   are host-state checks done at `Prepare`/launch (skip cleanly in CI /
   `--dry-run` without a runtime).
3. No compose-style service dependencies, health ports, ranges, or `expose`.
   Pure host↔container bindings only.
4. No `network: host` interaction beyond a warning that `ports:` are redundant
   under host networking.
5. No dual-protocol binding on the same container port (map-key collision by
   design; tcp+udp on one port is out of scope).
6. No port-conflict resolution: two instances binding the same explicit
   `host` port fail at start with the engine's bind error. No auto-remapping
   or conflict detection.

---

## Schema

### `ports` (optional map)

- **Key** = container port (numeric; YAML int or string, normalized to
  string). This is the merge identity and the template key — the template
  key is the bare container port, **without** a protocol suffix
  (`.Ports` key `"8080"`, never `"8080/tcp"`).
- **Value** (empty `{}` = all defaults):
  - `host`: host port to bind. Missing, `0`, or `""` → an unused host port is
    allocated (random). String or int accepted (normalized). Note the
    distinction from `null`: `host: 0` keeps the binding and switches it to
    random; `8080: null` deletes the inherited entry entirely.
  - `host_ip`: bind address on the host. Default `""` = all interfaces
    (matches docker/compose behavior).
  - `protocol`: `tcp` (default), `udp`, or `sctp`. `sctp` requires an
    explicit `host` — auto-allocation for sctp is a validation error (Go
    stdlib cannot pre-allocate an sctp port).
- `null` deletes an inherited binding (reuse the §4.3 null-to-delete rule;
  consistent with `mounts`).

### `devices` (optional map)

- **Key** = container device path (identity key for merge).
- **Value:**
  - `source`: host device path (default = key, i.e. same path on host).
  - `permissions`: `r`, `rw`, or `rwm` (default `rwm`).
  - `cgroup`: bool, default **false**. When true, emit a broad
    device-cgroup allow-rule so the container can use the device. **See the
    cgroup note below** before flipping this on.
- `null` deletes an inherited device by container path.

> **cgroup note — prefer `false`.** Modern Docker and Podman grant access to a
> device listed in `HostConfig.Devices` directly (the `Devices` entry *is* the
> cgroup rule for that node), so for the common case — `/dev/fuse`, a specific
> USB node, a GPU char device — `cgroup: false` (default) is sufficient and
> safer. Set `cgroup: true` only for workflows that need a *broad* allow-rule
> not tied to one node (Docker-in-Docker that mknods arbitrary nodes, or a GPU
> driver that enumerates devices at runtime). When `cgroup: true`, emit
> `HostConfig.DeviceCgroupRules` scoped to the device's major:minor where
> derivable (parse `/sys/dev/char`/`/sys/dev/block` at `Prepare`), falling back
> to `["c <major>:*"]` per-device rather than the blanket `["c *:*"]` — a
> blanket all-char-devices rule is a privilege escalation and must not be the
> default.

### Validation (runs on the resolved/merged profile, exit code 2 on failure)

- Container port in range 1–65535 (key parse is pure YAML — no string
  parsing; a non-numeric key or out-of-range value fails at load).
- `host` in range 1–65535 (after YAML normalization; `0`/empty = random).
- `host_ip` accepted as-is (validated by the runtime/engine on bind failure).
- `protocol` must be `tcp`, `udp`, or `sctp`; `sctp` with missing/`0`
  `host` is a validation error (no client-side allocation possible).
- `network: host` + non-empty `ports` → warn (not error) to stderr (ports are
  redundant under host networking).
- `devices` with `source` referencing a host path that does not exist → not
  a config error; checked at `Prepare` (host state), skipped cleanly in
  `--dry-run`/`config check` without a runtime.

---

## Template integration (`.Ports`)

The existing `{{ }}` template machinery (`internal/profile/paths.go`,
`renderTemplate`) renders mount sources/targets and cache targets against
`.Env` (host environment) and `.UID`. It gains a third context field:

- **`.Ports`** — `map[string]string`, key = container port (`"8080"`),
  value = the host port the instance will publish (`"5173"`, or the allocated
  port for auto entries). Present for every entry in the resolved `ports`
  map, fixed or auto.

Template rendering additionally extends to:

- `environment` values: `PORT: '{{ index .Ports "8080" }}'`
  (empty value still means host-env passthrough; a value starting with `{{`
  is a template expression).
- `command` and `args_if_none` args:
  `command: ["opencode", "web", "--port", "{{ index .Ports \"8080\" }}"]`.

**Rule:** rendering has two modes, by field family:

| Field family | Template iff | Reason |
|---|---|---|
| `mounts` sources/targets, `caches` targets (existing) | contains `{{` | backward compatible with shipped profiles (`~/dev/{{ .Env.X }}` must keep working) |
| `environment` values, `command`/`args_if_none` args (new) | starts with `{{` | literal `{{` mid-string is common in shell snippets (jq filters, heredocs); a leading `{{` is unambiguous |

The asymmetry is intentional: the starts-with rule cannot be applied
retroactively to mounts without breaking existing profiles, and the
contains rule would break shell commands containing literal `{{`.

**Allocation timing:** `.Ports` must be populated **before** templates render,
so port allocation happens in `buildSpec` immediately before
`ResolveTildes`. Dry-run allocates too (ephemeral bind, harmless) so
`--dry-run` output is faithful.

---

## Merge semantics (extends)

Both are maps keyed by their natural identity (container port; container
device path), so they follow the §4.3 map rule: child overrides parent at
matching key, child `null` deletes an inherited entry, no list-concat
pitfalls. `ports` uses `mergePortMap` (struct value, like `mergeMounts`);
`devices` uses `mergeDeviceMap` (struct value, like `mergeMounts`).

This does require extending the merge machinery:

- `internal/profile/catalog.go` `collectNullKeys` hardcodes the tracked fields
  (`mounts, environment, tools, caches, labels` today). Add `ports` and
  `devices` to that set so explicit-null children are captured for delete.
- `MergeProfiles` (merge.go): add `mergePortMap` and `mergeDeviceMap`
  helpers (struct value — modeled on `mergeMounts`), wired into
  `MergeProfiles`. Reuse the existing `nullKeys["*"]` whole-field-null
  sentinel for "delete the entire inherited map."
- `RawProfile.NullKeys` is `map[string]map[string]bool` — no type change
  needed; just two new keys.

---

## Runtime translation (spec §3.2/§5)

`pkg/toolpod/spec.go` `Profile` → `Spec` adds:

```go
type PortSpec struct {
    HostIP    string // "127.0.0.1", "0.0.0.0", or "" (all interfaces)
    HostPort  string // "5173"; auto entries are filled by toolpod in buildSpec
    Container string // "8080" (container port, no protocol)
    Protocol  string // "tcp" | "udp" | "sctp"
}
type DeviceSpec struct {
    Container string
    Host      string
    Perms     string // "rwm"
    Cgroup    bool
}
```

**Port allocation** (new, client-side): for every `PortSpec` with empty
`HostPort`, bind an ephemeral socket (`net.Listen("tcp", hostIp+":0")` for
tcp; `net.ListenPacket("udp", ...)` for udp), read the port, close the
socket, and use it as the explicit `HostPort`. `sctp` cannot be allocated
client-side (no Go stdlib support) and is rejected at validation when `host`
is absent. Known race: the port can be taken between close and daemon bind;
start then fails cleanly with the engine's bind error. The allocator is a
small injectable function on `LaunchOpts` for deterministic tests.

`internal/runtime` maps to the Docker Engine API create call:
- `Config.ExposedPorts[<container>/<protocol>] = struct{}{}` and
  `HostConfig.PortBindings[<container>/<protocol>] = []nat.PortBinding{
  {HostIP: HostIP, HostPort: HostPort}}`. `HostPort` is always explicit after
  allocation.
- `HostConfig.Devices = []container.DeviceMapping{{
  PathOnHost: Host, PathInContainer: Container,
  CgroupPermissions: Perms}}` (static node map; this already grants cgroup
  access to that node on modern Docker/Podman).
- Only when `Device.Cgroup == true`: append to
  `HostConfig.DeviceCgroupRules`, scoped to the device's major:minor where
  derivable at `Prepare` (parse `/sys/dev/char`/`/sys/dev/block`), else
  `["c <major>:*"]` per device. Never emit the blanket `["c *:*"]`.
  **Limitation:** in environments without the device node in sysfs
  (containers, some VMs) the major-only fallback applies; toolpod logs a
  warning naming the device whenever it falls back, and the rule remains
  scoped to a single major — never all devices.

Mode A vs Mode B is transparent here: the Docker-compatible Podman API honors
the same fields, so no mode-specific translation is needed.

**Post-start output:** after the container starts, print
`listening on <proto>://<host_ip or 127.0.0.1>:<host_port>` per published
binding via the progress writer (stderr), so concurrent multi-instance
launches are discoverable by the user. The scheme is `tcp://`/`udp://`/
`sctp://`, never an assumed `http://`.

**Determinism:** `buildSpec` iterates the maps into slices. Sort `PortSpecs`
(by container port, then protocol, then host port) and `DeviceSpecs` (by
container path) so `--dry-run`/`--verbose` output and tests are stable.

---

## Files to touch (draft — refined in writing-plans)

- `internal/profile/types.go` — add `Ports map[string]PortBind` and
  `Devices map[string]DeviceBind` to `Profile`; add `PortBind{Host,
  HostIP, Protocol string}` and `DeviceBind{Source, Permissions string;
  Cgroup bool}` structs (value types for the YAML map).
- `internal/profile/catalog.go` — add `ports`/`devices` to `collectNullKeys`.
- `internal/profile/merge.go` — add `mergePortMap`/`mergeDeviceMap` (struct
  value, like `mergeMounts`); wire into `MergeProfiles`.
- `internal/profile/validate.go` — port range checks, protocol enum, host
  normalization, `network: host` + non-empty `ports` warning.
- `internal/profile/paths.go` — add `.Ports` to `tmplData`; thread the
  allocated ports map through `ResolveTildes`; extend rendering to
  `environment` values and `command`/`args_if_none` args with the
  starts-with-`{{` rule.
- `pkg/toolpod/spec.go` (`runtime.Spec`) — add `PortSpecs []PortSpec` and
  `DeviceSpecs []DeviceSpec`; allocate auto host ports in `buildSpec` (before
  `ResolveTildes`), populate `.Ports`, **sort for determinism**.
- `pkg/toolpod/launch.go` — `LaunchOpts` gains the injectable port allocator
  (defaults to `net.Listen` ephemeral bind).
- `internal/runtime/runtime.go` — add `PortSpec`/`DeviceSpec` types and
  `PortSpecs`/`DeviceSpecs` fields on `Spec` (the single home for spec types).
- `internal/runtime/docker_run.go` — set `ExposedPorts`/`PortBindings`/
  `Devices`/`DeviceCgroupRules`; print `listening at ...` lines via progress
  writer after start.
- `pkg/toolpod/types.go` — add `PortSpec = runtime.PortSpec` and
  `DeviceSpec = runtime.DeviceSpec` aliases (mirrors the existing
  `MountSpec = runtime.MountSpec` re-export); no second type definition.
- `pkg/toolpod/dryrun.go` (`RenderSpec`) — emit `ports:` and `devices:`
  sections (sorted), mirroring the existing `mounts:` block style.
- Tests: `internal/profile/merge_test.go` (null-to-delete on both maps +
  `extends` override chain), `internal/profile/validate_test.go` (ranges,
  protocol enum, `network:host` warn), `internal/profile/paths_test.go`
  (`.Ports` rendering in env/command, starts-with-`{{` rule, literal `{{`
  passthrough), `pkg/toolpod/spec_test.go` (allocation + `.Ports` population
  + sort), dry-run rendering.
- Built-ins: unchanged.
- README: document `ports`/`devices` and the `.Ports` template values.

---

## Test plan

- Unit: `ports` key/value validation — range checks (1–65535), protocol
  enum, `host: 0`/missing → auto, int/string normalization → correct
  `PortSpec`. Invalid values fail at load (`ProfileError` exit 2); `sctp`
  without explicit `host` fails at load (validation-only — no runtime
  sctp test).
- Unit: `devices` defaults (`source` = key, `cgroup` defaults **false**,
  `permissions` default `rwm`); per-field override.
- Unit: null-to-delete on both maps (single key and whole-field `"*"`).
- Unit: `extends` chain override + delete across both new maps (parent ports
  inherited, child overrides one, deletes another).
- Unit: template rendering — `.Ports` resolves in `environment` values and
  `command` args; non-`{{`-prefixed strings stay literal (incl. a string
  containing literal `{{` mid-string).
- Unit: `buildSpec` — auto host ports allocated (fake allocator), fixed
  ports pass through, `.Ports` populated with both, **sorted** slice order.
- Unit: `RenderSpec` emits `ports:` and `devices:` blocks; `network: host`
  + ports emits a warning line.
- (Gated) integration: a profile publishing a fixed port actually maps it
  (curl the host port → container response); an auto port allocates
  successfully; a non-existent `devices.source` is warned-and-skipped at
  `Prepare`, not a config-check failure.
- (Gated) integration: two concurrent launches of the same profile with
  `ports: {8080: {}}` get distinct host ports.
- (Gated) integration: `cgroup: true` emits a scoped (major:min) rule, not
  `c *:*`.
