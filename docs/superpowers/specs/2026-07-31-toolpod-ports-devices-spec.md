# `ports` + `devices` Profile Schema — Spec

**Status:** Draft for review
**Date:** 2026-07-31
**Validated against:** toolpod working tree: `internal/profile`
(`types.go`, `merge.go`, `validate.go`, `catalog.go`), `pkg/toolpod`
(`spec.go`, `launch.go`, `dryrun.go`), `internal/runtime/runtime.go`,
`internal/catalog`, design doc §4.1/§4.3/§4.4. The `dryrun.go` renderer and
merge/`collectNullKeys` call sites were read to confirm the integration
points described below.

## Summary

Add two optional, escape-hatch schema fields to the profile config so a profile
can publish ports to the host and attach host devices (GPU, USB, `/dev/fuse`,
etc.) to the container. Both are **opt-in**: existing profiles and built-ins are
unaffected; the fields default to empty and are absent from `shell`/`opencode`/
`codex`.

These are the two highest-priority gaps (beyond `image`/`build`/`command`/
`mounts`/`environment`/`tools`) that rootless-Podman+mise does **not** cover and
that developers hit frequently: exposing a local service/preview and attaching
hardware or privileged device cgroup rules.

```yaml
version: 1
extends: shell
command: ["bash"]
ports:
  "127.0.0.1:8080:80": {}   # ip:hostPort:containerPort
  "9000": {}                # container 9000/tcp; host port auto-allocated
  "5173:5173": {}           # host 5173 -> container 5173
  "53:53/udp": {}           # udp example
  # null removes an inherited binding:  "8080:80": null
devices:
  /dev/fuse: {}             # source defaults to the key path on the host
  /dev/nvidia0: { permissions: rwm }
  /dev/bus/usb: { source: /dev/bus/usb, cgroup: true }  # see cgroup note
  # null removes an inherited device:  /dev/fuse: null
```

---

## Goals

1. Add `ports` supporting compose-compatible short-syntax bindings
   (`[ip:]hostPort:containerPort[/protocol]`, plus the host-port-omitted and
   host-port-`0` variants) — the form every dev already knows.
2. Add `devices` supporting container-device-keyed entries with an optional
   host `source`, `permissions` (rwm), and an opt-in `cgroup` flag for broad
   device-cgroup allow-rules (advanced: DinD/GPU-enum). The common case needs
   only `source`/`permissions` — see the cgroup note.
3. Integrate with the existing merge semantics (keyed maps, null-to-delete)
   so `extends` works transparently.
4. Keep built-ins minimal — no field added to `shell`/`opencode`/`codex`.
5. Surface bindings in `--dry-run`/`--verbose` output and in `doctor`/`config
   show --resolved`.

## Non-Goals

1. No new `ports:`/`devices:` on built-in profiles.
2. No validation that host devices/ports exist at `config check` time — those
   are host-state checks done at `Prepare`/launch (skip cleanly in CI /
   `--dry-run` without a runtime).
3. No compose-style top-level `ports`/`devices` service dependencies, health
   ports, or `expose`. Pure host↔container bindings only.
4. No `network: host` interaction beyond a warning that `ports:` are redundant
   under host networking.

---

## Schema

### `ports` (optional map)

- **Key** = the port binding expression. Compose short syntax:
  - `[HOST_IP:]HOST_PORT:CON_PORT[/PROTOCOL]`
  - If `HOST_PORT` is omitted, an unused host port is allocated (`0`).
  - `HOST_PORT == 0` → also random allocation.
- **Value** = empty map `{}` or `map{ protocol?: tcp|udp|sctp }`
  (`protocol` is an alternative to the key `/proto` suffix; specifying both
  with conflicting values is a validation error — see Validation).
- **Map identity key** = the full key string. Merge replaces per-key; `null`
  deletes an inherited binding (reuse §4.3 null-to-delete rule).

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

- **Each `ports` key is parsed at load/validation time** (no runtime needed),
  so `doctor`/`config check` fast-fail on malformed bindings before any
  launch. On parse failure return a `ProfileError{Path, Message}` naming the
  offending key.
- Default protocol `tcp` when neither key-suffix nor value `protocol` is set.
- If both key-suffix `/proto` and value `protocol` are set and **differ**,
  it is a validation error (ambiguous) rather than silent value-wins.
- `network: host` + non-empty `ports` → warn (not error) to stderr (ports are
  redundant under host networking).
- `devices` with `source` referencing a host path that does not exist → not
  a config error; checked at `Prepare` (host state), skipped cleanly in
  `--dry-run`/`config check` without a runtime.

---

## Merge semantics (extends)

Both are maps keyed by their natural identity (binding string; container device
path), so they follow the §4.3 map rule: child overrides parent at matching
key, child `null` deletes an inherited entry, no list-concat pitfalls.

This **does require extending the merge machinery** (correcting an earlier
draft that claimed "no change to `mergeProfiles`"):

- `internal/profile/catalog.go` `collectNullKeys` hardcodes the tracked fields
  (`mounts, environment, tools, caches, labels` today). Add `ports` and
  `devices` to that set so explicit-null children are captured for delete.
- `MergeProfiles` (merge.go) calls `mergeMounts` for struct-valued maps and
  `mergeStringMap` for string-valued maps. Add a `mergeDeviceMap` helper
  (struct value — like `mergeMounts`) for `devices`, and a `mergePortMap`
  helper (struct value) for `ports`. Reuse the existing `nullKeys["*"]`
  whole-field-null sentinel for "delete the entire inherited map."
- `RawProfile.NullKeys` is `map[string]map[string]bool` — no type change
  needed; just two new keys.

---

## Runtime translation (spec §3.2/§5)

`pkg/toolpod/spec.go` `Profile` → `Spec` adds:

```go
type PortSpec struct {
    HostIP   string // "127.0.0.1", "0.0.0.0", or "" (all interfaces / auto)
    HostPort string // "8080", or "" (auto-allocate)
    Container string // "80" (port only; no protocol)
    Protocol  string // "tcp" | "udp" | "sctp"
}
type DeviceSpec struct {
    Container string
    Host      string
    Perms     string // "rwm"
    Cgroup    bool
}
```

`internal/runtime` maps these to the Docker Engine API create call:
- `Config.ExposedPorts[<container>/<protocol>] = struct{}{}` and
  `HostConfig.PortBindings[<container>/<protocol>] = []nat.PortBinding{
  {HostIP: HostIP, HostPort: HostPort}}`. Empty `HostPort` → runtime allocates.
- `HostConfig.Devices = []container.DeviceMapping{{
  PathOnHost: Host, PathInContainer: Container,
  CgroupPermissions: Perms}}` (static node map; this already grants cgroup
  access to that node on modern Docker/Podman).
- Only when `Device.Cgroup == true`: append to
  `HostConfig.DeviceCgroupRules`, scoped to the device's major:minor where
  derivable at `Prepare` (parse `/sys/dev/char`/`/sys/dev/block`), else
  `["c <major>:*"]` per device. Never emit the blanket `["c *:*"]`.

Mode A vs Mode B is transparent here: the Docker-compatible Podman API honors
the same fields, so no mode-specific translation is needed.

**Determinism:** `buildSpec` iterates the maps into slices. Sort `PortSpecs`
(by container port, then host IP, then host port) and `DeviceSpecs` (by
container path) so `--dry-run`/`--verbose` output and tests are stable.

---

## Files to touch (draft — refined in writing-plans)

- `internal/profile/types.go` — add `Ports map[string]PortBind` and
  `Devices map[string]DeviceBind` to `Profile`; add `PortBind`/`DeviceBind`
  structs (value types for the YAML map).
- `internal/profile/catalog.go` — add `ports`/`devices` to `collectNullKeys`.
- `internal/profile/merge.go` — add `mergePortMap`/`mergeDeviceMap` (struct
  value, like `mergeMounts`); wire into `MergeProfiles`.
- `internal/profile/validate.go` — **fast-fail `ports` key parse at load**
  (no runtime) so `doctor`/`config check` catch malformed bindings;
  protocol-conflict check; `network: host` + non-empty `ports` warning.
- `pkg/toolpod/spec.go` (`runtime.Spec`) — add `PortSpecs []PortSpec` and
  `DeviceSpecs []DeviceSpec`; populate in `buildSpec`; **sort for determinism**.
- `internal/runtime/runtime.go` — add `PortSpec`/`DeviceSpec` types and
  `PortSpecs`/`DeviceSpecs` fields on `Spec` (the single home for spec types).
- `pkg/toolpod/types.go` — add `PortSpec = runtime.PortSpec` and
  `DeviceSpec = runtime.DeviceSpec` aliases (mirrors the existing
  `MountSpec = runtime.MountSpec` re-export); no second type definition.
- `pkg/toolpod/dryrun.go` (`RenderSpec`) — emit `ports:` and `devices:`
  sections (sorted), mirroring the existing `mounts:` block style.
- Tests: `internal/profile/merge_test.go` (null-to-delete on both maps +
  `extends` override chain), `internal/profile/validate_test.go` (key parse,
  protocol conflict, `network:host` warn), `pkg/toolpod/spec_test.go`
  (shorthand expansion + sort), dry-run rendering.
- Built-ins: unchanged.
- Design doc §4.1/§4.3: document the two fields + their merge identity.

---

## Test plan

- Unit: `ports` key parse — all shorthand forms
  (`ip:h:c`, `h:c`, `c`, `/udp` suffix, host-port `0`) → correct
  `HostIP/HostPort/Container/Protocol`; **malformed key fails at validation
  with no runtime** (`ProfileError` exit 2) so `doctor`/`config check` catch it.
- Unit: `protocol` conflict (key `/udp` + value `protocol: tcp`) → error.
- Unit: `devices` defaults (`source` = key, `cgroup` defaults **false**,
  `permissions` default `rwm`); per-field override.
- Unit: null-to-delete on both maps (single key and whole-field `"*"`).
- Unit: `extends` chain override + delete across both new maps (parent ports
  inherited, child overrides one, deletes another).
- Unit: `buildSpec` expansion + **sort** (stable slice order for ports and
  devices).
- Unit: `RenderSpec` emits `ports:` and `devices:` blocks; `network: host`
  + ports emits a warning line.
- (Gated) integration: a profile publishing a port actually maps it
  (curl the host port → container response); a non-existent `devices.source`
  is warned-and-skipped at `Prepare`, not a config-check failure.
- (Gated) integration: `cgroup: true` emits a scoped (major:min) rule, not
  `c *:*`.
