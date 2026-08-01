# Host-Side xdg-dbus-proxy for Filtered Container D-Bus Access — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give container apps filtered access to the host session D-Bus through a per-launch `xdg-dbus-proxy` running on the host, with a flatpak-style `dbus.talk`/`dbus.own` allowlist in the profile, and no D-Bus access at all when `xdg-dbus-proxy` isn't installed.

**Architecture:** `toolpod launch` (the host process) spawns `xdg-dbus-proxy` with a filter allowlist derived from the profile's new `dbus` field, listening on a socket in `$XDG_RUNTIME_DIR`. The container's `DBUS_SESSION_BUS_ADDRESS` points at that socket, and the real host bus socket (`$XDG_RUNTIME_DIR/bus`) is hidden from the container by bind-mounting `/dev/null` over it. The proxy is killed on launch exit. If `xdg-dbus-proxy` is absent on the host, the container gets no `DBUS_SESSION_BUS_ADDRESS` (bus disabled) and the real socket is still hidden.

**Tech Stack:** Go (kong CLI, docker SDK), xdg-dbus-proxy, Debian base image, profile YAML schema.

## Global Constraints

- Container D-Bus access must go **only** through the per-launch proxy socket; the real host bus socket must be unreachable from the container.
- If `xdg-dbus-proxy` is not installed on the host, the container gets **no** `DBUS_SESSION_BUS_ADDRESS` (bus disabled entirely).
- The proxy process must be terminated and its socket removed when `toolpod launch` exits.
- Profile merge semantics follow the repo convention: maps merge key-by-key, lists/scalars replace (see `internal/profile/merge.go`).
- No code comments unless the code doesn't make the intent apparent (repo convention).
- Commits use conventional format (`feat:`, `fix:`, `test:`).
- `go test ./...` and `go vet ./...` must pass after every task.
- **Executor warning:** `internal/profile/types_test.go` and `internal/runtime/docker_test.go` already exist with unrelated tests. The test functions in this plan must be **appended** to those files — never overwrite them. Only `pkg/toolpod/dbusproxy_test.go` is a genuinely new file.

---

### Task 1: `dbus` profile schema, merge, and validation

**Files:**
- Modify: `internal/profile/types.go`
- Modify: `internal/profile/merge.go:150-190`
- Modify: `internal/profile/validate.go:22-42`
- Modify: `internal/profile/catalog.go:253-296` (`collectNullKeys`)
- Test: `internal/profile/types_test.go` (append to — file exists with unrelated tests)

**Interfaces:**
- Consumes: existing `Profile` struct, `mergeMap[V any]`, `validate()`.
- Produces: `Profile.Dbus *DbusConfig`, `DbusConfig{Talk, Own map[string]bool}`. `MergeProfiles` merges `dbus` so a child profile's `talk`/`own` keys are unioned with the parent's (map key-by-key). `validate()` rejects invalid D-Bus names.

- [ ] **Step 1: Write the failing tests**

```go
// internal/profile/types_test.go
package profile

import (
	"strings"
	"testing"
)

func TestParseAndMergeDbus(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "gui.yaml", `version: 1
dbus:
  talk:
    org.freedesktop.portal.Desktop: true
`)
	mustWriteProfile(t, dir, "app.yaml", `version: 1
extends: gui
command: ["app"]
image: img:1
dbus:
  talk:
    org.freedesktop.Notifications: true
  own:
    xyz.block.buzz.app: true
`)
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "app")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dbus == nil {
		t.Fatal("dbus missing after merge")
	}
	if !cfg.Dbus.Talk["org.freedesktop.portal.Desktop"] {
		t.Error("talk from parent (gui) lost")
	}
	if !cfg.Dbus.Talk["org.freedesktop.Notifications"] {
		t.Error("talk from child lost")
	}
	if !cfg.Dbus.Own["xyz.block.buzz.app"] {
		t.Error("own from child lost")
	}
}

func TestValidateDbusRejectsBadName(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "bad.yaml", `version: 1
command: ["x"]
image: img:1
dbus:
  talk:
    "not a bus name!": true
`)
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProfile(cat, "bad"); err == nil {
		t.Fatal("expected validation error for invalid dbus name")
	} else if !strings.Contains(err.Error(), "dbus") {
		t.Fatalf("error should mention dbus: %v", err)
	}
}

func TestMergeDbusNullClears(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "gui.yaml", `version: 1
dbus:
  talk:
    org.freedesktop.portal.Desktop: true
`)
	mustWriteProfile(t, dir, "app.yaml", `version: 1
extends: gui
command: ["app"]
image: img:1
dbus: null
`)
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "app")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dbus != nil {
		t.Errorf("dbus should be cleared by null, got %+v", cfg.Dbus)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/profile/ -run 'TestParseAndMergeDbus|TestValidateDbusRejectsBadName'`
Expected: FAIL — `unknown field "dbus"` (or `cfg.Dbus` nil).

- [ ] **Step 3: Add the schema**

```go
// internal/profile/types.go — append to Profile struct and file
	Dbus *DbusConfig `yaml:"dbus,omitempty"`
}

// DbusConfig is a flatpak-style session-bus allowlist. Talk names may be
// called; Own names may be acquired. Values are maps (not lists) so profiles
// extending a base merge their names key-by-key.
type DbusConfig struct {
	Talk map[string]bool `yaml:"talk,omitempty"`
	Own  map[string]bool `yaml:"own,omitempty"`
}
```

- [ ] **Step 4: Add the merge**

```go
// internal/profile/merge.go — in MergeProfiles, alongside the other map merges:
	out.Dbus = mergeDbus(parent.Dbus, child.Dbus, child.NullKeys["dbus"])

// mergeDbus unions talk/own key-by-key (child wins per key; there are no
// per-key values). nullKeys supports the repo's null-to-delete convention:
// "*" clears the whole dbus config; "talk"/"own" clear that sub-map.
func mergeDbus(parent, child *DbusConfig, nullKeys map[string]bool) *DbusConfig {
	if child == nil {
		return parent
	}
	if nullKeys["*"] {
		return nil
	}
	if parent == nil {
		parent = &DbusConfig{}
	}
	out := &DbusConfig{}
	if nullKeys["talk"] {
		out.Talk = map[string]bool{}
	} else {
		out.Talk = mergeMap(parent.Talk, child.Talk, nil)
	}
	if nullKeys["own"] {
		out.Own = map[string]bool{}
	} else {
		out.Own = mergeMap(parent.Own, child.Own, nil)
	}
	if len(out.Talk) == 0 && len(out.Own) == 0 {
		return nil
	}
	return out
}
```

Also register `dbus` in `collectNullKeys` (`internal/profile/catalog.go`) so `dbus: null` (and `dbus: {talk: null}` / `dbus: {own: null}`) are captured for the merge:

```go
	nulls := map[string]map[string]bool{
		"mounts":      {},
		"environment": {},
		"tools":       {},
		"caches":      {},
		"labels":      {},
		"ports":       {},
		"devices":     {},
		"dbus":        {},
	}
```

Null-delete covers the whole config (`dbus: null`), the `talk` sub-map, and the `own` sub-map. Deleting a *single* inherited name (e.g. `dbus: {talk: {org.freedesktop.portal.Desktop: null}}`) is not supported — `collectNullKeys` does not descend into `dbus.talk`'s names — and that is an accepted limitation (flatpak likewise offers no per-name removal).

- [ ] **Step 5: Add validation**

```go
// internal/profile/validate.go — import "regexp", add near the top of file:
var busNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*(\.[*])?$`)

// in validate(), after validateDevices:
	if err := validateDbus(rc); err != nil {
		return err
	}

func validateDbus(rc RawProfile) error {
	if rc.Dbus == nil {
		return nil
	}
	for name := range rc.Dbus.Talk {
		if !busNameRe.MatchString(name) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("dbus.talk: invalid bus name %q", name)}
		}
	}
	for name := range rc.Dbus.Own {
		if !busNameRe.MatchString(name) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("dbus.own: invalid bus name %q", name)}
		}
	}
	return nil
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/profile/`
Expected: PASS. Also `go vet ./internal/profile/`.

- [ ] **Step 7: Commit**

```bash
git add internal/profile/types.go internal/profile/merge.go internal/profile/validate.go internal/profile/types_test.go
git commit -m "feat(profile): add dbus talk/own allowlist schema"
```

---

### Task 2: Proxy filter args and `startBusProxy`

**Files:**
- Create: `pkg/toolpod/dbusproxy.go`
- Test: `pkg/toolpod/dbusproxy_test.go` (create)

**Interfaces:**
- Consumes: `profile.DbusConfig` (Task 1).
- Produces: `proxyFilterArgs(cfg profile.Profile) []string` (pure), `startBusProxy(cfg profile.Profile) (cleanup func(), busAddr string)`. `busAddr` is the container-side `DBUS_SESSION_BUS_ADDRESS` value, or `""` when the bus is disabled.

- [ ] **Step 1: Write the failing tests**

```go
// pkg/toolpod/dbusproxy_test.go
package toolpod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgillich/toolpod/internal/profile"
)

func TestProxyFilterArgs(t *testing.T) {
	cfg := profile.Profile{Dbus: &profile.DbusConfig{
		Talk: map[string]bool{"org.freedesktop.portal.Desktop": true},
		Own:  map[string]bool{"xyz.block.buzz.app": true},
	}}
	args := proxyFilterArgs(cfg)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--see=org.freedesktop.portal.Desktop",
		"--talk=org.freedesktop.portal.Desktop",
		"--own=xyz.block.buzz.app",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
}

func TestStartBusProxyNoConfigDisables(t *testing.T) {
	// No dbus config -> no proxy, empty address (bus disabled).
	cfg := profile.Profile{}
	cleanup, addr := startBusProxy(cfg)
	if addr != "" {
		t.Errorf("addr = %q, want empty when profile has no dbus config", addr)
	}
	if cleanup != nil {
		t.Error("cleanup should be nil when no proxy started")
	}
}

func TestStartBusProxySpawnsAndFilters(t *testing.T) {
	// Fake xdg-dbus-proxy records its args to a file, then sleeps. It creates
	// the socket file at its second positional arg (the socket path) so
	// startBusProxy's readiness poll can observe it.
	dir := t.TempDir()
	record := filepath.Join(dir, "args")
	proxy := filepath.Join(dir, "xdg-dbus-proxy")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\n: > \"$2\"\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(proxy, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")

	cfg := profile.Profile{Dbus: &profile.DbusConfig{
		Talk: map[string]bool{"org.freedesktop.portal.Desktop": true},
	}}
	cleanup, addr := startBusProxy(cfg)
	if cleanup == nil {
		t.Fatal("expected a running proxy")
	}
	defer cleanup()
	if !strings.HasPrefix(addr, "unix:path="+dir+"/toolpod-bus-") {
		t.Errorf("addr = %q, want unix:path in XDG_RUNTIME_DIR", addr)
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	gotS := string(got)
	for _, want := range []string{
		"unix:path=/run/user/1000/bus", // host bus address
		"--filter",
		"--talk=org.freedesktop.portal.Desktop",
	} {
		if !strings.Contains(gotS, want) {
			t.Errorf("proxy args missing %q:\n%s", want, gotS)
		}
	}
	// The socket path must be a positional arg (a plain path).
	if !strings.Contains(gotS, "\n"+filepath.Join(dir, "toolpod-bus-")) {
		t.Errorf("proxy should be given the socket path as a plain path:\n%s", gotS)
	}
	// cleanup is deferred; the explicit-call-and-nil dance would double-run it.
}

func TestStartBusProxyMissingBinaryDisables(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir) // no xdg-dbus-proxy here
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	cfg := profile.Profile{Dbus: &profile.DbusConfig{
		Talk: map[string]bool{"org.freedesktop.portal.Desktop": true},
	}}
	cleanup, addr := startBusProxy(cfg)
	if cleanup != nil || addr != "" {
		t.Errorf("expected disabled bus (cleanup=%v addr=%q) when proxy binary missing", cleanup, addr)
	}
}

func TestStartBusProxyFallsBackToRuntimeDirBus(t *testing.T) {
	// With DBUS_SESSION_BUS_ADDRESS unset, the host bus address falls back to
	// unix:path=$XDG_RUNTIME_DIR/bus.
	// Fake xdg-dbus-proxy records its args, then sleeps. It creates the socket
	// file at its second positional arg so the readiness poll can observe it.
	dir := t.TempDir()
	record := filepath.Join(dir, "args")
	proxy := filepath.Join(dir, "xdg-dbus-proxy")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\n: > \"$2\"\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(proxy, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	cfg := profile.Profile{Dbus: &profile.DbusConfig{
		Talk: map[string]bool{"org.freedesktop.portal.Desktop": true},
	}}
	cleanup, addr := startBusProxy(cfg)
	if cleanup == nil {
		t.Fatal("expected a running proxy")
	}
	defer cleanup()
	if addr == "" {
		t.Fatal("expected a proxy bus address")
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	if !strings.Contains(string(got), "unix:path="+dir+"/bus") {
		t.Errorf("host bus should fall back to $XDG_RUNTIME_DIR/bus:\n%s", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/toolpod/ -run 'TestProxyFilterArgs|TestStartBusProxy'`
Expected: FAIL — `proxyFilterArgs` and `startBusProxy` undefined.

- [ ] **Step 3: Implement `pkg/toolpod/dbusproxy.go`**

```go
package toolpod

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jgillich/toolpod/internal/profile"
)

// proxyFilterArgs builds the flatpak-style allowlist flags for the profile's
// dbus config: --see + --talk for every talk name, --see + --own for every
// own name. Map iteration order is non-deterministic; callers must not rely
// on argument order.
func proxyFilterArgs(cfg profile.Profile) []string {
	if cfg.Dbus == nil {
		return nil
	}
	var args []string
	for name := range cfg.Dbus.Talk {
		args = append(args, "--see="+name, "--talk="+name)
	}
	for name := range cfg.Dbus.Own {
		args = append(args, "--see="+name, "--own="+name)
	}
	return args
}

// startBusProxy spawns a host-side xdg-dbus-proxy for the launch, filtered to
// the profile's dbus allowlist, listening on $XDG_RUNTIME_DIR/toolpod-bus-<pid>.sock.
// It returns a cleanup that kills the proxy and removes the socket, and the
// DBUS_SESSION_BUS_ADDRESS to set in the container ("" = bus disabled).
//
// The bus is disabled (no proxy, empty address) when the profile has no dbus
// config, there is no host session bus to proxy, or xdg-dbus-proxy is not
// installed on the host.
func startBusProxy(cfg profile.Profile) (func(), string) {
	if cfg.Dbus == nil || (len(cfg.Dbus.Talk) == 0 && len(cfg.Dbus.Own) == 0) {
		return nil, ""
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	hostBus := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if hostBus == "" {
		if runtimeDir != "" {
			hostBus = "unix:path=" + filepath.Join(runtimeDir, "bus")
		}
	}
	if runtimeDir == "" || hostBus == "" {
		return nil, ""
	}
	proxy, err := exec.LookPath("xdg-dbus-proxy")
	if err != nil {
		fmt.Fprintln(os.Stderr, "toolpod: warning: xdg-dbus-proxy not found; container D-Bus disabled")
		return nil, ""
	}
	sockPath := filepath.Join(runtimeDir, fmt.Sprintf("toolpod-bus-%d.sock", os.Getpid()))
	_ = os.Remove(sockPath)

	args := append([]string{proxy, hostBus, sockPath, "--filter"}, proxyFilterArgs(cfg)...)
	if os.Getenv("TOOLPOD_OPEN_DEBUG") != "" {
		args = append(args, "--log")
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "toolpod: warning: start xdg-dbus-proxy: %v\n", err)
		return nil, ""
	}
	// Wait for the proxy to create its socket so container clients don't race
	// startup (a client that connects before the proxy listens gets ENOENT).
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			_ = os.Remove(sockPath)
			fmt.Fprintln(os.Stderr, "toolpod: warning: xdg-dbus-proxy did not start; container D-Bus disabled")
			return nil, ""
		}
		time.Sleep(10 * time.Millisecond)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = os.Remove(sockPath)
	}
	return cleanup, "unix:path=" + sockPath
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/toolpod/ -run 'TestProxyFilterArgs|TestStartBusProxy'`
Expected: PASS. Then `go test ./...` and `go vet ./...`.

- [ ] **Step 5: Commit**

```bash
git add pkg/toolpod/dbusproxy.go pkg/toolpod/dbusproxy_test.go
git commit -m "feat(launch): spawn host-side xdg-dbus-proxy with profile dbus allowlist"
```

---

### Task 3: Wire into launch + hide the real bus socket

**Files:**
- Modify: `pkg/toolpod/launch.go:96-115` (non-dry-run path)
- Modify: `internal/runtime/docker_run.go:349-393` (`buildMounts`)
- Test: `internal/runtime/docker_test.go` (append to — file exists with ~530 lines of unrelated tests)

**Interfaces:**
- Consumes: `startBusProxy(cfg profile.Profile) (func(), string)` (Task 2).
- Produces: launch overrides `spec.Env["DBUS_SESSION_BUS_ADDRESS"]` to the proxy address (or `""` when disabled). `buildMounts` adds a `/dev/null` bind over `$XDG_RUNTIME_DIR/bus` whenever `spec.Env["XDG_RUNTIME_DIR"]` is set, so the container can never reach the real host bus.

- [ ] **Step 1: Write the failing test (runtime overlay)**

```go
// internal/runtime/docker_test.go
package runtime

import (
	"strings"
	"testing"
)

func TestBuildMountsHidesRealBusSocket(t *testing.T) {
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/ws", Target: "/ws"},
		Env:       map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
	}
	mounts, err := buildMounts(spec, "/root")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range mounts {
		if m.Target == "/run/user/1000/bus" {
			found = true
			if m.Source != "/dev/null" {
				t.Errorf("bus overlay source = %q, want /dev/null", m.Source)
			}
		}
	}
	if !found {
		t.Error("expected a mount over /run/user/1000/bus when XDG_RUNTIME_DIR is set")
	}
}

func TestBuildMountsNoBusOverlayWithoutRuntimeDir(t *testing.T) {
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/ws", Target: "/ws"},
		Env:       map[string]string{},
	}
	mounts, err := buildMounts(spec, "/root")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mounts {
		if strings.HasSuffix(m.Target, "/bus") {
			t.Errorf("unexpected bus overlay mount: %+v", m)
		}
	}
}

func TestBuildMountsSkipsOverlayWhenBusAlreadyMounted(t *testing.T) {
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/ws", Target: "/ws"},
		Mounts:    []MountSpec{{Target: "/run/user/1000/bus", Source: "/host/socket", ReadOnly: true}},
		Env:       map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
	}
	mounts, err := buildMounts(spec, "/root")
	if err != nil {
		t.Fatal(err)
	}
	devNull := 0
	for _, m := range mounts {
		if m.Target == "/run/user/1000/bus" && m.Source == "/dev/null" {
			devNull++
		}
	}
	if devNull != 0 {
		t.Errorf("should not overlay /dev/null when a mount already targets the bus path (got %d overlays)", devNull)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/ -run 'TestBuildMountsHide'`
Expected: FAIL — no mount targets `/run/user/1000/bus`.

- [ ] **Step 3: Add the overlay to `buildMounts`**

```go
// internal/runtime/docker_run.go — inside buildMounts, after the profile
// mounts loop and before the mise volume append:
	if rtDir := spec.Env["XDG_RUNTIME_DIR"]; rtDir != "" {
		busPath := filepath.Join(rtDir, "bus")
		overlaid := false
		for _, existing := range m {
			if existing.Target == busPath {
				overlaid = true
				break
			}
		}
		if !overlaid {
			m = append(m, mount.Mount{
				Type:   mount.TypeBind,
				Source: "/dev/null",
				Target: busPath,
			})
		}
	}
```

The loop skips the overlay if a profile mount already targets `$XDG_RUNTIME_DIR/bus`, avoiding a duplicate-target mount (Podman errors on those; Docker is last-wins).

- [ ] **Step 4: Wire the proxy into `LaunchWithWriter`**

```go
// pkg/toolpod/launch.go — in the non-dry-run branch, after Prepare and
// before rt.Run:
		cleanupProxy, busAddr := startBusProxy(cfg)
		if cleanupProxy != nil {
			defer cleanupProxy()
		}
		spec.Env["DBUS_SESSION_BUS_ADDRESS"] = busAddr

		code, err := rt.Run(ctx, runSpec)
```

`buildEnv` (internal/runtime/docker_run.go:465) already skips empty env values, so setting `spec.Env["DBUS_SESSION_BUS_ADDRESS"] = ""` removes the variable from the container (bus disabled).

> **Reachability note:** the proxy socket lives in the host's `$XDG_RUNTIME_DIR`, and the container reaches it only because the `gui` fragment mounts `$XDG_RUNTIME_DIR`. A profile that declares `dbus` but does **not** mount `$XDG_RUNTIME_DIR` (i.e. doesn't extend `gui`) will point `DBUS_SESSION_BUS_ADDRESS` at a socket the container can't see. Document this requirement in the profile docs (Task 4).

- [ ] **Step 5: Add a launch test for the disabled path**

```go
// pkg/toolpod/launch_test.go — extend TestLaunchWithFakeRuntime or add:
func TestLaunchOverridesBusAddressWhenDisabled(t *testing.T) {
	dir := writeBuiltinShell(t) // shell profile: no dbus config
	fr := &runtime.FakeRuntime{ExitCode: 0}
	var out strings.Builder
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		ProfileDir:  dir,
		Runtime:     fr,
	}, &out)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	if fr.RanSpec == nil {
		t.Fatal("Run not called")
	}
	if got, ok := fr.RanSpec.Env["DBUS_SESSION_BUS_ADDRESS"]; !ok || got != "" {
		t.Errorf("DBUS_SESSION_BUS_ADDRESS = %q, want empty (disabled)", got)
	}
}
```

Run: `go test ./pkg/toolpod/ -run TestLaunchOverridesBusAddressWhenDisabled`
Expected: PASS.

- [ ] **Step 6: Run all tests and vet**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/toolpod/launch.go internal/runtime/docker_run.go internal/runtime/docker_test.go pkg/toolpod/launch_test.go
git commit -m "feat(runtime): route container dbus through launch proxy and hide host bus socket"
```

---

### Task 4: Default allowlist in `gui` fragment + buzz profile config

**Files:**
- Modify: `internal/catalog/fragments/gui.yaml`
- Modify: `internal/catalog/profiles/buzz.yaml`
- Test: `internal/profile/catalog_test.go`

**Interfaces:**
- Consumes: `dbus` schema (Task 1).
- Produces: GUI profiles default to allowing `org.freedesktop.portal.Desktop` (link opening). Buzz additionally allows notifications and owns its app name.

> **Docs:** a profile that declares `dbus` must also mount `$XDG_RUNTIME_DIR` (the `gui` fragment does), or its `DBUS_SESSION_BUS_ADDRESS` points at a socket the container can't reach. Add this to the profile-format documentation when updating it.

- [ ] **Step 1: Update the gui fragment**

Remove the now-contradictory `DBUS_SESSION_BUS_ADDRESS` env line — the container's bus address is owned by the launch proxy logic (Task 3), which overrides it per launch; keeping the host-address template here would leak the real bus address into profiles that skip the override. This is an intentional behavior change: GUI apps move from direct host-bus access to filtered-proxy-or-disabled (constraint #1). Any GUI app whose bus names aren't in an allowlist loses bus access; the `org.freedesktop.portal.Desktop` default below keeps link-opening working.

```yaml
# internal/catalog/fragments/gui.yaml — remove line 21 (the env entry), append:
dbus:
  talk:
    org.freedesktop.portal.Desktop: true
```

- [ ] **Step 2: Add buzz's names**

```yaml
# internal/catalog/profiles/buzz.yaml — append:
dbus:
  talk:
    org.freedesktop.Notifications: true
  own:
    xyz.block.buzz.app: true
```

(The `org.freedesktop.portal.Desktop` talk comes from `gui` via map merge.)

- [ ] **Step 3: Write the failing resolve test**

```go
// internal/profile/catalog_test.go — append:
func TestResolveBuzzDbusAllowlist(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "buzz")
	if err != nil {
		t.Fatalf("ResolveProfile(buzz): %v", err)
	}
	if cfg.Dbus == nil {
		t.Fatal("buzz should resolve a dbus allowlist (via gui)")
	}
	for _, name := range []string{"org.freedesktop.portal.Desktop", "org.freedesktop.Notifications"} {
		if !cfg.Dbus.Talk[name] {
			t.Errorf("dbus.talk missing %q", name)
		}
	}
	if !cfg.Dbus.Own["xyz.block.buzz.app"] {
		t.Error("dbus.own missing xyz.block.buzz.app")
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/profile/ -run TestResolveBuzzDbusAllowlist`
Expected: PASS.

- [ ] **Step 5: Run all tests and vet**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/catalog/fragments/gui.yaml internal/catalog/profiles/buzz.yaml internal/profile/catalog_test.go
git commit -m "feat(catalog): default gui dbus allowlist; buzz notifications + own name"
```

---

### Task 5: End-to-end verification + discover buzz's required bus names

> **This task is manual.** It requires a desktop host with `xdg-dbus-proxy` installed and a working Buzz AppImage (real display + session bus). It cannot be run in a headless/CI environment. The proxy args from Task 2 are unit-tested; this task validates the real end-to-end behavior and the exact bus names buzz needs.

**Files:**
- Modify: `internal/catalog/profiles/buzz.yaml` (as needed)

**Interfaces:**
- Consumes: Tasks 1–4 (a `toolpod` binary with the proxy wiring, and an image rebuilt if the wrapper changed).
- Produces: verified behavior — links open through the proxy; non-allowlisted names blocked; bus disabled when the proxy is absent.

- [ ] **Step 1: Build and launch with debug proxy logging**

On a desktop host with `xdg-dbus-proxy` installed:

```bash
make install
TOOLPOD_OPEN_DEBUG=1 toolpod launch buzz
```

`startBusProxy` passes `--log` to the proxy whenever `TOOLPOD_OPEN_DEBUG` is set, so its stderr shows every bus name the app touches. Confirm:
- The container's `DBUS_SESSION_BUS_ADDRESS` points at `$XDG_RUNTIME_DIR/toolpod-bus-<pid>.sock`.
- `$XDG_RUNTIME_DIR/bus` inside the container is `/dev/null` (not a socket).
- Clicking a link opens the host browser (portal call via the proxy).

- [ ] **Step 2: Verify blocking**

Get a shell in the running buzz container (toolpod has no `exec` subcommand; use the container engine directly, or open a terminal in the buzz profile):

```bash
CONTAINER=$(podman ps --format '{{.Names}}' | grep '^toolpod-buzz-' | head -1)
podman exec -it "$CONTAINER" bash
```

Then, from inside the container (the proxy address is in `$DBUS_SESSION_BUS_ADDRESS`), a call to an allowlisted name must be forwarded, while a non-allowlisted name must be refused:

```bash
# allowed (portal) — expect a reply or ServiceUnknown (forwarded to the host bus):
dbus-send --session --print-reply --dest=org.freedesktop.portal.Desktop \
  /org/freedesktop/portal/desktop org.freedesktop.portal.OpenURI.OpenURI \
  string:'' string:'https://example.com' dict:string:variant:
# blocked (any non-allowlisted service name) — expect ServiceUnknown without reaching the host:
dbus-send --session --print-reply --dest=com.example.NotInAllowlist \
  /com/example com.example.NotInAllowlist.Ping
```

Expected: the first returns a reply/ServiceUnknown (forwarded), the second returns `ServiceUnknown` without reaching the host.

- [ ] **Step 3: Discover buzz's real bus names from `--log`**

Run with the proxy `--log` enabled and exercise buzz (agent auth, notifications, deep-link). Collect any names the proxy reports as blocked (`AccessDenied`/`ServiceUnknown` in the log) and add them to `internal/catalog/profiles/buzz.yaml` under `dbus.talk`/`dbus.own`. Common candidates: the single-instance/deep-link own name (likely the Tauri identifier or the URL-scheme name). Note that trailing `.*` wildcards (e.g. `org.freedesktop.portal.*`) are valid in `dbus.talk`/`dbus.own` and accepted by the proxy, matching the flatpak convention. Re-run and repeat until the buzz UI shows no bus errors.

- [ ] **Step 4: Verify the disabled path**

On a host WITHOUT `xdg-dbus-proxy` (or temporarily rename it), launch a GUI profile and confirm the container has no `DBUS_SESSION_BUS_ADDRESS` and cannot reach `$XDG_RUNTIME_DIR/bus` (it is `/dev/null`). Document in `docs/` that GUI apps needing a session bus (e.g. Buzz's notifications/single-instance) will not get one in this case.

- [ ] **Step 5: Commit any profile changes from Step 3**

```bash
git add internal/catalog/profiles/buzz.yaml
git commit -m "feat(catalog): add buzz dbus names discovered from proxy log"
```

---

## Self-Review

- **Spec coverage:** (1) host-side proxy per launch — Task 2/3; (2) flatpak-style talk config — Task 1/4; (3) disable dbus entirely when proxy absent — Task 2 (`startBusProxy` returns `""` + launch sets env to empty) and Task 3 (overlay always hides the real socket); (4) proxy closed on exit — Task 2 cleanup deferred in Task 3; (5) hide the real bus socket so the proxy can't be bypassed — Task 3 overlay. ✓
- **Placeholder scan:** no TBDs; all steps carry concrete code or commands. The only "discover at runtime" step (Task 5 Step 3) is explicitly an iterative verification with concrete commands and the exact file/field to update. ✓
- **Type consistency:** `profile.DbusConfig{Talk, Own map[string]bool}` used consistently across Tasks 1, 2, 4; `startBusProxy(cfg profile.Profile) (func(), string)` and `proxyFilterArgs(cfg profile.Profile) []string` signatures match between Tasks 2 and 3; `mergeDbus(parent, child *DbusConfig, nullKeys map[string]bool) *DbusConfig` matches its single call in `MergeProfiles`. ✓
