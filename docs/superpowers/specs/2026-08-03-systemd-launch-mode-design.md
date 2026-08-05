# Systemd Launch Mode — Spec (standalone deliverable)

**Status:** Draft for review
**Date:** 2026-08-03
**Validated against:** toolpod working tree: `internal/profile`
(`types.go`, `merge.go`, `validate.go`, `catalog.go`), `pkg/tpd`
(`spec.go`, `launch.go`, `dryrun.go`), `internal/runtime/docker_run.go`,
`internal/catalog`, `cmd/tpd/cli.go`.

## Summary

Add an opt-in **systemd launch mode** so a profile can run systemd as PID 1 in
the container and manage long-lived services — most importantly a nested
rootless Podman engine reachable over `podman.socket` via systemd socket
activation. Today every profile boots a tini-wrapped, host-user command
(`docker_run.go`); there is no way to keep a background engine alive
independently of the profile command. Systemd mode fills that gap with a
real init system instead of a hand-rolled one.

**Scope (standalone):** this plan delivers the systemd launch mode itself and
a `podman-nested` fragment that demonstrates it with **per-instance,
ephemeral** nested-podman storage — concurrent-safe by default. Sharing a warm
image store across concurrent instances (the sequential cache we rejected) is a
separate follow-up (see *Follow-up work*), designed after this lands.

The mode is gated purely on a new `systemd:` field:

```yaml
version: 1
systemd:
  enable:
    - podman.socket
```

- A resolved profile **with** `systemd:` boots `/sbin/init` (systemd as PID 1),
  brings up the user manager for the host user, enables/start the listed
  units, and then runs the profile command through the Docker exec API — so
  the tool keeps today's execution model (host-user identity, exit-code
  mapping, TTY, passthrough args).
- A resolved profile **without** `systemd:` is byte-for-byte unchanged: tini
  init, bootstrap → setpriv → attach, as today.

Built-in fragment `podman-nested` uses the mode to run an **isolated** nested
rootless Podman (socket-activated; storage is per-launch ephemeral in the
container's writable layer, so concurrent instances never share a store), the
host engine is never touched. This is deliberately distinct from the existing
`podman` fragment, which mounts the host's Podman socket.

```yaml
version: 1
systemd:
  enable: [podman.socket]
packages: [systemd, podman, fuse-overlayfs, uidmap, slirp4netns]
environment:
  DOCKER_HOST: unix:///run/user/{{ uid }}/podman/podman.sock
  PODMAN_HOST: unix:///run/user/{{ uid }}/podman/podman.sock
caches:
  containers: [~/.local/share/containers, ~/.config/containers]
```

---

## Goals

1. Add `systemd:` to the profile schema; a resolved profile that sets it
   launches in systemd mode, all others keep the current tini flow.
2. Boot systemd as PID 1 (container `Cmd` `/sbin/init`, no tini), with the
   HostConfig a systemd container needs: tmpfs on `/run`, `/run/lock`, `/tmp`,
   and a rw `/sys/fs/cgroup` bind. **No `--privileged`** — rootless Podman +
   systemd is supported well enough today that privilege is not required, and
   the host-user model must not be weakened. (Nested podman *may* turn out to
   need `privileged`; see the risk register — decided by the validation spike,
   not assumed.)
3. Keep the execution model: keep-id host-user identity, workspace mounted at
   the host path, profile command run as the host user after boot with exit
   code mapped, TTY/interactive preserved, passthrough args and `-c` honoured.
4. Run the units listed in `systemd.enable` via the user manager
   (`systemctl --user enable --now <unit>`), so `.socket` units get real
   socket activation and `.service` units start.
5. Ship the `podman-nested` fragment: systemd + nested rootless Podman,
   socket-activated, with persistent storage via a cache volume.
6. Surface systemd-mode risk honestly: validate on the primary target
   (rootless Podman) before implementing; document engine caveats.

## Non-Goals

1. No systemd mode when `systemd:` is absent — the tini path is not reworked
   for ordinary profiles.
2. No systemd for the **rootful** (Mode B) fallback beyond best-effort: the
   documented guarantee is rootless Podman. Docker/rootful Podman systemd-mode
   support is documented as "validated on rootless Podman; others best-effort"
   until the spike says otherwise.
3. No `--privileged` in systemd mode by default. If the spike proves nested
   podman needs it, the *fragment* opts in via the `privileged` schema field;
   systemd mode itself stays unprivileged.
4. No replacement for `tini`/`Init: true` in non-systemd profiles.
5. No supervision/restart-policy UI, no `systemd.timers`, no unit-file
   templating beyond the existing `{{ }}` in `files:`.
6. No logd: systemd/journald is not configured to persist; service logs go to
   the container's console only for debugging. `Storage=volatile` (or
   equivalent) is left to the profile.
7. No nested-podman persistence of running containers across tpd launches —
   the *storage* (images, volumes) is cached, running state is per-launch.
8. No **concurrent** sharing of the nested-podman storage cache. Podman's
   graphroot is not safe for two live engines: the store lock lives in the
   per-instance runroot, so separate instances don't coordinate (parallel jobs
   hit `database is locked` even against a single local store). The cache
   volume is sequential-warm only; two concurrent `podman-nested` launches
   sharing it would corrupt or thrash. Solving this (read-only shared
   `imagestore` + per-instance writable graphroot, the standard HPC pattern)
   is explicitly out of scope for this feature.

---

## Schema

### `systemd` (optional map)

- **Key:** `enable` → list of unit names (`podman.socket`). Units live in the
  user unit dir and are materialized via the existing `files:` mechanism
  (`~/.config/systemd/user/<unit>`), so `files:` + `systemd.enable` are the
  two halves of declaring a service.
- **Value:** list of unit names; empty list is equivalent to absent.
- **Merge:** key-by-key map merge; `enable` appends + dedups (like
  `packages`), `null` clears the inherited list. Only the top-level key is
  `enable` today (schema-ready for future keys).
- **Validation:** unit names must match `[a-zA-Z0-9._@-]+\.(service|socket|path|timer)`.
  A fragment may set `systemd:` (the fragment rule about no `image`/`command`
  is unchanged).

### `privileged` (optional bool)

- **Schema field only; not used by systemd mode.** Scalar merge (child wins,
  `null` deletes). Plumbed `Profile → Spec → HostConfig.Privileged`. Landed
  now because the risk register shows nested podman may need it; the
  `podman-nested` fragment starts **without** it and flips on only if the
  spike proves it necessary.
- Advisory: any resolved profile with `privileged: true` surfaces a warning in
  `tpd show`/`tpd init` (extend the advisory table path), mirroring the
  sensitive-fragment advisories.

### Merge machinery

- `collectNullKeys` (`internal/profile/catalog.go`) gains `systemd` (map) and
  `privileged` is scalar (no null tracking needed beyond the existing scalar
  rule).
- `MergeProfiles` (`merge.go`): `systemd` merges key-by-key with append+dedup
  for `enable`; `privileged` is a plain scalar copy.

---

## Systemd launch mode — runtime

New code path in `internal/runtime`; ordinary profiles never enter it. A
resolved profile enters systemd mode iff the merged `systemd.enable` is
non-empty.

### Container create

- `Config.Cmd = ["/sbin/init"]`, `Config.Entrypoint = []string{}`,
  `HostConfig.Init = false` (no tini — podman's own docs warn `--init` mounts
  over `/run` and breaks systemd containers).
- `HostConfig.Tmpfs`: `/run`, `/run/lock`, `/tmp` (size-capped).
- `HostConfig.Mounts`: add a rw bind of `/sys/fs/cgroup` (cgroup v2 host; on a
  v1 host this is a documented caveat, not a supported path).
- `HostConfig.Privileged` = spec `privileged` (defaults false).
- keep-id userns, host-user identity, workspace mount, cache volumes, `files:`
  materialization, env — all identical to today.
- The `Env` the container starts with must set `HOME` (already done), and the
  exec phase sets `XDG_RUNTIME_DIR=/run/user/<uid>` for the user command.

### Boot sequence (exec'd as container root, after `ContainerStart`)

Waits for systemd to be ready (poll `systemctl is-system-running` /
`is-running`), then:

1. `mkdir -p` + `chown` the runtime home and `/run/user/<uid>` (0600/0700).
   The runtime-home bootstrap that today runs in the wrapper command moves
   here for systemd profiles.
2. Append `<uid>:100000:65536` to `/etc/subuid` and `/etc/subgid` so the
   nested rootless podman can map ranges (keep-id already maps the host user's
   subuids into the container, so this is consistent).
3. `systemctl start user@<uid>.service` — bring up the user manager for the
   host user (no logind needed; `user@.service` is a template unit).
4. For each unit in `systemd.enable`: `systemctl --user enable --now <unit>`
   exec'd as the host user with `XDG_RUNTIME_DIR=/run/user/<uid>`. `--now`
   starts `.service` units immediately and starts the listener for `.socket`
   units.

### Profile command execution (exec, not attach)

After boot, the profile command runs through `ContainerExec` as the host user
(`User: "<uid>:<gid>"`, container uids), preserving today's construction:
`cd <workspace> && <mise activate> && <backend-runtime-setup> && <plugin install>
&& mise install && exec <userCmd>` (the `mise install`/activate chain from
`docker_run.go:43-71`). TTY when interactive, raw-mode handling, SIGWINCH
resize — all reused. The exec's hijacked stream replaces the container attach
as the output pump; the exec exit code is tpd's exit code.

Signals: SIGINT/SIGTERM still kill the container (`ContainerKill`, today's
behavior); systemd at PID 1 propagates to services and the exec'd command.

Teardown unchanged: force-remove the container on exit (`AutoRemove: false` +
explicit `ContainerRemove` in the defer), pruning the ephemeral systemd boot
state. Derived-image and volume lifecycle are identical to today.

---

## Built-in fragment `podman-nested`

`internal/catalog/fragments/podman-nested.yaml`:

```yaml
version: 1
systemd:
  enable: [podman.socket]
packages: [systemd, podman, fuse-overlayfs, uidmap, slirp4netns]
environment:
  DOCKER_HOST: unix:///run/user/{{ uid }}/podman/podman.sock
  PODMAN_HOST: unix:///run/user/{{ uid }}/podman/podman.sock
caches:
  containers: [~/.local/share/containers, ~/.config/containers]
```

- `packages:` installs podman + userspace deps into the derived image (systemd
  included so `/sbin/init` exists). The mise appimage `podman` tool is *not*
  used — the distro package is the coherent set (conmon, storage drivers,
  user units). Debian ships `podman.socket`/`podman.service` user units.
- `systemd.enable` gives real socket activation: the first `podman`/compose
  call starts the engine; no pre-started service, no polling.
- `environment` points `DOCKER_HOST`/`PODMAN_HOST` at the inner socket. Env
  values are literal (no shell expansion), so the path is templated:
  `unix:///run/user/{{ uid }}/podman/podman.sock` — the user manager
  guarantees `/run/user/<uid>`.
- `caches` keeps images/volumes warm across **sequential** launches;
  `~/.config/containers` covers storage config. **Not** safe for two
  concurrently running instances (see Non-Goals) — podman's graphroot lock
  lives in the per-instance runroot, so a second engine on the same store
  fails with `database is locked`. Only one `podman-nested` launch at a time.
- Advisory (`internal/catalog/advisories.go`): "runs a nested rootless Podman
  engine inside the container — an isolated engine, no host access". Note the
  contrast with the existing `podman` fragment advisory (host socket).

Because it is a fragment (no `image`/`command`), users compose it into any
profile; the profile supplies the command (e.g. `extends: [mise,
podman-nested]` + `command: ["bash", "-l"]`).

---

## Risk register (validation-first)

The runtime behaves differently per engine around cgroups; the spike must
settle each before the full implementation is built. Rootless Podman is the
guaranteed target; everything else is documented caveats.

| Risk | Question the spike answers | Fallback if it fails |
|---|---|---|
| systemd as PID 1 under keep-id | Does `/sbin/init` boot cleanly in a keep-id userns container on rootless Podman? | Move to rootful Mode B for systemd profiles (documented, workspace at `/workspace`) |
| user manager without logind | Does `systemctl start user@<uid>.service` work in a container without logind/PAM sessions? | Boot script starts `systemd --user` directly |
| socket activation | Does `systemctl --user enable --now podman.socket` activate on first connect? | Add `podman system service` background start to the boot sequence (accepting the socket-activation loss) |
| nested rootless podman | Does `podman run` work inside (userns, fuse-overlayfs, subuid, networking)? | Add `privileged: true` to the fragment only |
| cgroup delegation | Is `/sys/fs/cgroup` rw sufficient under rootless Podman's delegated subtree? | Document engine caveat; adjust mounts |
| `--init` conflict | Confirm removing tini is safe (systemd reaps). | n/a — required |

The implementation plan leads with a throwaway spike container (built with
`podman run` directly, not via tpd) that answers every row, then proceeds
only on green results. This machine has no Podman, so the spike runs on the
user's host.

---

## Files to touch (draft — refined in writing-plans)

- `internal/profile/types.go` — `Systemd *SystemdConfig` and
  `Privileged bool` on `Profile`; `SystemdConfig{Enable []string}`.
- `internal/profile/catalog.go` — track `systemd` in `collectNullKeys`.
- `internal/profile/merge.go` — merge `systemd` (map, `enable` append+dedup),
  `privileged` (scalar).
- `internal/profile/validate.go` — unit-name pattern; bool coercion.
- `pkg/tpd/spec.go` — plumb `Systemd`/`Privileged` into `runtime.Spec`;
  `Systemd bool` mode flag (resolved `enable` non-empty).
- `pkg/tpd/types.go` — aliases if needed.
- `internal/runtime` — new systemd-mode path in `docker_run.go`:
  `/sbin/init` create, boot-poll + bootstrap exec, `user@<uid>` start,
  `systemctl --user enable --now`, `ContainerExec` for the command, exec
  stream pump, exec exit-code mapping.
- `internal/runtime/docker.go` — nothing unless engine detection needs
  `systemd` hints.
- `internal/catalog/fragments/podman-nested.yaml` — new fragment.
- `internal/catalog/advisories.go` — `podman-nested` advisory + privileged
  warning path.
- `cmd/tpd/` — dry-run/verbose render of `systemd:`; completion unchanged.
- Tests: `internal/profile/merge_test.go`, `validate_test.go`,
  `pkg/tpd/spec_test.go`, runtime unit tests with the fake client
  (`docker_test.go` fakes) covering the boot/exec sequence and exit-code
  mapping, `internal/catalog/catalog_test.go` fragment load + advisory.
- README + `docs/` security model: systemd mode, nested podman, engine caveats.

---

## Test plan

- Unit: `systemd` validation (unit-name grammar), merge (append+dedup,
  null-clear, key-by-key), absence → tini mode unchanged.
- Unit: spec plumbing — `systemd.enable` non-empty ⇒ mode flag set;
  `privileged` reaches `Spec`.
- Unit (fake client): systemd profile create call has `Cmd=["/sbin/init"]`,
  `Init=false`, tmpfs `/run`,`/run/lock`,`/tmp`, `/sys/fs/cgroup` rw bind,
  no privileged; boot-poll + bootstrap exec order; `systemctl --user enable
  --now` exec runs as the host user with `XDG_RUNTIME_DIR`; profile command
  runs via exec with the mise chain; exec exit code becomes the result.
- Unit: non-systemd profile produces the exact current create call (regression
  guard).
- Unit: `podman-nested` fragment resolves (extends, env, caches, systemd,
  packages) and `tpd show` renders `systemd:` + the advisory.
- (Gated) integration on rootless Podman: the spike results, then a systemd
  profile boots, `systemctl --user` works, `podman run` inside works, and the
  engine survives across multiple commands within one launch.
- (Gated) integration: nested storage persists across launches via the cache
  volume.
