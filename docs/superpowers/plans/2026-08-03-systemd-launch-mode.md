# Systemd Launch Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in `systemd:` profile field that boots systemd as PID 1 and runs the profile command via the exec API, plus a `podman-nested` fragment demonstrating a socket-activated nested rootless Podman.

**Architecture:** A resolved profile with a non-empty `systemd.enable` takes a new runtime path in `internal/runtime`: the container is created with `Cmd: ["/sbin/init"]` (no tini), tmpfs on `/run`,`/run/lock`,`/tmp`; after start, tpd execs a bootstrap, starts the user manager (`user@<uid>.service`), enables the declared units via `systemctl --user enable --now`, then runs the profile command through a hijacked exec stream as the host user (same mise chain, TTY, and exit-code mapping as today). Profiles without `systemd:` keep the current tini/attach path untouched.

**Tech Stack:** Go 1.25, docker SDK `github.com/docker/docker v27.1.0`, cobra CLI. Tests use the httptest fake-Docker-server pattern already in `internal/runtime/docker_test.go`.

## Global Constraints

- Systemd mode is gated solely on `systemd.enable` being non-empty in the resolved profile; absence → current tini path byte-for-byte unchanged.
- Unit names must match `^[a-zA-Z0-9._@-]+\.(service|socket|path|timer)$`.
- `systemd.enable` merges append + dedup across `extends`; `systemd: null` (whole field) or `systemd: {enable: null}` clears inherited values.
- Systemd mode sets NO `--privileged`; `privileged` defaults to `false` and the `podman-nested` fragment does not set it.
- Target is rootless Podman (keep-id); Docker rootful/rootless and rootful Podman are best-effort (spike decides).
- Every task ends with `go test ./...` and `go vet ./...` green, then a conventional commit.
- No code comments except where the code doesn't make intent apparent (repo AGENTS.md).
- Docker SDK signatures (verified against v27.1.0): `ContainerExecCreate(ctx, container, container.ExecOptions) (types.IDResponse, error)`; `ContainerExecAttach(ctx, execID, container.ExecAttachOptions) (types.HijackedResponse, error)` (attach hijacks `/exec/{id}/start`, so it STARTS the exec — do not call `ContainerExecStart` when attaching); `ContainerExecInspect(ctx, execID) (container.ExecInspect, error)` with `.Running`/`.ExitCode`; `ContainerExecResize(ctx, execID, container.ResizeOptions) error`; `types.HijackedResponse.Close()` returns nothing.

---

### Task 1: Spike — validate the systemd container recipe on rootless Podman

**Files:**
- Create: `scripts/spike-systemd.sh`

**Interfaces:**
- Consumes: the user's host rootless Podman (`podman` on PATH, rootless socket).
- Produces: a green/red result per row of the risk register in the design spec (`docs/superpowers/specs/2026-08-03-systemd-launch-mode-design.md`), recorded at the top of this task's script output.

This machine has no Podman. The user runs this on a host with rootless Podman. The script builds a systemd image and exercises every risk-register row; the runtime tasks (7–9) may only proceed on green results for rows 1–4 (systemd boots, user manager, socket activation, nested podman). If a row fails, the implementer stops and reports back before writing runtime code.

- [ ] **Step 1: Write the spike script**

```bash
#!/usr/bin/env bash
set -euo pipefail
# Spike: validate the systemd-in-rootless-Podman recipe that systemd launch
# mode depends on. Run on a host with rootless podman; no tpd involved.
IMG="tpd-spike-systemd"
podman build -t "$IMG" -f - . <<'EOF'
FROM debian:13-slim
RUN apt-get update && apt-get install -y --no-install-recommends systemd dbus podman fuse-overlayfs uidmap slirp4netns \
 && rm -rf /var/lib/apt/lists/*
EOF

CTR="tpd-spike"
podman rm -f "$CTR" >/dev/null 2>&1 || true
podman run -d --name "$CTR" --userns=keep-id \
  --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
  "$IMG" /sbin/init

# row 1: systemd boots as PID 1 under keep-id
sleep 5
echo "== is-system-running: $(podman exec "$CTR" systemctl is-system-running || true)"

# row 2: user manager starts without logind
podman exec "$CTR" systemctl start "user@$(id -u).service"
for i in $(seq 1 20); do
  if podman exec "$CTR" test -S "/run/user/$(id -u)/systemd/private"; then break; fi
  sleep 1
done
echo "== user@ manager socket present: $?"

# row 3: socket activation
podman exec -u "$(id -u):$(id -g)" \
  -e "XDG_RUNTIME_DIR=/run/user/$(id -u)" "$CTR" \
  systemctl --user enable --now podman.socket
sleep 2
echo "== podman.socket active: $(podman exec "$CTR" systemctl --user -e XDG_RUNTIME_DIR=/run/user/$(id -u) is-active podman.socket)"

# row 4: nested rootless podman
podman exec -u "$(id -u):$(id -g)" \
  -e "XDG_RUNTIME_DIR=/run/user/$(id -u)" \
  -e "DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock" "$CTR" \
  podman run --rm hello-world

podman rm -f "$CTR" >/dev/null 2>&1 || true
echo "SPIKE COMPLETE"
```

- [ ] **Step 2: Run the spike on a rootless-Podman host and record results**

Run: `bash scripts/spike-systemd.sh`
Expected: rows 1–4 all succeed (no failures above each `==` line). Record any failure and its stderr in this task's commit message. If row 1, 2, or 3 fails, STOP and report to the user before writing runtime code; the runtime design must change. If row 4 alone fails, note that `privileged: true` (already in the schema plan) is the likely follow-up fix for the fragment, but systemd mode itself stays unprivileged.

- [ ] **Step 3: Commit**

```bash
git add scripts/spike-systemd.sh
git commit -m "test: add systemd container spike script"
```

---

### Task 2: Profile schema — `systemd` and `privileged` fields

**Files:**
- Modify: `internal/profile/types.go`
- Test: `internal/profile/types_test.go`

**Interfaces:**
- Produces: `type SystemdConfig struct { Enable []string }`; `Profile.Systemd *SystemdConfig`; `Profile.Privileged bool`.

- [ ] **Step 1: Write the failing YAML round-trip test**

```go
package profile

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSystemdConfigYAML(t *testing.T) {
	var p Profile
	data := []byte("systemd:\n  enable: [podman.socket]\nprivileged: true\n")
	if err := yaml.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Systemd == nil || len(p.Systemd.Enable) != 1 || p.Systemd.Enable[0] != "podman.socket" {
		t.Errorf("systemd = %+v, want enable [podman.socket]", p.Systemd)
	}
	if !p.Privileged {
		t.Error("privileged = false, want true")
	}
	out, err := yaml.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytesContains(out, []byte("podman.socket")) {
		t.Errorf("marshalled profile missing systemd.enable: %s", out)
	}
}

func TestSystemdConfigAbsent(t *testing.T) {
	var p Profile
	if err := yaml.Unmarshal([]byte("version: 1\n"), &p); err != nil {
		t.Fatal(err)
	}
	if p.Systemd != nil {
		t.Errorf("systemd = %+v, want nil when absent", p.Systemd)
	}
	if p.Privileged {
		t.Error("privileged = true, want false when absent")
	}
}

func bytesContains(b, sub []byte) bool {
	return len(b) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(b); i++ {
			if string(b[i:i+len(sub)]) == string(sub) {
				return true
			}
		}
		return false
	})()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestSystemdConfigYAML -v`
Expected: FAIL — `p.Systemd` stays nil (field doesn't exist yet).

- [ ] **Step 3: Add the types**

In `internal/profile/types.go`, after `DbusConfig`:

```go
// SystemdConfig switches a profile into systemd launch mode and lists the
// user units to enable+start. Units are materialized as files under
// ~/.config/systemd/user/ and enabled via `systemctl --user enable --now`.
type SystemdConfig struct {
	Enable []string `yaml:"enable,omitempty"`
}
```

Add to `Profile` struct (after `Dbus`):

```go
	Systemd     *SystemdConfig    `yaml:"systemd,omitempty"`
	Privileged  bool              `yaml:"privileged,omitempty"`
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/profile/ -run 'TestSystemdConfig' -v`
Expected: PASS.

- [ ] **Step 5: Run the suite and commit**

Run: `go test ./... && go vet ./...`
Commit:
```bash
git add internal/profile/types.go internal/profile/types_test.go
git commit -m "feat(profile): add systemd and privileged schema fields"
```

---

### Task 3: Merge semantics for `systemd` and `privileged`

**Files:**
- Modify: `internal/profile/catalog.go`, `internal/profile/merge.go`
- Test: `internal/profile/merge_test.go`

**Interfaces:**
- Consumes: `SystemdConfig`/`Privileged` from Task 2.
- Produces: `func mergeSystemd(parent, child *SystemdConfig, nullKeys map[string]bool) *SystemdConfig`; `collectNullKeys` tracks `"systemd"`.

- [ ] **Step 1: Write the failing merge tests**

```go
func TestMergeSystemdAppendDedup(t *testing.T) {
	parent := RawProfile{Profile: Profile{Systemd: &SystemdConfig{Enable: []string{"podman.socket"}}}}
	child := RawProfile{Profile: Profile{Systemd: &SystemdConfig{Enable: []string{"podman.socket", "redis.service"}}}}
	merged := MergeProfiles(parent, child)
	if merged.Systemd == nil {
		t.Fatal("merged.Systemd is nil")
	}
	want := []string{"podman.socket", "redis.service"}
	if !eqStrings(merged.Systemd.Enable, want) {
		t.Errorf("enable = %v, want %v", merged.Systemd.Enable, want)
	}
}

func TestMergeSystemdChildClears(t *testing.T) {
	parent := RawProfile{Profile: Profile{Systemd: &SystemdConfig{Enable: []string{"podman.socket"}}}}
	child := RawProfile{Profile: Profile{Systemd: &SystemdConfig{Enable: []string{"a.service"}}}, NullKeys: map[string]map[string]bool{"systemd": {"enable": true}}}
	merged := MergeProfiles(parent, child)
	if merged.Systemd == nil {
		t.Fatal("merged.Systemd is nil")
	}
	want := []string{"a.service"}
	if !eqStrings(merged.Systemd.Enable, want) {
		t.Errorf("enable = %v, want %v (null clears inherited list)", merged.Systemd.Enable, want)
	}
}

func TestMergeSystemdWholeFieldNull(t *testing.T) {
	parent := RawProfile{Profile: Profile{Systemd: &SystemdConfig{Enable: []string{"podman.socket"}}}}
	child := RawProfile{NullKeys: map[string]map[string]bool{"systemd": {"*": true}}}
	merged := MergeProfiles(parent, child)
	if merged.Systemd != nil {
		t.Errorf("merged.Systemd = %+v, want nil after systemd: null", merged.Systemd)
	}
}

func TestMergeSystemdAbsentChildKeepsParent(t *testing.T) {
	parent := RawProfile{Profile: Profile{Systemd: &SystemdConfig{Enable: []string{"podman.socket"}}}}
	merged := MergeProfiles(parent, RawProfile{})
	if merged.Systemd == nil || len(merged.Systemd.Enable) != 1 {
		t.Errorf("enable = %+v, want inherited podman.socket", merged.Systemd)
	}
}

func TestMergePrivilegedChildWins(t *testing.T) {
	merged := MergeProfiles(RawProfile{}, RawProfile{Profile: Profile{Privileged: true}})
	if !merged.Privileged {
		t.Error("privileged = false, want true (child sets it)")
	}
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/profile/ -run 'TestMergeSystemd|TestMergePrivileged' -v`
Expected: FAIL — `mergeSystemd` undefined.

- [ ] **Step 3: Implement**

In `internal/profile/catalog.go`, add `"systemd": {},` to the `nulls` map in `collectNullKeys` (the map literal with `"mounts"`, `"environment"`, … `"files"`).

In `internal/profile/merge.go`, wire into `MergeProfiles` after the `out.Dbus` line:

```go
	out.Systemd = mergeSystemd(parent.Systemd, child.Systemd, child.NullKeys["systemd"])
	if child.Privileged {
		out.Privileged = true
	}
```

Add the helper at the end of `merge.go`:

```go
// mergeSystemd unions enable across extends: append + dedup (child order
// after parent), with the usual null-to-delete convention for the whole field
// ("*") and the enable list. An empty result collapses to nil.
func mergeSystemd(parent, child *SystemdConfig, nullKeys map[string]bool) *SystemdConfig {
	if nullKeys["*"] {
		return nil
	}
	if child == nil {
		return parent
	}
	if parent == nil {
		parent = &SystemdConfig{}
	}
	out := &SystemdConfig{}
	if nullKeys["enable"] {
		out.Enable = nil
	} else {
		out.Enable = mergePackages(parent.Enable, child.Enable, nil)
	}
	if len(out.Enable) == 0 {
		return nil
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/profile/ -run 'TestMergeSystemd|TestMergePrivileged' -v`
Expected: PASS.

- [ ] **Step 5: Run the suite and commit**

Run: `go test ./... && go vet ./...`
Commit:
```bash
git add internal/profile/catalog.go internal/profile/merge.go internal/profile/merge_test.go
git commit -m "feat(profile): merge systemd and privileged fields"
```

---

### Task 4: Validate `systemd` unit names

**Files:**
- Modify: `internal/profile/validate.go`
- Test: `internal/profile/validate_test.go`

**Interfaces:**
- Consumes: `Profile.Systemd` from Task 2.
- Produces: `validateSystemd(rc RawProfile) error`, called from `validate`.

- [ ] **Step 1: Write the failing validation tests**

```go
func TestValidateSystemdUnitNames(t *testing.T) {
	for _, ok := range []string{"podman.socket", "redis.service", "foo.path", "backup.timer", "a.b_c@1.service"} {
		rc := RawProfile{Profile: Profile{Systemd: &SystemdConfig{Enable: []string{ok}}}, Path: "t"}
		if err := validateSystemd(rc); err != nil {
			t.Errorf("unit %q should validate: %v", ok, err)
		}
	}
	for _, bad := range []string{"podman", "podman.service;rm -rf /", "a b.service", "podman..service", "../evil.service", "PODMAN.socket"} {
		rc := RawProfile{Profile: Profile{Systemd: &SystemdConfig{Enable: []string{bad}}}, Path: "t"}
		if err := validateSystemd(rc); err == nil {
			t.Errorf("unit %q must be rejected", bad)
		}
	}
}

func TestValidateSystemdNilOK(t *testing.T) {
	if err := validateSystemd(RawProfile{}); err != nil {
		t.Errorf("nil systemd must validate: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/profile/ -run TestValidateSystemd -v`
Expected: FAIL — `validateSystemd` undefined.

- [ ] **Step 3: Implement**

In `internal/profile/validate.go`, add a regexp next to the other `var` regexps:

```go
var systemdUnitRe = regexp.MustCompile(`^[a-zA-Z0-9._@-]+\.(service|socket|path|timer)$`)
```

Add the function after `validateDbus`:

```go
// validateSystemd enforces the systemd unit-name grammar so a unit can never
// smuggle shell metacharacters into the `systemctl --user enable --now`
// exec line.
func validateSystemd(rc RawProfile) error {
	if rc.Systemd == nil {
		return nil
	}
	for _, unit := range rc.Systemd.Enable {
		if !systemdUnitRe.MatchString(unit) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("systemd.enable: invalid unit name %q (want [a-zA-Z0-9._@-]+.(service|socket|path|timer))", unit)}
		}
	}
	return nil
}
```

Wire it into `validate` after the `validateDbus` call:

```go
	if err := validateSystemd(rc); err != nil {
		return err
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/profile/ -run TestValidateSystemd -v`
Expected: PASS.

- [ ] **Step 5: Run the suite and commit**

Run: `go test ./... && go vet ./...`
Commit:
```bash
git add internal/profile/validate.go internal/profile/validate_test.go
git commit -m "feat(profile): validate systemd unit names"
```

---

### Task 5: Plumb `systemd`/`privileged` into the runtime Spec

**Files:**
- Modify: `internal/runtime/runtime.go`, `pkg/tpd/spec.go`
- Test: `pkg/tpd/spec_test.go`

**Interfaces:**
- Consumes: `profile.Profile.Systemd`/`Privileged`.
- Produces: `runtime.SystemdSpec struct { Enable []string }`; `Spec.Systemd SystemdSpec`; `Spec.Privileged bool`; `func (s Spec) SystemdMode() bool`.

- [ ] **Step 1: Write the failing tests**

```go
func TestBuildSpecSystemdAndPrivileged(t *testing.T) {
	cfg := profile.Profile{
		Version: 1, Image: "img", Command: []string{"sh"},
		Systemd:    &profile.SystemdConfig{Enable: []string{"podman.socket"}},
		Privileged: true,
	}
	spec, err := buildSpec(LaunchOpts{Workspace: "/p", ProfileName: "nested"}, cfg, workspace.ModeRootless, "/home/me", "/home/me")
	if err != nil {
		t.Fatal(err)
	}
	if !spec.SystemdMode() {
		t.Error("SystemdMode = false, want true")
	}
	if len(spec.Systemd.Enable) != 1 || spec.Systemd.Enable[0] != "podman.socket" {
		t.Errorf("Systemd.Enable = %v, want [podman.socket]", spec.Systemd.Enable)
	}
	if !spec.Privileged {
		t.Error("Privileged = false, want true")
	}
}

func TestBuildSpecNoSystemd(t *testing.T) {
	cfg := profile.Profile{Version: 1, Image: "img", Command: []string{"sh"}}
	spec, err := buildSpec(LaunchOpts{Workspace: "/p", ProfileName: "plain"}, cfg, workspace.ModeRootless, "/home/me", "/home/me")
	if err != nil {
		t.Fatal(err)
	}
	if spec.SystemdMode() {
		t.Error("SystemdMode = true, want false for a profile without systemd")
	}
	if len(spec.Systemd.Enable) != 0 {
		t.Errorf("Systemd.Enable = %v, want empty", spec.Systemd.Enable)
	}
	if spec.Privileged {
		t.Error("Privileged = true, want false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/tpd/ -run 'TestBuildSpecSystemd|TestBuildSpecNoSystemd' -v`
Expected: FAIL — `Systemd`/`Privileged` fields don't exist.

- [ ] **Step 3: Implement**

In `internal/runtime/runtime.go`, add a type and fields:

```go
// SystemdSpec switches a launch into systemd mode: the container boots
// /sbin/init (systemd as PID 1) and the units in Enable are enabled and
// started via the user manager before the profile command runs.
type SystemdSpec struct {
	Enable []string
}
```

Add to `Spec` struct (after `TTY`):

```go
	Systemd   SystemdSpec
	Privileged bool
```

Add a method at the end of `runtime.go`:

```go
// SystemdMode reports whether the profile declared systemd services, which
// changes how Run boots the container and runs the command.
func (s Spec) SystemdMode() bool {
	return len(s.Systemd.Enable) > 0
}
```

In `pkg/tpd/spec.go`, in `buildSpec` before the `return Spec{...}`:

```go
	systemd := runtime.SystemdSpec{}
	if cfg.Systemd != nil {
		systemd.Enable = append([]string{}, cfg.Systemd.Enable...)
	}
```

and add to the returned struct literal:

```go
		Systemd:     systemd,
		Privileged:  cfg.Privileged,
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/tpd/ -run 'TestBuildSpecSystemd|TestBuildSpecNoSystemd' -v`
Expected: PASS.

- [ ] **Step 5: Run the suite and commit**

Run: `go test ./... && go vet ./...`
Commit:
```bash
git add internal/runtime/runtime.go pkg/tpd/spec.go pkg/tpd/spec_test.go
git commit -m "feat(runtime): plumb systemd mode and privileged into Spec"
```

---

### Task 6: Render `systemd:`/`privileged:` in dry-run/verbose output

**Files:**
- Modify: `pkg/tpd/dryrun.go`
- Test: `pkg/tpd/dryrun_test.go`

**Interfaces:**
- Consumes: `Spec.Systemd`, `Spec.Privileged` from Task 5.

- [ ] **Step 1: Write the failing test**

```go
func TestRenderSpecSystemdAndPrivileged(t *testing.T) {
	spec := Spec{
		ProfileName: "nested",
		Image:       "img",
		Command:     []string{"bash", "-l"},
		Systemd:     runtime.SystemdSpec{Enable: []string{"podman.socket"}},
		Privileged:  true,
	}
	var buf bytes.Buffer
	if err := RenderSpec(&buf, spec); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"systemd:", "enable: [podman.socket]", "privileged: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderSpec output missing %q:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tpd/ -run TestRenderSpecSystemdAndPrivileged -v`
Expected: FAIL — output lacks `systemd:`.

- [ ] **Step 3: Implement**

In `pkg/tpd/dryrun.go`, after the `command:` line block:

```go
	if len(spec.Systemd.Enable) > 0 {
		_, err = fmt.Fprintf(w, "systemd:\n  enable: %v\n", spec.Systemd.Enable)
		if err != nil {
			return err
		}
	}
	if spec.Privileged {
		_, err = fmt.Fprintln(w, "privileged: true")
		if err != nil {
			return err
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tpd/ -run TestRenderSpecSystemdAndPrivileged -v`
Expected: PASS.

- [ ] **Step 5: Run the suite and commit**

Run: `go test ./... && go vet ./...`
Commit:
```bash
git add pkg/tpd/dryrun.go pkg/tpd/dryrun_test.go
git commit -m "feat(cli): render systemd and privileged in dry-run output"
```

---

### Task 7: `execIn` — run a command inside the container via the exec API

**Files:**
- Modify: `internal/runtime/attach.go`, `internal/runtime/docker_run.go`
- Test: `internal/runtime/docker_exec_test.go`

**Interfaces:**
- Consumes: `Spec`, `types.HijackedResponse`, `container.ExecOptions`.
- Produces: `func (d *DockerRuntime) execIn(ctx context.Context, containerID, user string, env []string, cmd []string, tty bool, stdin io.Reader, stdout, stderr io.Writer) (int, error)`; `handleExecResize`.

`execIn` is the single workhorse for systemd mode: admin commands (bootstrap, `systemctl …`) and the interactive user command. It creates the exec, hijacks `/exec/{id}/start` (which starts the process and yields the stream), pumps output (stdcopy multiplex when not tty, raw when tty), feeds stdin when tty, and polls `ContainerExecInspect` until the process exits, returning its exit code.

- [ ] **Step 1: Write the failing test (fake exec server)**

```go
package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
)

// execFake serves the docker API for one container + one exec: create, exec
// create, hijacked exec start, exec inspect, container start/remove. stdoutFrames
// are written as stdcopy frames when tty=false.
func execFake(t *testing.T, stdoutFrames []byte, exitCode int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Id":"ctr1","Warnings":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1.41/containers/ctr1/start":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/ctr1/exec"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Id":"exec1"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec/exec1/start"):
			hj := w.(http.Hijacker)
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			defer conn.Close()
			fmt.Fprintf(conn, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_, _ = conn.Write(stdoutFrames)
			conn.(interface{ CloseWrite() error }).CloseWrite()
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/exec/exec1/json"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"ID":"exec1","Running":false,"ExitCode":%d}`, exitCode)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/ctr1"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	return srv
}

func stdcopyFrame(stream byte, payload string) []byte {
	buf := make([]byte, 8+len(payload))
	buf[0] = stream
	binary.BigEndian.PutUint32(buf[4:8], uint32(len(payload)))
	copy(buf[8:], payload)
	return buf
}

func TestExecInNonTTYExitCodeAndOutput(t *testing.T) {
	srv := execFake(t, stdcopyFrame(1, "hello"), 7)
	defer srv.Close()

	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.41"))
	if err != nil {
		t.Fatal(err)
	}
	d := &DockerRuntime{cli: cli}

	var out bytes.Buffer
	code, err := d.execIn(context.Background(), "ctr1", "1000:1000", []string{"A=B"}, []string{"echo", "hi"}, false, nil, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if out.String() != "hello" {
		t.Errorf("stdout = %q, want %q", out.String(), "hello")
	}
}

func TestExecInNoStreamStillReturnsExitCode(t *testing.T) {
	srv := execFake(t, nil, 0)
	defer srv.Close()

	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.41"))
	if err != nil {
		t.Fatal(err)
	}
	code, err := (&DockerRuntime{cli: cli}).execIn(context.Background(), "ctr1", "0:0", nil, []string{"true"}, false, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/ -run TestExecIn -v`
Expected: FAIL — `execIn` undefined.

- [ ] **Step 3: Implement `handleExecResize`**

In `internal/runtime/attach.go`, add after `handleResize`:

```go
func (d *DockerRuntime) handleExecResize(ctx context.Context, execID string, winCh chan os.Signal) {
	for range winCh {
		rows, cols := terminalSize()
		if rows > 0 && cols > 0 {
			_ = d.cli.ContainerExecResize(ctx, execID, container.ResizeOptions{Height: rows, Width: cols})
		}
	}
}
```

- [ ] **Step 4: Implement `execIn`**

Add to `internal/runtime/docker_run.go`, plus imports `github.com/docker/docker/api/types` (if not already imported). The pump mirrors the attach pump in `Run` (mutex-guarded stdin, single close):

```go
// execIn runs cmd inside containerID via the exec API and returns its exit
// code. For tty execs it attaches stdin/stdout and wires SIGWINCH resize;
// otherwise it decodes the multiplexed stream into stdout/stderr. Attach
// hijacks the start endpoint, so no separate start call is made.
func (d *DockerRuntime) execIn(ctx context.Context, containerID, user string, env []string, cmd []string, tty bool, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	createResp, err := d.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		User:         user,
		Env:          env,
		Tty:          tty,
		AttachStdin:  tty && stdin != nil,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return 3, fmt.Errorf("exec create: %w", err)
	}
	if tty && stdin != nil && term.IsTerminal(int(os.Stdin.Fd())) {
		rows, cols := terminalSize()
		if rows > 0 && cols > 0 {
			_ = d.cli.ContainerExecResize(ctx, createResp.ID, container.ResizeOptions{Height: rows, Width: cols})
		}
		winCh := make(chan os.Signal, 1)
		signal.Notify(winCh, syscall.SIGWINCH)
		defer func() {
			signal.Stop(winCh)
			close(winCh)
		}()
		go d.handleExecResize(ctx, createResp.ID, winCh)
	}

	hijacked, err := d.cli.ContainerExecAttach(ctx, createResp.ID, container.ExecAttachOptions{Tty: tty})
	if err != nil {
		return 3, fmt.Errorf("exec attach: %w", err)
	}
	defer hijacked.Close()

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		if tty {
			_, _ = io.Copy(stdout, hijacked.Reader)
		} else {
			_, _ = stdcopy.StdCopy(stdout, stderr, hijacked.Reader)
		}
	}()

	var connMu sync.Mutex
	connClosed := false
	if tty && stdin != nil {
		go func() {
			buf := make([]byte, 32*1024)
			for {
				n, readErr := stdin.Read(buf)
				if n > 0 {
					connMu.Lock()
					if connClosed {
						connMu.Unlock()
						return
					}
					_, writeErr := hijacked.Conn.Write(buf[:n])
					connMu.Unlock()
					if writeErr != nil {
						return
					}
				}
				if readErr != nil {
					return
				}
			}
		}()
	}
	closeConn := func() {
		connMu.Lock()
		connClosed = true
		hijacked.Close()
		connMu.Unlock()
	}
	<-pumpDone
	closeConn()

	for {
		insp, err := d.cli.ContainerExecInspect(ctx, createResp.ID)
		if err != nil {
			return 3, fmt.Errorf("exec inspect: %w", err)
		}
		if !insp.Running {
			return insp.ExitCode, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/runtime/ -run TestExecIn -v`
Expected: PASS.

- [ ] **Step 6: Run the suite and commit**

Run: `go test ./... && go vet ./...`
Commit:
```bash
git add internal/runtime/attach.go internal/runtime/docker_run.go internal/runtime/docker_exec_test.go
git commit -m "feat(runtime): add execIn helper for systemd mode"
```

---

### Task 8: `runSystemd` — create config and boot sequence

**Files:**
- Modify: `internal/runtime/docker_run.go`
- Test: `internal/runtime/docker_exec_test.go`

**Interfaces:**
- Consumes: `execIn` (Task 7), `Spec.SystemdMode()`, `Spec.Systemd`, `Spec.Privileged`.
- Produces: `func (d *DockerRuntime) waitSystemdReady(ctx context.Context, containerID string, timeout time.Duration) error`; `func (d *DockerRuntime) bootSystemd(ctx context.Context, containerID string, spec Spec, hostUID, hostGID int) error`; `func (d *DockerRuntime) createSystemdContainer(ctx context.Context, spec Spec, userns container.UsernsMode, env []string, mounts []mount.Mount, containerName string) (container.CreateResponse, error)`; a shared test fake `newSystemdFake(t, userExitCode, userExecN int) (*httptest.Server, *systemdFake)` (reused by Task 9).

- [ ] **Step 1: Write the failing tests (shared fake + create config + boot)**

> Append to `internal/runtime/docker_exec_test.go` (created in Task 7, same file). Task 7's block already imports `bytes`, `context`, `encoding/binary`, `fmt`, `net/http`, `net/http/httptest`, `strings`, `testing`, `client` and defines `stdcopyFrame`. Add these imports to that block: `encoding/json`, `io`, `os`, `regexp`, `strconv`, `sync`, `time`, `github.com/docker/docker/api/types/container`, `github.com/docker/docker/api/types/mount`, `github.com/jgillich/tpd/internal/mise`, `github.com/jgillich/tpd/internal/workspace`.

```go
// newSystemdFake serves the Docker API for a systemd-mode run: create, start,
// exec create/start/inspect, remove. Boot execs (1..5) exit 0; the exec at
// userExecN (the profile command) exits with userExitCode. is-system-running
// (exec 1) reports "running" only when systemRunning is true. The exec-start
// request body is plain JSON on the hijacked connection (the SDK sends it
// with Content-Length), so the handler reads r.Body before hijacking.
func newSystemdFake(t *testing.T, userExitCode, userExecN int) (*httptest.Server, *systemdFake) {
	t.Helper()
	f := &systemdFake{userExecN: userExecN, systemRunning: true}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			var req struct {
				Config     container.Config     `json:"Config"`
				HostConfig container.HostConfig `json:"HostConfig"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.created, f.hostCfg = req.Config, req.HostConfig
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Id":"ctr1","Warnings":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1.41/containers/ctr1/start":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/ctr1/exec"):
			f.execCount++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"Id":"exec%d"}`, f.execCount)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1.41/exec/exec") && strings.HasSuffix(r.URL.Path, "/start"):
			var opts container.ExecOptions
			if body, err := io.ReadAll(r.Body); err == nil {
				_ = json.Unmarshal(body, &opts)
				f.lastUser = opts.User
				f.lastCmd = strings.Join(opts.Cmd, " ")
			}
			n := systemdExecNumber(r.URL.Path)
			hj := w.(http.Hijacker)
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			defer conn.Close()
			fmt.Fprintf(conn, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			if n == 1 && f.systemRunning {
				_, _ = conn.Write(stdcopyFrame(1, "running\n"))
			}
			conn.(interface{ CloseWrite() error }).CloseWrite()
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.41/exec/exec") && strings.HasSuffix(r.URL.Path, "/json"):
			n := systemdExecNumber(r.URL.Path)
			code := 0
			if n == f.userExecN {
				code = userExitCode
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"ID":"exec%d","Running":false,"ExitCode":%d}`, n, code)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/ctr1"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

type systemdFake struct {
	created       container.Config
	hostCfg       container.HostConfig
	lastUser      string
	lastCmd       string
	execCount     int
	userExecN     int
	systemRunning bool
}

func systemdExecNumber(path string) int {
	m := regexp.MustCompile(`/exec/exec(\d+)/`).FindStringSubmatch(path)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func testSpecSystemd() Spec {
	return Spec{
		ProfileName: "nested",
		Image:       "img",
		Command:     []string{"bash", "-l"},
		RuntimeHome: "/home/me",
		Workspace:   WorkspaceSpec{HostPath: "/w", Target: "/home/me/proj", Mode: workspace.ModeRootless},
		Systemd:     SystemdSpec{Enable: []string{"podman.socket"}},
		Env:         map[string]string{},
		Tools:       map[string]mise.Tool{},
	}
}

func TestCreateSystemdContainer(t *testing.T) {
	srv, f := newSystemdFake(t, 0, 0)
	defer srv.Close()
	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.41"))
	if err != nil {
		t.Fatal(err)
	}
	d := &DockerRuntime{cli: cli}
	_, err = d.createSystemdContainer(context.Background(), testSpecSystemd(), "keep-id", []string{"HOME=/home/me"}, []mount.Mount{}, "tpd-nested-abc123")
	if err != nil {
		t.Fatal(err)
	}
	if f.created.Cmd == nil || len(f.created.Cmd) != 1 || f.created.Cmd[0] != "/sbin/init" {
		t.Errorf("Cmd = %v, want [/sbin/init]", f.created.Cmd)
	}
	if f.created.User != "0:0" {
		t.Errorf("User = %q, want 0:0 (systemd boots as root)", f.created.User)
	}
	if f.hostCfg.Init == nil || *f.hostCfg.Init {
		t.Errorf("Init = %v, want false (no tini in systemd mode)", f.hostCfg.Init)
	}
	for _, m := range []string{"/run", "/run/lock", "/tmp"} {
		if f.hostCfg.Tmpfs[m] == "" {
			t.Errorf("missing tmpfs entry for %s: %v", m, f.hostCfg.Tmpfs)
		}
	}
	if f.hostCfg.Privileged {
		t.Error("systemd mode must not be privileged")
	}
	if f.hostCfg.UsernsMode != "keep-id" {
		t.Errorf("UsernsMode = %q, want keep-id", f.hostCfg.UsernsMode)
	}
}

func TestWaitSystemdReady(t *testing.T) {
	srv, _ := newSystemdFake(t, 0, 0)
	defer srv.Close()
	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.41"))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&DockerRuntime{cli: cli}).waitSystemdReady(context.Background(), "ctr1", 5*time.Second); err != nil {
		t.Fatalf("waitSystemdReady: %v", err)
	}
}

func TestBootSystemd(t *testing.T) {
	srv, f := newSystemdFake(t, 0, 0)
	defer srv.Close()
	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.41"))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&DockerRuntime{cli: cli}).bootSystemd(context.Background(), "ctr1", testSpecSystemd(), os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("bootSystemd: %v", err)
	}
	if f.execCount != 4 {
		t.Errorf("boot exec count = %d, want 4 (bootstrap, user@, socket poll, enable)", f.execCount)
	}
	if !strings.Contains(f.lastCmd, "systemctl --user enable --now podman.socket") {
		t.Errorf("last boot exec = %q, want the unit enable", f.lastCmd)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/ -run 'TestCreateSystemdContainer|TestWaitSystemdReady|TestBootSystemd' -v`
Expected: FAIL — `createSystemdContainer`/`waitSystemdReady`/`bootSystemd` undefined.

- [ ] **Step 3: Implement the boot helpers**

Add to `internal/runtime/docker_run.go`:

```go
// waitSystemdReady polls until systemd reports the system is running or
// degraded, or the timeout passes.
func (d *DockerRuntime) waitSystemdReady(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var out bytes.Buffer
		_, err := d.execIn(ctx, containerID, "0:0", nil, []string{"systemctl", "is-system-running"}, false, nil, &out, &out)
		if err != nil {
			return fmt.Errorf("wait for systemd: %w", err)
		}
		switch strings.TrimSpace(out.String()) {
		case "running", "degraded":
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("systemd did not finish booting within %s", timeout)
}

// bootSystemd prepares the systemd container: it creates the runtime home and
// the user runtime dir, appends subuid/subgid entries so the nested rootless
// podman can map ranges, starts the user manager for the execution user, and
// enables+starts each declared unit via the user manager.
func (d *DockerRuntime) bootSystemd(ctx context.Context, containerID string, spec Spec, hostUID, hostGID int) error {
	runtimeDir := fmt.Sprintf("/run/user/%d", hostUID)
	bootstrap := fmt.Sprintf(
		"mkdir -p %s %s && chown %d:%d %s %s && chmod 700 %s && "+
			"(grep -q '^%d:' /etc/subuid || echo '%d:100000:65536' >> /etc/subuid) && "+
			"(grep -q '^%d:' /etc/subgid || echo '%d:100000:65536' >> /etc/subgid)",
		shq(spec.RuntimeHome), shq(runtimeDir),
		hostUID, hostGID, shq(spec.RuntimeHome), shq(runtimeDir), shq(runtimeDir),
		hostUID, hostUID, hostGID, hostGID)
	if code, err := d.execIn(ctx, containerID, "0:0", nil, []string{"sh", "-c", bootstrap}, false, nil, os.Stderr, os.Stderr); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("systemd bootstrap exited %d", code)
	}

	if code, err := d.execIn(ctx, containerID, "0:0", nil, []string{"systemctl", "start", fmt.Sprintf("user@%d.service", hostUID)}, false, nil, os.Stderr, os.Stderr); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("starting user@%d.service exited %d", hostUID, code)
	}

	// Wait for the user manager's private socket (no logind in a container).
	userSock := filepath.Join(runtimeDir, "systemd", "private")
	deadline := time.Now().Add(60 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if code, err := d.execIn(ctx, containerID, "0:0", nil, []string{"test", "-S", userSock}, false, nil, io.Discard, io.Discard); err != nil {
			return err
		} else if code == 0 {
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		return fmt.Errorf("user manager for uid %d did not start within 60s", hostUID)
	}

	userEnv := []string{"HOME=" + spec.RuntimeHome, "XDG_RUNTIME_DIR=" + runtimeDir}
	user := fmt.Sprintf("%d:%d", hostUID, hostGID)
	for _, unit := range spec.Systemd.Enable {
		if code, err := d.execIn(ctx, containerID, user, userEnv, []string{"systemctl", "--user", "enable", "--now", unit}, false, nil, os.Stderr, os.Stderr); err != nil {
			return err
		} else if code != 0 {
			return fmt.Errorf("enabling %s exited %d", unit, code)
		}
	}
	return nil
}
```

- [ ] **Step 4: Implement `createSystemdContainer`**

Add to `internal/runtime/docker_run.go`:

```go
// createSystemdContainer builds the create call for a systemd container:
// /sbin/init as PID 1 (no tini), tmpfs for the dirs systemd needs to write,
// keep-id identity. Privileged comes from the profile, but systemd mode never
// sets it itself.
func (d *DockerRuntime) createSystemdContainer(ctx context.Context, spec Spec, userns container.UsernsMode, env []string, mounts []mount.Mount, containerName string) (container.CreateResponse, error) {
	initEnabled := false
	_, rootUser, _, _ := containerIdentity(d.podman)
	tmpfs := map[string]string{
		"/run":      "rw,nosuid,nodev,size=65536k,mode=755",
		"/run/lock": "rw,nosuid,nodev,size=8192k,mode=755",
		"/tmp":      "rw,nosuid,nodev,size=65536k,mode=755",
	}
	return d.cli.ContainerCreate(ctx, &container.Config{
		Image:      spec.Image,
		Cmd:        []string{"/sbin/init"},
		Env:        env,
		User:       rootUser,
		Tty:        false,
		WorkingDir: spec.RuntimeHome,
		Labels:     spec.Labels,
		Hostname:   spec.ProfileName,
		Entrypoint: []string{},
	}, &container.HostConfig{
		Mounts:      mounts,
		Tmpfs:       tmpfs,
		NetworkMode: container.NetworkMode(spec.Network),
		UsernsMode:  userns,
		SecurityOpt: d.securityOpts(),
		AutoRemove:  false,
		Init:        &initEnabled,
		Privileged:  spec.Privileged,
		Resources: container.Resources{
			Memory:   spec.Resources.MemoryBytes,
			NanoCPUs: spec.Resources.NanoCPUs,
		},
	}, &network.NetworkingConfig{}, nil, containerName)
}
```

(`mount` and `network` are already imported by `docker_run.go`.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/runtime/ -run 'TestCreateSystemdContainer|TestWaitSystemdReady|TestBootSystemd' -v`
Expected: PASS.

- [ ] **Step 6: Run the suite and commit**

Run: `go test ./... && go vet ./...`
Commit:
```bash
git add internal/runtime/docker_run.go internal/runtime/docker_exec_test.go
git commit -m "feat(runtime): systemd create config and boot sequence"
```

---

### Task 9: `runSystemd` orchestrator, user-command exec, and `Run()` wiring

**Files:**
- Modify: `internal/runtime/docker_run.go`
- Test: `internal/runtime/docker_exec_test.go`

**Interfaces:**
- Consumes: `createSystemdContainer`/`waitSystemdReady`/`bootSystemd` (Task 8), `execIn` (Task 7), the `newSystemdFake` helper (Task 8).
- Produces: `func (d *DockerRuntime) runSystemd(ctx context.Context, spec Spec) (int, error)`; `func (d *DockerRuntime) runSystemdCommand(ctx context.Context, containerID string, spec Spec, env []string, hostUID, hostGID int) (int, error)`; `Run` routes `Spec.SystemdMode()` to `runSystemd`.

- [ ] **Step 1: Write the failing test (full systemd run)**

```go
func TestRunSystemdFullFlow(t *testing.T) {
	srv, f := newSystemdFake(t, 5, 6)
	defer srv.Close()
	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.41"))
	if err != nil {
		t.Fatal(err)
	}
	d := &DockerRuntime{cli: cli}
	spec := testSpecSystemd()
	spec.Command = []string{"podman", "run", "-it", "alpine"}
	code, err := d.runSystemd(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if code != 5 {
		t.Errorf("exit code = %d, want 5", code)
	}
	if !strings.Contains(f.lastCmd, "exec 'podman' 'run' '-it' 'alpine'") {
		t.Errorf("user command exec = %q, want it to end in the shell-quoted user command", f.lastCmd)
	}
	if f.lastUser != fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()) {
		t.Errorf("user exec user = %q, want host user", f.lastUser)
	}
	if f.execCount != 6 {
		t.Errorf("exec count = %d, want 6 (5 boot execs + user command)", f.execCount)
	}
	if f.created.Cmd == nil || f.created.Cmd[0] != "/sbin/init" {
		t.Errorf("container Cmd = %v, want [/sbin/init]", f.created.Cmd)
	}
}
```

> The fake exits execs 1–5 (is-system-running, bootstrap, user@ start, socket poll, enable) with 0 and exec 6 (the user command) with 5. The `"running\n"` frame on exec 1 lets `waitSystemdReady` pass immediately; the socket-poll exec's exit 0 means `bootSystemd` doesn't wait.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/ -run TestRunSystemdFullFlow -v`
Expected: FAIL — `runSystemd` undefined.

- [ ] **Step 3: Implement `runSystemd`**

Add to `internal/runtime/docker_run.go`:

```go
// runSystemd boots a container with systemd as PID 1, enables the profile's
// units via the user manager, then runs the profile command via the exec API
// as the host user. Used only when spec.SystemdMode() is true.
func (d *DockerRuntime) runSystemd(ctx context.Context, spec Spec) (int, error) {
	runtimeHome := spec.RuntimeHome
	hostUID, hostGID := os.Getuid(), os.Getgid()
	userns, _, _, _ := containerIdentity(d.podman)
	runtimeDir := fmt.Sprintf("/run/user/%d", hostUID)

	mounts, err := buildMounts(spec, runtimeHome, d.subpathSupported(ctx))
	if err != nil {
		return 3, fmt.Errorf("build mounts: %w", err)
	}
	envList := buildEnv(spec, runtimeHome)
	envList = append(envList, "XDG_RUNTIME_DIR="+runtimeDir)

	containerName := "tpd-" + spec.ProfileName + "-" + randomID(8)
	resp, err := d.createSystemdContainer(ctx, spec, userns, envList, mounts, containerName)
	if err != nil {
		return 3, fmt.Errorf("create container: %w", err)
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := d.cli.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
			fmt.Fprintf(os.Stderr, "tpd: warning: remove container %s: %v\n", resp.ID, err)
		}
	}()

	if len(spec.Files) > 0 {
		if err := writeContainerFiles(ctx, d.cli, resp.ID, spec.Files, hostUID, hostGID); err != nil {
			return 3, fmt.Errorf("write profile files: %w", err)
		}
	}

	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return 3, fmt.Errorf("start container: %w", err)
	}

	if err := d.waitSystemdReady(ctx, resp.ID, 120*time.Second); err != nil {
		return 3, err
	}
	if err := d.bootSystemd(ctx, resp.ID, spec, hostUID, hostGID); err != nil {
		return 3, err
	}

	return d.runSystemdCommand(ctx, resp.ID, spec, envList, hostUID, hostGID)
}
```

- [ ] **Step 4: Implement `runSystemdCommand`**

Add to `internal/runtime/docker_run.go`:

```go
// runSystemdCommand builds the profile command (the same mise chain the tini
// path uses) and runs it via the exec API as the host user, mapping its exit
// code. Signals still kill the container; systemd propagates them.
func (d *DockerRuntime) runSystemdCommand(ctx context.Context, containerID string, spec Spec, env []string, hostUID, hostGID int) (int, error) {
	configDir := filepath.Join(spec.RuntimeHome, ".config", "mise")
	parts := []string{"cd " + shq(spec.Workspace.Target)}
	if activateCmd := mise.ActivateCommand(configDir, spec.Tools); activateCmd != "" {
		parts = append(parts, activateCmd)
	}
	if cmd := mise.BackendRuntimesCommand(configDir, spec.Tools); cmd != "" {
		parts = append(parts, cmd)
	}
	if mise.NeedsEmbeddedPlugin(spec.Tools) {
		parts = append(parts, mise.PluginInstallCommand())
	}
	parts = append(parts, "mise install")
	parts = append(parts, `eval "$(mise hook-env 2>/dev/null)" || true`)
	parts = append(parts, "exec "+shellQuote(spec.Command))
	userCmd := strings.Join(parts, " && ")

	tty := spec.TTY == "true" || ((spec.TTY == "auto" || spec.TTY == "") && term.IsTerminal(int(os.Stdout.Fd())))

	var oldState *term.State
	var err error
	if tty && term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return 3, fmt.Errorf("set raw mode: %w", err)
		}
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer func() {
		signal.Stop(sigCh)
		close(sigCh)
	}()
	go func() {
		for sig := range sigCh {
			if s, ok := sig.(syscall.Signal); ok {
				_ = d.cli.ContainerKill(ctx, containerID, strconv.Itoa(int(s)))
			}
		}
	}()

	user := fmt.Sprintf("%d:%d", hostUID, hostGID)
	code, err := d.execIn(ctx, containerID, user, env, []string{"sh", "-c", userCmd}, tty, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		return 3, fmt.Errorf("run command: %w", err)
	}
	return code, nil
}
```

- [ ] **Step 5: Wire `Run`**

In `internal/runtime/docker_run.go`, at the top of `Run`:

```go
	if spec.SystemdMode() {
		return d.runSystemd(ctx, spec)
	}
```

- [ ] **Step 6: Add a regression guard for the non-systemd path**

```go
func TestRunNonSystemdKeepsTiniPath(t *testing.T) {
	var req struct {
		Config     container.Config     `json:"Config"`
		HostConfig container.HostConfig `json:"HostConfig"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Id":"ctr1","Warnings":[]}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/ctr1/attach"):
			hj := w.(http.Hijacker)
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			defer conn.Close()
			fmt.Fprintf(conn, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			conn.(interface{ CloseWrite() error }).CloseWrite()
		case r.Method == http.MethodPost && r.URL.Path == "/v1.41/containers/ctr1/start":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/ctr1/wait"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"StatusCode":0}`)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/ctr1"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.41"))
	if err != nil {
		t.Fatal(err)
	}
	d := &DockerRuntime{cli: cli}
	spec := Spec{
		ProfileName: "plain", Image: "img", Command: []string{"sh"}, RuntimeHome: "/home/me",
		Workspace: WorkspaceSpec{HostPath: "/w", Target: "/home/me/proj", Mode: workspace.ModeRootless},
		Env:       map[string]string{}, Tools: map[string]mise.Tool{},
	}
	code, err := d.Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if req.Config.Cmd == nil || len(req.Config.Cmd) != 3 || req.Config.Cmd[0] != "sh" {
		t.Errorf("Cmd = %v, want the tini bootstrap wrapper (sh -c ...), not /sbin/init", req.Config.Cmd)
	}
	if req.HostConfig.Init == nil || !*req.HostConfig.Init {
		t.Error("Init must be true in the non-systemd path")
	}
}
```

> `Run` completes here because the fake attach stream closes immediately and `/wait` reports `StatusCode:0`. The `supportsVolumeSubpath` probe hits `GET /version`, which the fake 404s; that probe already tolerates errors (returns false). Asserting `Cmd[0] == "sh"` and `Init == true` proves a non-systemd profile keeps the current tini path and never gets `Cmd=["/sbin/init"]`.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/runtime/ -run 'TestRunSystemdFullFlow|TestRunNonSystemdKeepsTiniPath' -v`
Expected: PASS.

- [ ] **Step 8: Run the suite and commit**

Run: `go test ./... && go vet ./...`
Commit:
```bash
git add internal/runtime/docker_run.go internal/runtime/docker_exec_test.go
git commit -m "feat(runtime): run profile command via exec in systemd mode"
```

---

### Task 10: `podman-nested` fragment and its advisory

**Files:**
- Create: `internal/catalog/fragments/podman-nested.yaml`
- Modify: `internal/catalog/advisories.go`
- Test: `internal/catalog/catalog_test.go`

**Interfaces:**
- Consumes: schema fields from Tasks 2–4; fragment validation from `internal/profile/catalog.go`.
- Produces: a fragment named `podman-nested` that any profile can extend.

- [ ] **Step 1: Write the failing tests**

```go
func TestPodmanNestedFragment(t *testing.T) {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	frag, ok := cat.Get("podman-nested")
	if !ok {
		t.Fatal("podman-nested fragment not found in catalog")
	}
	if frag.Systemd == nil || len(frag.Systemd.Enable) != 1 || frag.Systemd.Enable[0] != "podman.socket" {
		t.Errorf("systemd = %+v, want enable [podman.socket]", frag.Systemd)
	}
	if len(frag.Packages) == 0 {
		t.Error("fragment should declare systemd/podman packages")
	}
	if frag.Privileged {
		t.Error("podman-nested must not be privileged")
	}
	if frag.Image != "" || len(frag.Command) > 0 {
		t.Errorf("fragment must not set image/command, got image=%q command=%v", frag.Image, frag.Command)
	}
}
```

Add to the existing advisory test in `internal/catalog/catalog_test.go` (or a new test):

```go
func TestAdvisoryPodmanNested(t *testing.T) {
	if got := Advisory("podman-nested"); got == "" {
		t.Error("podman-nested should carry an advisory")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/catalog/ -run 'TestPodmanNested|TestAdvisoryPodmanNested' -v`
Expected: FAIL — fragment not found / empty advisory.

- [ ] **Step 3: Create the fragment**

`internal/catalog/fragments/podman-nested.yaml`:

```yaml
version: 1
systemd:
  enable:
    - podman.socket
packages:
  - systemd
  - podman
  - fuse-overlayfs
  - uidmap
  - slirp4netns
environment:
  DOCKER_HOST: unix:///run/user/{{ uid }}/podman/podman.sock
  PODMAN_HOST: unix:///run/user/{{ uid }}/podman/podman.sock
```

- [ ] **Step 4: Add the advisory**

In `internal/catalog/advisories.go`, add a case:

```go
	case "podman-nested":
		return "runs a nested rootless Podman engine inside the container (systemd, socket-activated) — an isolated engine with no host daemon access"
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/catalog/ ./internal/profile/ -run 'TestPodmanNested|TestAdvisoryPodmanNested|TestLoadBuiltins|TestLoadFragments' -v`
Expected: PASS. (This also exercises that the fragment passes `validateFragmentName`.)

- [ ] **Step 6: Run the suite and commit**

Run: `go test ./... && go vet ./...`
Commit:
```bash
git add internal/catalog/fragments/podman-nested.yaml internal/catalog/advisories.go internal/catalog/catalog_test.go
git commit -m "feat(catalog): add podman-nested fragment and advisory"
```

---

### Task 11: Documentation

**Files:**
- Modify: `README.md`, `docs/2026-08-03-security-model.md`

**Interfaces:**
- Consumes: the implemented `systemd:`/`privileged` fields and the `podman-nested` fragment.

- [ ] **Step 1: Document `systemd:` and `privileged`**

In `README.md`, add a short section after the schema/`files:` docs:

```markdown
### `systemd:` (systemd launch mode)

A profile that resolves with `systemd.enable` non-empty boots `systemd` as PID 1
instead of the tini wrapper. The listed user units (materialized via `files:` in
`~/.config/systemd/user/`) are enabled and started through the user manager with
`systemctl --user enable --now`, then the profile command runs via the exec API
as the host user — same mise chain, TTY, and exit-code mapping as the normal
path. This is how a long-lived background engine (e.g. nested Podman) stays
alive independently of the command: a `.socket` unit gets real socket activation.

`privileged: true` is a schema escape hatch (it sets `HostConfig.Privileged`).
Systemd mode never sets it; profiles that opt in take full responsibility.

```yaml
version: 1
extends: [mise, podman-nested]
command: ["bash", "-l"]
```

See `internal/catalog/fragments/podman-nested.yaml` for a full example.
```

- [ ] **Step 2: Note the security model**

In `docs/2026-08-03-security-model.md`, add a bullet under the container-isolation section: systemd mode runs systemd as PID 1 with tmpfs on `/run`,`/run/lock`,`/tmp`; it is not privileged and keeps keep-id host-user identity; the nested podman engine is contained in the container and never touches the host socket. The `privileged` field is a deliberate, advisory-flagged opt-out.

- [ ] **Step 3: Run the suite and commit**

Run: `go test ./... && go vet ./...`
Commit:
```bash
git add README.md docs/2026-08-03-security-model.md
git commit -m "docs: document systemd launch mode and privileged"
```

---

## Self-Review

- **Spec coverage:** `systemd:` field (Tasks 2–4); gate on non-empty `enable` + tini path untouched (Tasks 5, 9); `/sbin/init` + no tini + tmpfs + no privileged create (Task 8); boot sequence incl. user manager, subuid/subgid, `systemctl --user enable --now` (Task 8); orchestrator + exec-based command with mise chain/TTY/exit-code mapping and signal kill (Task 9); `podman-nested` fragment + advisory (Task 10); dry-run render (Task 6); risk-register spike (Task 1); docs (Task 11). The concurrent-shared-cache work is explicitly deferred (spec Non-Goal 8) and has no task here by design.
- **Placeholders:** none — every task has concrete code and test steps, including the shared `newSystemdFake` test helper (Task 8, reused by Task 9) and the full-flow regression guards.
- **Type consistency:** `SystemdConfig.Enable`, `runtime.SystemdSpec.Enable`, `Spec.Systemd`, `Spec.Privileged`, `Spec.SystemdMode()`, `execIn`, `waitSystemdReady(ctx, id, timeout)`, `bootSystemd`, `createSystemdContainer`, `runSystemd`, `runSystemdCommand`, `handleExecResize` are each introduced once and reused with identical signatures throughout. `execIn` attach hijacks `/exec/{id}/start` (it starts the process), so no `ContainerExecStart` call appears anywhere — consistent with the SDK behavior verified against v27.1.0.
