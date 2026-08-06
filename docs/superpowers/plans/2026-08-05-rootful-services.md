# Rootful-Mode Services Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make tpd's `services:` (companion socket-exposing containers) work in rootful Mode B (rootful Docker/Podman), removing the current rootless-only hard block.

**Architecture:** Two blockers make services rootless-only today (`docker_services.go`): (1) the host run-dir for rootful is `/run/tpd-svc-<name>-<uid>/`, which the host tpd process (a non-root user) cannot create; (2) a rootful service creates its socket `root:root 0755`, so the host user's `connect()` readiness probe gets EACCES. The fix relocates the rootful run-dir to `/tmp/tpd-svc-<name>-<uid>/` (user-writable, with an ownership/symlink check), and on first start chowns the socket inside the service container via a synchronous `ContainerExec` (`chown <hostUID>:<hostGID>` + `chmod 0770`) the first time the socket appears, so the existing host `connect()` probe works identically to rootless. The main container's bootstrap chown of `Spec.SocketPaths` (`docker_run.go:70-73`) stays the mechanism that guarantees the consumer (host user after setpriv) can connect even when the socket is root-owned at mount time (e.g. after a daemon socket-recreation on reuse).

**Tech Stack:** Go 1.25, docker/docker client library, `golang.org/x/sys/unix`, stdlib `syscall`. CGO off. Tests via `go test ./...`. Lint via `go vet ./...`.

## Global Constraints

- Go 1.25, CGO disabled in releases. `go test ./...` and `go vet ./...` must pass after every task.
- No comments unless the code doesn't make something apparent (AGENTS.md rule).
- Conventional commit format (`feat:`, `fix:`, `refactor:`, `test:`, `docs:`).
- **Rootful run-dir is `/tmp/tpd-svc-<name>-<uid>/` always** — no XDG_RUNTIME_DIR branch. Rationale: it must be writable by the host tpd process in headless/sudo contexts where XDG_RUNTIME_DIR is unset, and it must be transient (sockets must not live on the persistent home FS). The `<uid>` suffix keeps the rootful tree (`tpd-svc-<name>-<uid>` container) from colliding with a concurrent rootless run (`tpd-svc-<name>` container) that shares `/run/user/<uid>`.
- **The rootful run-dir is verified after creation**: `ensureServiceRunDir` rejects a pre-existing symlink or a dir not owned by the current uid. `/tmp` is world-writable, so a malicious local user could otherwise redirect sockets or drop files there.
- **The probe-time chown runs exactly once per exposed socket, on first start only.** The reuse path (`docker_services.go:140-148`) never probes and never chowns — that is the existing documented reuse optimization; the main-container bootstrap chown (`docker_run.go:70-73`) is what grants the consumer access on reuse.
- **The probe-time chown is NOT skipped for `svc.Privileged`** — socket ownership is orthogonal to privileges: a privileged rootful service still creates its socket as host root, so the chown is still required for tpd's probe.
- **A failed chown/chmod exec is a hard probe error**, surfaced with the exit code (e.g. a service image without `chown`/`chmod` exits 127). It is never silently ignored.
- **The rootless probe loop stays byte-identical** apart from the signature change; rootless behavior must not change.
- The exec-completion wait uses `ContainerExecCreate` → `ContainerExecStart` → poll `ContainerExecInspect` until `Running==false`. Not the `ContainerExecAttach` drain: attach requires a hijacked stream (no test fake in this repo handles hijacking today), and it doesn't surface the exec exit code — `ExecInspect` is needed anyway to detect a missing binary (exit 127).
- **`/tmp` cleanup tradeoff is documented, not coded around**: `systemd-tmpfiles` may clear `/tmp` after ~10 days. A service kept alive across launches that long would lose its socket dir; the running daemon keeps its bound socket but the next launch's bind mount breaks. tpd services are disposable by design; the design doc records the caveat. XDG_RUNTIME_DIR is not used because it is unset in the headless/sudo contexts rootful mode is typically used in.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/runtime/docker_services.go` | `serviceRunDir` rootful → `/tmp/...`; new `ensureServiceRunDir`; remove both rootful guards; `waitForServiceSocket` becomes a method with rootful chown branch; new `chownServiceSocket`/`runServiceExec`; `createService` call-site + `%w` wrap |
| `internal/runtime/docker_services_test.go` | Replace `TestStartServicesRejectsRootful`; add rootful lifecycle/stop/run-dir/ownership/chown-exec tests; extend `fakeServicesDaemon` with exec endpoints + narrowed `/start` arm |
| `docs/superpowers/specs/2026-08-04-services-design.md` | Rootful run-dir path, probe-time chown, tmpfiles caveat, drop "rootless-only" framing |
| `AGENTS.md` | Runtime notes: services supported in both modes |

---

## Task 1: Relocate the rootful run-dir to `/tmp` with ownership verification

**Files:**
- Modify: `internal/runtime/docker_services.go` — `serviceRunDir` var (lines 31-36), `createService` run-dir creation (line 223), add `ensureServiceRunDir` + `syscall` import.
- Test: `internal/runtime/docker_services_test.go` — add `TestServiceRunDirPaths`, `TestEnsureServiceRunDir`.

**Interfaces:**
- Consumes: `workspace.Mode`, `os.Getuid`, `os.Lstat`, `syscall.Stat_t`.
- Produces: `func ensureServiceRunDir(path string) error` — `MkdirAll(path, 0o700)`, then `Lstat` and require a real directory owned by `os.Getuid()`; errors otherwise. `createService` calls it in place of the raw `os.MkdirAll(runDir, 0o700)`. Later tasks rely on `serviceRunDir` returning the new `/tmp` path for rootful.

- [ ] **Step 1: Write the failing path tests**

Add to `internal/runtime/docker_services_test.go` (do NOT use `overrideServicePaths` here — it replaces `serviceRunDir`):

```go
func TestServiceRunDirPaths(t *testing.T) {
	uid := os.Getuid()
	if got := serviceRunDir("db", workspace.ModeRootless); got != fmt.Sprintf("/run/user/%d/tpd-svc-db/", uid) {
		t.Errorf("rootless run dir = %q, want /run/user/%d/tpd-svc-db/", got, uid)
	}
	if got := serviceRunDir("db", workspace.ModeRootful); got != fmt.Sprintf("/tmp/tpd-svc-db-%d/", uid) {
		t.Errorf("rootful run dir = %q, want /tmp/tpd-svc-db-%d/", got, uid)
	}
}

func TestEnsureServiceRunDir(t *testing.T) {
	base := t.TempDir()
	if err := ensureServiceRunDir(filepath.Join(base, "ok")); err != nil {
		t.Fatalf("fresh dir must be accepted: %v", err)
	}
	if err := ensureServiceRunDir(filepath.Join(base, "ok")); err != nil {
		t.Fatalf("existing own dir must be accepted: %v", err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureServiceRunDir(file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("a regular file must be rejected, got %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "ok"), link); err != nil {
		t.Fatal(err)
	}
	if err := ensureServiceRunDir(link); err == nil {
		t.Error("a symlink must be rejected (Lstat, not Stat)")
	}
}
```

`os`, `filepath`, `strings`, and `workspace` are already imported in this file; `syscall` is not needed in the test.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/runtime/ -run 'TestServiceRunDirPaths|TestEnsureServiceRunDir' -v`
Expected: FAIL — `TestServiceRunDirPaths` gets `/run/tpd-svc-db-<uid>/`; `TestEnsureServiceRunDir` fails to compile (`ensureServiceRunDir` undefined).

- [ ] **Step 3: Implement `ensureServiceRunDir` and the new run-dir**

In `internal/runtime/docker_services.go`, add `"syscall"` to the imports, change `serviceRunDir`:

```go
var serviceRunDir = func(name string, mode workspace.Mode) string {
	if mode == workspace.ModeRootless {
		return fmt.Sprintf("/run/user/%d/tpd-svc-%s/", os.Getuid(), name)
	}
	// Rootful sockets must live where the host tpd (a non-root user) can
	// create, unlink, and probe them; /tmp is user-writable and transient, and
	// the uid suffix keeps this tree distinct from a concurrent rootless run.
	return fmt.Sprintf("/tmp/tpd-svc-%s-%d/", name, os.Getuid())
}
```

Add the helper (place it near `acquireServiceLock`):

```go
// ensureServiceRunDir creates the host run-dir and rejects a pre-existing
// symlink or a directory owned by another user: /tmp is world-writable, so a
// malicious local user could otherwise redirect sockets or drop files. Expose
// parent dirs sit inside the verified dir (0700, ours), so they need no check.
func ensureServiceRunDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("service run dir %s is not a directory", path)
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("service run dir %s is not owned by the current user", path)
	}
	return nil
}
```

In `createService`, replace:

```go
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return fmt.Errorf("create service run dir: %w", err)
	}
```

with:

```go
	if err := ensureServiceRunDir(runDir); err != nil {
		return fmt.Errorf("create service run dir: %w", err)
	}
```

The expose-parent `os.MkdirAll(serviceSocketPath(name, mode, parent), 0o700)` (line ~230) stays as-is — it runs inside the now-verified dir.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/runtime/ -run 'TestServiceRunDirPaths|TestEnsureServiceRunDir' -v`
Expected: PASS.

- [ ] **Step 5: Run the full runtime suite**

Run: `go test ./internal/runtime/`
Expected: all existing service tests still pass (`overrideServicePaths` redirects `serviceRunDir` to temp dirs, so the production path change doesn't touch them).

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/docker_services.go internal/runtime/docker_services_test.go
git commit -m "feat(services): move rootful run-dir to user-writable /tmp with ownership check"
```

---

## Task 2: Remove the rootful guards and cover the rootful lifecycle

**Files:**
- Modify: `internal/runtime/docker_services.go` — delete the `ModeRootful` error in `StartServices` (lines 77-79) and the early-return in `StopServices` (lines 468-471).
- Test: `internal/runtime/docker_services_test.go` — replace `TestStartServicesRejectsRootful` (line 789) with `TestStartServicesRootfulLifecycle`; add `TestStopServicesRootful`.

**Interfaces:**
- Consumes: `serviceContainerName(name, mode)` already returns `tpd-svc-<name>-<uid>` for rootful; `serviceRunDir` from Task 1.
- Produces: proof that a rootful launch creates the uid-suffixed container, probes its socket, binds it, and that rootful stop removes it.

- [ ] **Step 1: Replace the rejection test with the rootful lifecycle expectation**

Replace `TestStartServicesRejectsRootful` (currently `docker_services_test.go:789-803`) with:

```go
func TestStartServicesRootfulLifecycle(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	svcName := fmt.Sprintf("tpd-svc-db-%d", os.Getuid())
	daemon.sockets = map[string][]string{
		svcName: {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Workspace.Mode = workspace.ModeRootful
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if daemon.createCount != 1 {
		t.Fatalf("ContainerCreate calls = %d, want 1", daemon.createCount)
	}
	if daemon.createdNames[0] != svcName {
		t.Errorf("rootful service container name = %q, want %q", daemon.createdNames[0], svcName)
	}
	want := filepath.Join(runDir, "db", "run", "db.sock")
	if got := bindings.Sockets["db/port"]; got != want {
		t.Errorf("binding db/port = %q, want %q", got, want)
	}
}

func TestStopServicesRootful(t *testing.T) {
	overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	svcName := fmt.Sprintf("tpd-svc-db-%d", os.Getuid())
	daemon.containers = []types.Container{{
		ID:     "svc-1",
		Names:  []string{"/" + svcName},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "hash123"},
	}}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Workspace.Mode = workspace.ModeRootful
	if err := rt.StopServices(context.Background(), spec); err != nil {
		t.Fatalf("StopServices: %v", err)
	}
	if daemon.stopCount != 1 {
		t.Errorf("ContainerStop calls = %d, want 1", daemon.stopCount)
	}
	if !containsString(daemon.removed, "svc-1") {
		t.Errorf("rootful service container not removed; removed = %v", daemon.removed)
	}
}
```

`serviceSpec` returns a `Spec` with `Mode: workspace.ModeRootless`; overriding `spec.Workspace.Mode` is how the rootful cases are built.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/runtime/ -run 'TestStartServicesRootfulLifecycle|TestStopServicesRootful' -v`
Expected: FAIL — `TestStartServicesRootfulLifecycle` errors with `services are not supported in rootful mode`; `TestStopServicesRootful` reports zero stop calls.

- [ ] **Step 3: Remove the two rootful guards**

In `StartServices` (`docker_services.go:76-82`), delete:

```go
	if spec.Workspace.Mode == workspace.ModeRootful {
		return ServiceBindings{Sockets: map[string]string{}, Release: func() {}}, fmt.Errorf("services are not supported in rootful mode; use rootless podman")
	}
```

In `StopServices` (`docker_services.go:468-471`), delete:

```go
	if spec.Workspace.Mode == workspace.ModeRootful {
		return nil
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/runtime/ -run 'TestStartServicesRootfulLifecycle|TestStopServicesRootful' -v`
Expected: PASS.

- [ ] **Step 5: Run the full runtime suite**

Run: `go test ./internal/runtime/`
Expected: PASS (the fake binds the host socket as the test user, so the probe connects even before Task 3 adds the chown).

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/docker_services.go internal/runtime/docker_services_test.go
git commit -m "feat(services): support rootful mode lifecycle"
```

---

## Task 3: Rootful probe-time chown (once, synchronous, errors surfaced)

**Files:**
- Modify: `internal/runtime/docker_services.go` — `waitForServiceSocket` becomes a `DockerRuntime` method with a rootful branch; new `chownServiceSocket` and `runServiceExec`; update the `createService` probe call site (lines 313-319) and wrap its error with `%w`.
- Test: `internal/runtime/docker_services_test.go` — extend `fakeServicesDaemon` (exec endpoints, narrowed `/start` arm, `execCmds`/`execExitCode` fields); add `TestStartServicesRootfulChownsSocket` and `TestStartServicesRootfulChownFailure`.

**Interfaces:**
- Consumes: `createService`'s `containerID` and per-expose `exposePath` (the container-side socket path); `os.Getuid`/`os.Getgid`.
- Produces: `func (d *DockerRuntime) waitForServiceSocket(ctx, containerID, hostPath, containerPath string, rootful bool) error`; `func (d *DockerRuntime) chownServiceSocket(ctx, containerID, containerPath string) error`; `func (d *DockerRuntime) runServiceExec(ctx, containerID string, cmd []string) error`. `createService` calls `d.waitForServiceSocket(ctx, containerID, serviceSocketPath(name, mode, exposePath), exposePath, mode == workspace.ModeRootful)`.

- [ ] **Step 1: Write the failing chown-exec tests**

Add to `internal/runtime/docker_services_test.go`:

```go
func TestStartServicesRootfulChownsSocket(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	svcName := fmt.Sprintf("tpd-svc-db-%d", os.Getuid())
	daemon.sockets = map[string][]string{
		svcName: {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Workspace.Mode = workspace.ModeRootful
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	wantUID := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	if len(daemon.execCmds) != 2 {
		t.Fatalf("exec calls = %v, want exactly one chown + one chmod", daemon.execCmds)
	}
	if daemon.execCmds[0][0] != "chown" || daemon.execCmds[0][1] != wantUID || daemon.execCmds[0][2] != "/run/db.sock" {
		t.Errorf("first exec = %v, want chown %s /run/db.sock", daemon.execCmds[0], wantUID)
	}
	if daemon.execCmds[1][0] != "chmod" || daemon.execCmds[1][1] != "0770" || daemon.execCmds[1][2] != "/run/db.sock" {
		t.Errorf("second exec = %v, want chmod 0770 /run/db.sock", daemon.execCmds[1])
	}
}

func TestStartServicesRootfulChownFailure(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	overrideProbeTimeout(t, time.Second)
	daemon := newFakeServicesDaemon()
	daemon.execExitCode = 127
	svcName := fmt.Sprintf("tpd-svc-db-%d", os.Getuid())
	daemon.sockets = map[string][]string{
		svcName: {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Workspace.Mode = workspace.ModeRootful
	_, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err == nil || !strings.Contains(err.Error(), "chown") || !strings.Contains(err.Error(), "exit code 127") {
		t.Fatalf("a failing chown exec must surface as an error, got %v", err)
	}
}
```

`overrideServicePaths` redirects `serviceRunDir` to a temp dir, so the fake's bound socket path and the production `serviceSocketPath` agree; `overrideProbeTimeout` shrinks the deadline so a real timeout (if the exec path regressed) fails fast.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/runtime/ -run 'TestStartServicesRootfulChownsSocket|TestStartServicesRootfulChownFailure' -v`
Expected: FAIL with a **compile error** — both tests reference `daemon.execCmds`/`daemon.execExitCode`, fields the fake only gains in Step 4. That is the expected red state; resolve it by completing Step 3 (probe) and Step 4 (fake fields) before re-running. The existing rootless probe (unchanged here) would otherwise let `StartServices` succeed with zero execs.

- [ ] **Step 3: Implement the probe changes in `docker_services.go`**

Replace the free function `waitForServiceSocket` (`docker_services.go:439-462`) with the method + helpers:

```go
// waitForServiceSocket probes a unix socket with connect() (a stale file or a
// touched-but-not-accepting socket both fail the dial) until it accepts, the
// deadline passes, or the context is canceled. In rootful mode the socket is
// created root-owned and is unconnectable by the host user, so the first time
// the host path appears as a socket we exec chown/chmod inside the service
// (running as root) to make it host-user-connectable before dialing.
func (d *DockerRuntime) waitForServiceSocket(ctx context.Context, containerID, hostPath, containerPath string, rootful bool) error {
	deadline := time.Now().Add(serviceProbeTimeout)
	dialer := net.Dialer{Timeout: time.Second}
	chowned := false
	for {
		if rootful && !chowned {
			if st, err := os.Lstat(hostPath); err == nil && st.Mode()&os.ModeSocket != 0 {
				if err := d.chownServiceSocket(ctx, containerID, containerPath); err != nil {
					return err
				}
				chowned = true
			}
		}
		conn, err := dialer.DialContext(ctx, "unix", hostPath)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("socket %s did not appear", hostPath)
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// chownServiceSocket makes the socket file host-user-connectable. Rootful
// services create the socket root-owned; exec runs as root inside the service
// against the bind-mounted file. Safe: root keeps the bound socket, and peer
// credentials come from the connecting process, not the inode owner. Runs once
// per socket (see the caller); chmod 0770 is stricter than the daemon's 0755
// and still lets the owning host user connect.
func (d *DockerRuntime) chownServiceSocket(ctx context.Context, containerID, containerPath string) error {
	uidGid := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	for _, cmd := range [][]string{
		{"chown", uidGid, containerPath},
		{"chmod", "0770", containerPath},
	} {
		if err := d.runServiceExec(ctx, containerID, cmd); err != nil {
			return fmt.Errorf("exec %v: %w", cmd, err)
		}
	}
	return nil
}

// runServiceExec runs a command in a running container and waits for it to
// finish, surfacing a non-zero exit (e.g. a chown binary missing from an exotic
// service image) instead of silently proceeding. ContainerExecAttach can't be
// used for the wait: it doesn't report the exit code and needs a hijacked
// stream.
func (d *DockerRuntime) runServiceExec(ctx context.Context, containerID string, cmd []string) error {
	execResp, err := d.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{Cmd: cmd})
	if err != nil {
		return err
	}
	if err := d.cli.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{}); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		inspect, err := d.cli.ContainerExecInspect(ctx, execResp.ID)
		if err != nil {
			return err
		}
		if !inspect.Running {
			if inspect.ExitCode != 0 {
				return fmt.Errorf("exit code %d", inspect.ExitCode)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("did not finish within 10s")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
```

Update the `createService` probe loop (`docker_services.go:313-319`):

```go
	for socketName, exposePath := range svc.Exposes {
		if err := d.waitForServiceSocket(ctx, containerID, serviceSocketPath(name, mode, exposePath), exposePath, mode == workspace.ModeRootful); err != nil {
			cleanup()
			return fmt.Errorf("service %s did not expose socket %s within %s: %w", name, socketName, serviceProbeTimeout, err)
		}
		bindings[name+"/"+socketName] = serviceSocketPath(name, mode, exposePath)
	}
```

(The `%w` replaces the old unconditional wrap so a chown failure isn't misreported as a timeout. `container` is already imported.)

- [ ] **Step 4: Extend `fakeServicesDaemon` with exec endpoints**

In `internal/runtime/docker_services_test.go`:

Add fields to the struct (`fakeServicesDaemon`, near line 49):

```go
	execCreates  int
	execCmds     [][]string
	execExitCode int
```

In `ServeHTTP`:

1. Narrow the container-start arm (line 73) so `/exec/<id>/start` no longer matches it:

```go
	case r.Method == http.MethodPost && strings.HasPrefix(p, "containers/") && strings.HasSuffix(p, "/start"):
```

2. Add three arms before the generic `/json` arm (the one at line 101). Placement in the `switch` is significant: the exec-inspect arm must precede the `strings.HasSuffix(p, "/json")` arm, and the exec-create/start arms must not be shadowed by the narrowed container-start arm:

```go
	case r.Method == http.MethodPost && strings.HasPrefix(p, "containers/") && strings.HasSuffix(p, "/exec"):
		var execReq struct{ Cmd []string }
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &execReq)
		f.execCreates++
		f.execCmds = append(f.execCmds, execReq.Cmd)
		fmt.Fprintf(w, `{"Id":"exec%d"}`, f.execCreates)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "exec/") && strings.HasSuffix(p, "/start"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "exec/") && strings.HasSuffix(p, "/json"):
		fmt.Fprintf(w, `{"Running":false,"ExitCode":%d}`, f.execExitCode)
```

`io` and `json` are already imported in this test file.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/runtime/ -run 'TestStartServicesRootfulChownsSocket|TestStartServicesRootfulChownFailure' -v`
Expected: PASS — the chown runs once (two execs), and a non-zero `execExitCode` surfaces as `... exec [chown ...]: exit code 127`.

- [ ] **Step 6: Run the full runtime suite**

Run: `go test ./internal/runtime/`
Expected: PASS. `TestStartServicesRemovesContainerOnProbeTimeout` and the other rootless tests still pass — the rootless branch is unchanged and no test calls `waitForServiceSocket` directly.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/docker_services.go internal/runtime/docker_services_test.go
git commit -m "feat(services): chown rootful service sockets at probe time"
```

---

## Task 4: Docs — rootful services supported

**Files:**
- Modify: `docs/superpowers/specs/2026-08-04-services-design.md`
- Modify: `AGENTS.md`

- [ ] **Step 1: Update the design doc**

In `docs/superpowers/specs/2026-08-04-services-design.md`:

1. In "Socket path allocation" (lines 308-311), change the rootful run-dir bullet to:

```markdown
- Rootless: `/run/user/<uid>/tpd-svc-<name>/`
- Rootful: `/tmp/tpd-svc-<name>-<uid>/` (user-writable — the host tpd is not
  root; `/tmp` is transient, and the `<uid>` suffix keeps it distinct from a
  concurrent rootless run sharing `/run/user/<uid>`)
```

2. Replace the "Socket ownership in rootful mode" paragraph (lines 328-333) with:

```markdown
**Socket ownership in rootful mode:** a rootful service creates its socket
`root:root`, which the host tpd (a non-root user) cannot `connect()` to. On
first start, before the probe, tpd execs `chown <hostUID>:<hostGID>` +
`chmod 0770` inside the service container (running as root against the
bind-mounted file) the first time the host socket appears, so the existing
`connect()` probe works as in rootless. The chown is best-effort and
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
```

3. In the earlier "Multi-user rootful is not supported" note (lines 136-139) keep the caveat but drop the implication that rootful services are unreachable; the "Services are rootless-only" statement in the old plan doc (`docs/superpowers/plans/2026-08-04-services.md:34`) is superseded by this plan and left as history.

- [ ] **Step 2: Update AGENTS.md**

In `AGENTS.md` "Runtime notes" (line 37), append:

```markdown
`services:` work in both modes; rootful service sockets live in
`/tmp/tpd-svc-<name>-<uid>/` and are chowned to the host user at probe time.
```

- [ ] **Step 3: Verify and commit**

Run: `go test ./...` and `go vet ./...`
Expected: PASS (docs-only change).

```bash
git add docs/superpowers/specs/2026-08-04-services-design.md AGENTS.md
git commit -m "docs: document rootful service support"
```
