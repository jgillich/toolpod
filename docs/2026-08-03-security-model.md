# tpd security model

This document records how tpd reasons about the host, and the trust and
operational trade-offs the hardening work accepts. It is a companion to the
[design notes](../AGENTS.md) and the code-review hardening plan
(`docs/superpowers/plans/2026-08-03-code-review-hardening.md`), which records
which review findings were adopted and why others were declined. Every claim
below reflects implemented code as of 2026-08-03, updated for the sensitive-field
approval system as of 2026-08-05.

## Trust model: profiles are trusted configuration

A profile is arbitrary code: its `command` runs in a container, its
`files:` are written into the container, and its `mounts:`/`caches:`/`ports:`
are applied to it. tpd does not sandbox the profile — a profile can run any
command, read anything it is mounted, and reach any network. **Only run
profiles you trust.**

The approval system now distinguishes contributions by origin: a sensitive
field (mount, device, environment, port, dbus talk/own, the `network` scalar,
or a service definition) written by a user-owned entry — one with no namespace,
from `~/.config/tpd/` — is trusted and runs ungated, while the same field
written by a built-in (`core/`) or remote-namespace catalog entry must be
approved at launch. That gate does not relax the guidance above: the profile's
own `command`, `files:`, and `packages:` remain trusted configuration, so only
run profiles you trust.

This is amplified by the built-in fragments. Several mount real host state
into the container:

| Fragment | Host access granted |
| --- | --- |
| `docker-host` | Docker socket, read-write — container processes can administer the daemon (host-root access on a rootful daemon). |
| `podman-host` | Podman socket, read-write — container processes can control the container engine. |
| `gui` | Host display, `/dev/dri`, X11 socket, and the specific Wayland socket named by `$WAYLAND_DISPLAY`. |
| `gui-runtime` | The entire `$XDG_RUNTIME_DIR` — audio, compositor, notification, and agent sockets. |
| `ssh`, `netrc`, `aws`, `azure`, `gcloud`, `github`, `gitlab`, `vault` | Host credentials, mounted read-only — any process in the profile can read them. |

The `podman` fragment is the complement: it grants **no host** access. Its
service sidecar runs an isolated **nested rootful Podman engine** and exposes
that engine's socket to the main container. The sidecar is launched
`privileged: true`, which in a rootless engine grants `CAP_SYS_ADMIN` plus all
devices *inside the container's user namespace* — it does not escape to the host
(a privileged rootless container is not host root). The privilege is required:
an unprivileged sidecar cannot run a nested engine (the kernel blocks the nested
user namespace's `/proc` mount, and there is no `/dev/net/tun` for networking).
Everything the nested engine builds or runs is contained in the sidecar; it
never touches the host daemon. It grants no host capability to approve — the
service definition itself is core-contributed and still appears in the launch
dialog.

The sensitive fragments are gated by the approval system at launch, not by a
static advisory: the dialog described in "Sensitive-field approvals" below is
the single source of sensitivity information, so the capability is decided
before launch, not discovered after. There is no per-mount risk grading: that
table would go stale, and the review explicitly declined it.

## Ownership labels and what `prune` removes

Every volume, derived image, and launched container tpd creates for a launch
carries `tpd.managed=true` (`runtime.OwnershipLabel`,
`internal/runtime/labels.go`). Derived images additionally carry `tpd.build=1`
build provenance (`internal/runtime/docker_build.go`).

Transient diagnostic/helper resources are deliberately unlabeled: the doctor
probe volume and container (`tpd-diag-*`, `internal/doctor/checks.go`), the
cache subpath helper (`ensureCacheSubpaths`,
`internal/runtime/docker_prepare.go`), and the image-file probe
(`readImageFile`, `internal/runtime/extrepo.go`). Each is created and removed
synchronously, so a leftover one is only a failed-cleanup straggler. Because
prune's running-container protection and doctor's leaked-container check are
label-filtered, neither sees a stray helper.

`tpd prune` removes **only labeled resources**. `listTpdVolumes` and
`listTpdImages` in `internal/prune/prune.go` require the label; an unlabeled
`tpd-*` volume or image (legacy cruft, or something another tool created with
a matching name) is reported to stderr as `warning: skipping unlabeled tpd-*
resource <name> (not tpd-owned)` and is never auto-removed. The label, not the
name prefix, is the ownership claim.

Prune also never removes a resource referenced by a running container: before
any removal it lists tpd-labeled containers (`runningContainerRefs`) and skips
volumes/images they reference, and the engine independently refuses
force-removal of an image a running container uses.

## Cleanup: one bounded attempt, no retry (L-03)

The review's recommendation to retry bounded container cleanup was declined.
Cleanup already runs once, inside a 10s bounded background context, for the
launched container and every transient helper (`docker_run.go:127-131`,
`docker_prepare.go:111-115`). Retrying inside that window adds latency without
meaningful success — the engine state that broke a removal rarely clears in
10s. The underlying concern is handled another way: cleanup errors are surfaced
to stderr as a `tpd: warning: remove ...` line, and doctor gained
leaked-container and stale-dbus-socket checks.

## AppImage policy: `latest` with install-time digest verification

Built-in `appimage:` tools stay on `latest` — a pinned catalog would force
maintainers to bump versions and digests on every upstream release. Instead
the backend (`internal/mise/plugins/appimage/hooks/backend_install.lua`)
resolves `latest` to a concrete GitHub release **at install time** and
verifies the downloaded `.AppImage` against a published digest, in priority
order:

1. An explicit `sha256` in the profile's `tools:` entry (a scalar, or a
   per-arch map keyed by `amd64`/`aarch64`).
2. GitHub's per-asset `digest` field from the releases API (the `sha256:`
   prefix is stripped).
3. A checksum sidecar asset: `SHA256SUMS` or `<asset>.sha256`.

The install **fails closed** when none of these exists — an unverified
download is what the plugin exists to prevent. The resolved `{repo, version,
asset, digest}` is written to `.tpd-resolved` next to the install as an audit
trail. Because mise only re-runs install when the version directory is absent,
a machine stays on its first resolution: later launches reuse it even as
`latest` moves, while a fresh machine (empty mise volume) resolves the current
release.

Accepted trade-offs:

- The upstream-API digest is **self-referential**: the same GitHub response
  supplies both the download URL and its hash. This protects against
  download/CDN tampering but not against a compromised upstream or API.
- **Fresh vs old machines diverge**: two machines first-launched at different
  times may pin different `latest` releases. Explicit `sha256` in the profile
  opts out of both trade-offs.

## `repos:` trust anchor: TLS to pages.debian.net

The `extrepo` index and GPG signing keys are fetched over HTTPS from
`extrepo-team.pages.debian.net` (`internal/runtime/extrepo.go`). TLS to that
host is the trust anchor; the catalog's `gpg-key-checksum` is checked against
the key, but the checksum itself comes from the same catalog, so it is
**self-referential** — it catches a corrupted download, not a compromised
`pages.debian.net` serving both the catalog and a key it also re-checksumed.

Bounded reads and strict fetching bound the blast radius of a malicious
response: the index is capped at 8 MiB and keys at 256 KiB, redirects are
HTTPS-only and same-origin (a redirect to any other host is refused, capped at
5 hops), and non-200 statuses are errors. Pinning a catalog digest was
considered and declined: the index changes as repos are added and removed, and
a pin would require constant shipped updates. A compromised
`pages.debian.net` that serves both catalog and keys remains a residual risk.

## Base-image freshness and `--pull`

The derived image tag is content-addressed from the **requested** package
names and repo descriptors (`DerivedTag`, `internal/runtime/docker_build.go`),
not from resolved apt versions. This keeps the tag deterministic and means
`prune` never needs network access to match a derived image to a profile.
The cost: a derived image does not automatically pick up newer apt versions of
the same requested packages.

Mutable base tags (`latest` and friends) are pulled on first use and reused on
later launches. `tpd run --pull <profile>` re-pulls the base image even when
it is already present locally, refreshing mutable tags. Because the derived
tag hashes the local base image ID, pulling a new base version changes the
derived tag and the next launch rebuilds the derived image automatically.

## SELinux: `label=disable` when enforcing

When SELinux is enforcing, tpd launches containers with `--security-opt
label=disable` (`securityOpts`, `internal/runtime/docker_run.go`) so bind
mounted host paths — the workspace, home, dbus socket — keep their host labels
and stay readable inside the container. Relabeling with `:Z` was rejected
because it would relabel the user's own files and break host access to the
shared workspace. The trade-off is that tpd containers opt out of SELinux
label separation entirely on enforcing hosts; tpd's containment then relies on
the engine's userns/cgroup isolation rather than SELinux policy.

## Effective identity and the setpriv-absent fallback

Containers start as root so a bootstrap can create/chown `$HOME` and the
volume mount points, then drop to the host user via `setpriv` with all
capabilities removed (`--clear-groups --inh-caps=-all --bounding-set=-all`,
`wrapAsUser` in `internal/runtime/docker_run.go`). Rootless Podman pairs this
with `keep-id` so the dropped uid matches the host uid.

Images **without `setpriv` fall back to running un-dropped — as root** — with
an in-container stderr warning (`tpd: setpriv not found, running as root`).
Failing closed was considered and declined: it would break Mode B, where
rootful Docker runs as root by design. In Mode B the fallback is therefore the
documented behavior, not an anomaly; only the warning tells you when it was
reached on an image that did not expect it.

## Host-port allocation

When a profile publishes a port with no host port, the OS allocates an
ephemeral host port. No reservation is held: a collision would require another
process to bind that exact port between allocation and the engine's bind at
`ContainerStart` — unlikely on a single-user rootless host, and holding a
reservation until launch would only narrow that window, not close it (the
engine binds at start regardless). The residual race is accepted.

## Concurrent `prune --all --force`

There is no host-level lock between `prune` and `launch` (single-user rootless
mode; a user does not meaningfully run them concurrently). Guidance: do not run
`tpd prune --all --force` concurrently with a `tpd run`. The practical
defenses remain: prune skips resources referenced by running containers, and
the engine refuses force-removal of in-use resources, so a concurrent launch
that has already created its container wins the race.

## Sensitive-field approvals

The per-fragment advisories are gone; sensitivity information lives in one
place, the launch dialog. Any sensitive field — mounts, devices, environment,
ports, dbus talk/own, the `network` scalar, or a service definition —
contributed by a non-user catalog entry (`core/` built-in or remote namespace)
is gated: the dialog lists each unapproved item and the user keeps or drops it,
and dropping removes the field from the resolved profile (denying a service
also drops the top-level mounts that reference its socket). Choices persist
per-profile in `~/.local/share/tpd/approvals/<FullName>.yaml`, keyed by a
content hash of the profile's sensitive fields, so an edited definition is
re-prompted. `--yes` and `--no` approve or deny every unapproved item for
non-interactive use; with neither a dialog nor a flag, launch fails closed with
"unapproved sensitive fields require --yes or --no".

The credentials the gate exists for are unchanged: `ssh`, `netrc`, `aws`,
`azure`, `gcloud`, `github`, `gitlab`, and `vault` mount host credentials
**read-only**, but read-only does not mean hidden — any process running in the
profile can read the mounted files and tokens. Approving one is the same
decision as the trust model above: a profile that extends `vault` is a profile
you have just approved to read your Vault token.
