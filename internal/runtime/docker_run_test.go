package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/jgillich/tpd/internal/workspace"
)

func TestSuspendSequenceIndex(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"plain", -1},
		{"a\x1ab", 1},
		{"a\x1b[122;5ub", 1},
		{"\x1b[122;5u\x1a", 0},
	}
	for _, tt := range tests {
		if got := suspendSequenceIndex([]byte(tt.input)); got != tt.want {
			t.Errorf("suspendSequenceIndex(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestTarFiles(t *testing.T) {
	files := []FileSpec{
		{Target: "/root/.config/foo", Content: "hello\n", Mode: 0o600},
		{Target: "/etc/tpd.conf", Content: "x", Mode: 0o644},
	}
	data, err := tarFiles(files, 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	var entries []*tar.Header
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, hdr)
		if len(entries) == 1 {
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != "hello\n" {
				t.Errorf("entry content = %q, want %q (Size/content mismatch)", body, "hello\n")
			}
		}
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 tar entries, got %d", len(entries))
	}
	if entries[0].Name != "root/.config/foo" {
		t.Errorf("entry name = %q, want root/.config/foo (relative, no leading slash)", entries[0].Name)
	}
	if entries[0].Uid != 1000 || entries[0].Gid != 1000 {
		t.Errorf("entry uid/gid = %d/%d, want 1000/1000", entries[0].Uid, entries[0].Gid)
	}
	if entries[0].Mode != 0o600 {
		t.Errorf("entry mode = %o, want 600", entries[0].Mode)
	}
	if entries[0].Typeflag != tar.TypeReg {
		t.Errorf("entry typeflag = %d, want TypeReg", entries[0].Typeflag)
	}
}

func TestTarFilesPAX(t *testing.T) {
	// Go's tar writer emits PAX only when a header cannot fit USTAR (paths
	// beyond 255 bytes); a long target forces it, exercising the PAX request.
	long := "/root/" + strings.Repeat("d", 300)
	data, err := tarFiles([]FileSpec{{Target: long, Content: "x", Mode: 0o600}}, 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimPrefix(long, "/")
	if hdr.Name != want {
		t.Errorf("entry name = %q, want %q", hdr.Name, want)
	}
	if hdr.Format != tar.FormatPAX {
		t.Errorf("entry format = %d, want PAX", hdr.Format)
	}
}

func TestTarFilesEmpty(t *testing.T) {
	data, err := tarFiles(nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1024 { // tar terminator is two zero blocks (1024 bytes)
		t.Errorf("empty tar should be the 1024-byte terminator, got %d bytes", len(data))
	}
}

// fakeRunDaemon serves the API endpoints the suspend and create paths use:
// /version (subpath probe), ContainerTop, ContainerCreate (with an optional
// 409), and the exec lifecycle. Exec start hijacks the connection and streams
// the configured ps output framed as stdcopy, matching a real daemon.
type fakeRunDaemon struct {
	topTitles    []string
	topProcesses [][]string

	createCount   int
	createNames   []string
	conflictCount int

	execIDs  []string
	execCmds [][]string
	psStdout string
	psExit   int
	killExit int
}

func newFakeRunDaemon() *fakeRunDaemon {
	return &fakeRunDaemon{psStdout: "    42\n"}
}

func (f *fakeRunDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/v1.41/")
	switch {
	case p == "version" && r.Method == http.MethodGet:
		fmt.Fprint(w, `{"Version":"28.0.0"}`)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/top"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Titles":    f.topTitles,
			"Processes": f.topProcesses,
		})
	case p == "containers/create" && r.Method == http.MethodPost:
		io.Copy(io.Discard, r.Body)
		f.createCount++
		name := r.URL.Query().Get("name")
		f.createNames = append(f.createNames, name)
		if f.conflictCount > 0 {
			f.conflictCount--
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `{"message":"Conflict. The container name %q is already in use"}`, name)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"Id":"ctr%d"}`, f.createCount)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "containers/") && strings.HasSuffix(p, "/exec"):
		var execReq struct{ Cmd []string }
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &execReq)
		f.execIDs = append(f.execIDs, fmt.Sprintf("exec%d", len(f.execIDs)+1))
		f.execCmds = append(f.execCmds, execReq.Cmd)
		fmt.Fprintf(w, `{"Id":"exec%d"}`, len(f.execIDs))
	case r.Method == http.MethodPost && strings.HasPrefix(p, "exec/") && strings.HasSuffix(p, "/start"):
		f.serveExecStart(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "exec/") && strings.HasSuffix(p, "/json"):
		f.serveExecInspect(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeRunDaemon) execIndex(execID string) int {
	for i, id := range f.execIDs {
		if id == execID {
			return i
		}
	}
	return -1
}

func (f *fakeRunDaemon) serveExecStart(w http.ResponseWriter, r *http.Request) {
	execID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1.41/exec/"), "/start")
	idx := f.execIndex(execID)
	if idx < 0 {
		http.NotFound(w, r)
		return
	}
	// Draining the request body before hijacking prevents an RST: closing a
	// connection with unread data makes the kernel send RST instead of FIN.
	io.Copy(io.Discard, r.Body)
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(bufrw, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	bufrw.Flush()
	if f.execCmds[idx][0] == "ps" && f.psStdout != "" {
		stdw := stdcopy.NewStdWriter(bufrw, stdcopy.Stdout)
		_, _ = stdw.Write([]byte(f.psStdout))
		bufrw.Flush()
	}
}

func (f *fakeRunDaemon) serveExecInspect(w http.ResponseWriter, r *http.Request) {
	execID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1.41/exec/"), "/json")
	idx := f.execIndex(execID)
	if idx < 0 {
		http.NotFound(w, r)
		return
	}
	exit := f.killExit
	if f.execCmds[idx][0] == "ps" {
		exit = f.psExit
	}
	fmt.Fprintf(w, `{"Running":false,"ExitCode":%d}`, exit)
}

func newRunTestRuntime(t *testing.T, daemon *fakeRunDaemon) *DockerRuntime {
	t.Helper()
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+srv.Listener.Addr().String()),
		client.WithVersion("1.41"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &DockerRuntime{cli: cli}
}

func TestContainerTopColumn(t *testing.T) {
	titles := []string{"UID", "PID", "STAT", "CMD"}
	if got := containerTopColumn(titles, "PID"); got != 1 {
		t.Errorf("PID = %d, want 1", got)
	}
	if got := containerTopColumn(titles, "stat"); got != 2 {
		t.Errorf("stat = %d, want 2", got)
	}
	if got := containerTopColumn(titles, "PPID"); got != -1 {
		t.Errorf("PPID = %d, want -1", got)
	}
}

func TestWaitForContainerProcessStopped(t *testing.T) {
	tests := []struct {
		name        string
		noPIDCol    bool
		processes   [][]string
		wantStopped bool
	}{
		{"leading T", false, [][]string{{"1", "Ss"}, {"2", "T"}}, true},
		{"leading t", false, [][]string{{"1", "Ss"}, {"2", "t"}}, true},
		{"DT is not stopped", false, [][]string{{"1", "Ss"}, {"2", "DT"}}, false},
		{"substring T is not stopped", false, [][]string{{"1", "Ss"}, {"2", "ST"}}, false},
		{"pid 1 stopped is ignored", false, [][]string{{"1", "T"}}, false},
		{"no PID column skips the pid 1 guard", true, [][]string{{"T"}}, true},
		{"running", false, [][]string{{"1", "Ss"}, {"2", "S"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon := newFakeRunDaemon()
			if tt.noPIDCol {
				daemon.topTitles = []string{"STAT"}
			} else {
				daemon.topTitles = []string{"PID", "STAT"}
			}
			daemon.topProcesses = tt.processes
			rt := newRunTestRuntime(t, daemon)
			if got := rt.waitForContainerProcessStopped(context.Background(), "ctr", 50*time.Millisecond); got != tt.wantStopped {
				t.Errorf("waitForContainerProcessStopped = %v, want %v", got, tt.wantStopped)
			}
		})
	}
}

func TestSignalContainerForegroundGroupUsesRealPGID(t *testing.T) {
	daemon := newFakeRunDaemon()
	daemon.topTitles = []string{"PID", "STAT", "CMD"}
	daemon.topProcesses = [][]string{
		{"1", "Ss", "/sbin/tini"},
		{"2", "S+", "opencode"},
		{"3", "S+", "opencode-server"},
	}
	rt := newRunTestRuntime(t, daemon)

	if err := rt.signalContainerForegroundGroup(context.Background(), "ctr", syscall.SIGTSTP); err != nil {
		t.Fatalf("signalContainerForegroundGroup: %v", err)
	}
	if len(daemon.execCmds) != 2 {
		t.Fatalf("execs = %v, want a ps query followed by a group kill", daemon.execCmds)
	}
	if got := daemon.execCmds[0]; len(got) != 5 || got[0] != "ps" || got[1] != "-o" || got[2] != "pgid=" || got[3] != "-p" || got[4] != "2" {
		t.Errorf("ps query = %v, want [ps -o pgid= -p 2] (the lowest non-init PID)", got)
	}
	if got := daemon.execCmds[1]; len(got) != 4 || got[0] != "kill" || got[1] != "-"+strconv.Itoa(int(syscall.SIGTSTP)) || got[2] != "--" || got[3] != "-42" {
		t.Errorf("kill = %v, want [kill -%d -- -42] (the queried PGID)", got, syscall.SIGTSTP)
	}
}

func TestSignalContainerForegroundGroupFallsBackWhenPSFails(t *testing.T) {
	daemon := newFakeRunDaemon()
	daemon.topTitles = []string{"PID", "STAT"}
	daemon.topProcesses = [][]string{{"1", "Ss"}, {"2", "S+"}}
	daemon.psExit = 127 // ps not installed in the container
	rt := newRunTestRuntime(t, daemon)

	if err := rt.signalContainerForegroundGroup(context.Background(), "ctr", syscall.SIGCONT); err != nil {
		t.Fatalf("signalContainerForegroundGroup: %v", err)
	}
	kill := daemon.execCmds[len(daemon.execCmds)-1]
	if len(kill) != 4 || kill[0] != "kill" || kill[1] != "-"+strconv.Itoa(int(syscall.SIGCONT)) || kill[3] != "-2" {
		t.Errorf("fallback kill = %v, want [kill -%d -- -2]", kill, syscall.SIGCONT)
	}
}

func TestSignalContainerForegroundGroupFallsBackWhenPGIDIsOne(t *testing.T) {
	// With tini/--init the app commonly inherits tini's process group (PGID
	// 1); kill -- -1 is a whole-container broadcast, so the legacy -2 group
	// must be signalled instead.
	daemon := newFakeRunDaemon()
	daemon.topTitles = []string{"PID", "STAT"}
	daemon.topProcesses = [][]string{{"1", "Ss"}, {"2", "S+"}}
	daemon.psStdout = "    1\n"
	rt := newRunTestRuntime(t, daemon)

	if err := rt.signalContainerForegroundGroup(context.Background(), "ctr", syscall.SIGTSTP); err != nil {
		t.Fatalf("signalContainerForegroundGroup: %v", err)
	}
	kill := daemon.execCmds[len(daemon.execCmds)-1]
	if len(kill) != 4 || kill[0] != "kill" || kill[1] != "-"+strconv.Itoa(int(syscall.SIGTSTP)) || kill[3] != "-2" {
		t.Errorf("kill for pgid 1 = %v, want fallback [kill -%d -- -2] (never -1)", kill, syscall.SIGTSTP)
	}
	for _, cmd := range daemon.execCmds {
		for _, arg := range cmd {
			if arg == "-1" {
				t.Errorf("kill -- -1 (whole-container broadcast) must never be emitted: %v", daemon.execCmds)
			}
		}
	}
}

func TestSignalContainerForegroundGroupReportsKillFailure(t *testing.T) {
	daemon := newFakeRunDaemon()
	daemon.topTitles = []string{"PID", "STAT"}
	daemon.topProcesses = [][]string{{"1", "Ss"}, {"2", "S+"}}
	daemon.killExit = 1
	rt := newRunTestRuntime(t, daemon)

	err := rt.signalContainerForegroundGroup(context.Background(), "ctr", syscall.SIGTSTP)
	if err == nil || !strings.Contains(err.Error(), "exit code 1") {
		t.Fatalf("kill failure must surface, got %v", err)
	}
}

func TestRunContainerExecCapturesStdout(t *testing.T) {
	daemon := newFakeRunDaemon()
	daemon.psStdout = "    7\n"
	rt := newRunTestRuntime(t, daemon)

	out, err := rt.runContainerExec(context.Background(), "ctr", []string{"ps", "-o", "pgid=", "-p", "2"})
	if err != nil {
		t.Fatalf("runContainerExec: %v", err)
	}
	if strings.TrimSpace(out) != "7" {
		t.Errorf("stdout = %q, want 7", out)
	}
}

func TestCreateContainerRetriesNameCollision(t *testing.T) {
	daemon := newFakeRunDaemon()
	daemon.conflictCount = 1
	rt := newRunTestRuntime(t, daemon)
	spec := Spec{
		ProfileName: "test",
		Image:       "debian:13-slim",
		Command:     []string{"sh", "-c", "echo hi"},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		RuntimeHome: "/root",
		Network:     "none",
	}
	res, err := rt.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if res.ContainerID != "ctr2" {
		t.Errorf("ContainerID = %q, want ctr2 (the second attempt)", res.ContainerID)
	}
	if daemon.createCount != 2 {
		t.Errorf("create count = %d, want 2 (one conflict, one success)", daemon.createCount)
	}
	if daemon.createNames[0] == daemon.createNames[1] {
		t.Errorf("retry reused the same container name %q", daemon.createNames[0])
	}
	for _, n := range daemon.createNames {
		if !strings.HasPrefix(n, "tpd-test-") {
			t.Errorf("name %q does not use the profile prefix", n)
		}
	}
}

func TestBuildDeviceCgroupRulesSkipsNonCharBlock(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{DeviceSpecs: []DeviceSpec{
		{Container: "/dev/null", Host: "/dev/null", Perms: "rwm", Cgroup: true},
		{Container: "/dev/notadevice", Host: dir, Cgroup: true},
	}}
	rules := buildDeviceCgroupRules(spec)
	if len(rules) != 1 {
		t.Fatalf("rules = %v, want only /dev/null (non-char/block skipped, no broad rule)", rules)
	}
	if strings.Contains(rules[0], "*") {
		t.Errorf("broad rule must never be emitted, got %q", rules[0])
	}
}

func TestBuildDeviceCgroupRulesPreservesPerms(t *testing.T) {
	spec := Spec{DeviceSpecs: []DeviceSpec{
		{Container: "/dev/null", Host: "/dev/null", Perms: "rw", Cgroup: true},
		{Container: "/dev/zero", Host: "/dev/zero", Cgroup: true},
	}}
	rules := buildDeviceCgroupRules(spec)
	if len(rules) != 2 {
		t.Fatalf("rules = %v, want two rules for /dev/null and /dev/zero", rules)
	}
	if !strings.HasSuffix(rules[0], " rw") {
		t.Errorf("rule[0] = %q, want the requested rw preserved (not hardcoded rwm)", rules[0])
	}
	if !strings.HasSuffix(rules[1], " rwm") {
		t.Errorf("rule[1] = %q, want the default rwm when perms are empty", rules[1])
	}
	if strings.Contains(rules[0], "*") || strings.Contains(rules[1], "*") {
		t.Errorf("broad rules must never be emitted, got %v", rules)
	}
}
