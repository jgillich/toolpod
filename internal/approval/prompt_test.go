package approval

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jgillich/tpd/internal/profile"
)

func TestDefaultPromptNonTTYReturnsError(t *testing.T) {
	req := PromptRequest{ProfileName: "test", Items: []GatedItem{
		{Field: "mounts", Key: "~/.ssh", Value: "~/.ssh", Source: testContrib()},
	}}
	_, err := DefaultPrompt(req, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("DefaultPrompt on non-TTY should error")
	}
	if !strings.Contains(err.Error(), "not a TTY") {
		t.Errorf("error should mention TTY, got %v", err)
	}
}

func testContrib() (c profile.Contributor) {
	return profile.Contributor{FullName: "core/creds/ssh", Namespace: "core"}
}

func TestFieldSectionsGroupByFieldType(t *testing.T) {
	req := PromptRequest{ProfileName: "test", Items: []GatedItem{
		{Field: "mounts", Key: "~/.ssh", Value: "~/.ssh", Source: testContrib()},
		{Field: "env", Key: "A", Value: "A=1", Source: testContrib()},
		{Field: "mounts", Key: "~/x", Value: "~/x", Source: testContrib()},
		{Field: "ports", Key: "8080", Value: "8080 → 1.2.3.4:8080", Source: testContrib()},
	}}
	sections := fieldSections(req)
	if len(sections) != 3 {
		t.Fatalf("expected one section per field type, got %d", len(sections))
	}
	if sections[0].field != "mounts" || sections[1].field != "env" || sections[2].field != "ports" {
		t.Errorf("mounts should sort first, then remaining fields by name, got %+v", sections)
	}
	if got := len(sections[0].items); got != 2 {
		t.Errorf("mounts items = %d, want 2", got)
	}
	if sections[0].items[0].Key != "~/.ssh" || sections[0].items[1].Key != "~/x" {
		t.Errorf("items should be sorted by key, got %+v", sections[0].items)
	}
}

func TestFieldTitle(t *testing.T) {
	cases := map[string]string{
		"env":       "Environment",
		"dbus.talk": "D-Bus Talk",
		"dbus.own":  "D-Bus Own",
		"mounts":    "Mounts",
		"network":   "Network",
	}
	for in, want := range cases {
		if got := fieldTitle(in); got != want {
			t.Errorf("fieldTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// pump runs one Update and then executes the returned cmd(s) back into the
// model, mirroring how the tea runtime dispatches commands (BatchMsg carries
// the individual sub-commands).
func pump(t *testing.T, m approvalModel, msg tea.Msg) approvalModel {
	t.Helper()
	mm, cmd := m.Update(msg)
	m = mm.(approvalModel)
	if cmd == nil {
		return m
	}
	switch res := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range res {
			if c == nil {
				continue
			}
			if r := c(); r != nil {
				m2, _ := m.Update(r)
				m = m2.(approvalModel)
			}
		}
	default:
		if res != nil {
			m2, _ := m.Update(res)
			m = m2.(approvalModel)
		}
	}
	return m
}

// approvalTestReq is a realistic item set: one prior-approved mount, a new
// mount, a secret-named env var, plus benign items (display env, loopback
// port, dbus talk).
func approvalTestReq() PromptRequest {
	return PromptRequest{ProfileName: "bash", Items: []GatedItem{
		{Field: "mounts", Key: "~/.ssh", Value: "~/.ssh (rw)", Source: testContrib(), PriorApproved: true, Detail: "read/write"},
		{Field: "mounts", Key: "~/.config/glab-cli", Value: "~/.config/glab-cli (rw)", Source: testContrib(), Detail: "read/write"},
		{Field: "env", Key: "AWS_ACCESS_KEY_ID", Value: "AWS_ACCESS_KEY_ID=AKIA...", Source: testContrib(), Detail: "host value"},
		{Field: "env", Key: "DISPLAY", Value: "DISPLAY=:0", Source: testContrib(), Detail: "host value", Benign: true},
		{Field: "ports", Key: "8080", Value: "8080 → 127.0.0.1:8080", Source: testContrib(), Detail: "127.0.0.1:8080 → container 8080", Benign: true},
		{Field: "dbus.talk", Key: "org.freedesktop.portal.*", Value: "org.freedesktop.portal.*", Source: testContrib(), Detail: "talk", Benign: true},
	}}
}

func TestApprovalModelInitialCheckState(t *testing.T) {
	m := newApprovalModel(approvalTestReq())
	if len(m.rows) != 6 {
		t.Fatalf("rows = %d, want 6", len(m.rows))
	}
	for _, r := range m.rows {
		want := r.item.Key == "~/.ssh" || r.item.Benign
		if r.checked != want {
			t.Errorf("row %q checked = %v, want %v (prior-approved and benign start checked)", r.item.Key, r.checked, want)
		}
	}
}

func TestApprovalModelBenignRowsSortToBottom(t *testing.T) {
	m := newApprovalModel(approvalTestReq())
	// The three benign items must come after the three non-benign ones,
	// preserving the original field/key order within each group.
	keys := make([]string, len(m.rows))
	for i, r := range m.rows {
		keys[i] = r.item.Key
	}
	want := []string{"~/.config/glab-cli", "~/.ssh", "AWS_ACCESS_KEY_ID", "org.freedesktop.portal.*", "DISPLAY", "8080"}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (order %v)", i, keys[i], want[i], keys)
		}
	}
}

func TestApprovalModelServicesSortFirst(t *testing.T) {
	req := PromptRequest{ProfileName: "bash", Items: []GatedItem{
		{Field: "env", Key: "AWS_ACCESS_KEY_ID", Value: "v", Source: testContrib(), Detail: "host value"},
		{Field: "services", Key: "podman", Value: "podman: privileged", Source: testContrib(), Detail: "privileged", Warning: true},
		{Field: "env", Key: "DISPLAY", Value: "DISPLAY=:0", Source: testContrib(), Detail: "host value", Benign: true},
		{Field: "mounts", Key: "~/.ssh", Value: "~/.ssh", Source: testContrib(), Detail: "read-only"},
	}}
	m := newApprovalModel(req)
	keys := make([]string, len(m.rows))
	for i, r := range m.rows {
		keys[i] = r.item.Key
	}
	want := []string{"podman", "~/.ssh", "AWS_ACCESS_KEY_ID", "DISPLAY"}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (order %v)", i, keys[i], want[i], keys)
		}
	}
}

func TestApprovalModelSpaceTogglesSelected(t *testing.T) {
	m := newApprovalModel(approvalTestReq())
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = u.(approvalModel)
	idx := m.list.Index()
	if !m.rows[idx].checked {
		t.Errorf("space should check the highlighted row %q", m.rows[idx].item.Key)
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = u.(approvalModel)
	if m.rows[m.list.Index()].checked {
		t.Error("second space should uncheck the highlighted row")
	}
}

func TestApprovalModelXKeyTogglesSelected(t *testing.T) {
	m := newApprovalModel(approvalTestReq())
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = u.(approvalModel)
	if !m.rows[m.list.Index()].checked {
		t.Error("x should toggle the highlighted row")
	}
}

func TestApprovalModelApproveAllDenyAll(t *testing.T) {
	m := newApprovalModel(approvalTestReq())
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = u.(approvalModel)
	for _, r := range m.rows {
		if !r.checked {
			t.Errorf("a should check every row, %q unchecked", r.item.Key)
		}
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = u.(approvalModel)
	for _, r := range m.rows {
		if r.checked {
			t.Errorf("n should uncheck every row, %q still checked", r.item.Key)
		}
	}
}

func TestApprovalModelADeniesWhenAllChecked(t *testing.T) {
	m := newApprovalModel(approvalTestReq())
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = u.(approvalModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = u.(approvalModel)
	for _, r := range m.rows {
		if r.checked {
			t.Errorf("a when everything is checked should uncheck every row, %q still checked", r.item.Key)
		}
	}
}

func TestApprovalModelNApprovesWhenNoneChecked(t *testing.T) {
	m := newApprovalModel(approvalTestReq())
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = u.(approvalModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = u.(approvalModel)
	for _, r := range m.rows {
		if !r.checked {
			t.Errorf("n when nothing is checked should check every row, %q unchecked", r.item.Key)
		}
	}
}

func TestApprovalModelEnterBuildsCompleteChoices(t *testing.T) {
	m := newApprovalModel(approvalTestReq())
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(approvalModel)
	if !m.done {
		t.Fatal("enter should finish the prompt")
	}
	for _, it := range approvalTestReq().Items {
		if _, ok := m.result[it.Field][it.Key]; !ok {
			t.Errorf("enter result missing choice for %s.%s", it.Field, it.Key)
		}
	}
	if !m.result["mounts"]["~/.ssh"] {
		t.Error("prior-approved item should stay checked in the result")
	}
	if m.result["mounts"]["~/.config/glab-cli"] {
		t.Error("new item should be unchecked in the result")
	}
}

func TestApprovalModelEscAndCtrlCCancel(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}} {
		m := newApprovalModel(approvalTestReq())
		u, _ := m.Update(key)
		m = u.(approvalModel)
		if !m.cancelled || !m.done {
			t.Errorf("%s should cancel", key.String())
		}
	}
}

func TestApprovalModelViewRendersRows(t *testing.T) {
	req := PromptRequest{ProfileName: "bash", Items: []GatedItem{
		{Field: "mounts", Key: "~/.ssh", Value: "~/.ssh (rw)", Source: testContrib(), PriorApproved: true, Detail: "read/write"},
		{Field: "services", Key: "podman", Value: "podman: privileged=true; exposes={podman=/run/podman/podman.sock}; env={A=1}", Source: testContrib(), Detail: "privileged; 1 socket(s); 1 env var(s)"},
	}}
	m := newApprovalModel(req)
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = u.(approvalModel)
	v := stripANSI(m.View())
	for _, want := range []string{
		"Review permissions for bash",
		"[x]", // prior-approved mount checked
		"[ ]", // new service unchecked
		"mounts",
		"services",
		"read/write",
		"privileged; 1 socket(s); 1 env var(s)",
		"approve all",
		"deny all",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q\n%s", want, v)
		}
	}
	// Selecting the service row must not pop a pane in.
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = u.(approvalModel)
	if strings.Contains(stripANSI(m.View()), "privileged=true") {
		t.Error("navigating must not show details automatically")
	}
}

func TestApprovalModelMountRowShowsSourcePath(t *testing.T) {
	req := PromptRequest{ProfileName: "bash", Items: []GatedItem{
		{Field: "mounts", Key: "/etc/mise", Value: "~/.config/mise", Source: testContrib(), Detail: "read-only"},
		{Field: "mounts", Key: "~/.config/amp", Value: "~/.config/amp (rw)", Source: testContrib(), Detail: "read/write"},
	}}
	m := newApprovalModel(req)
	m = pump(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	v := stripANSI(m.View())
	// The permission granted is exposing the host source path, so the row
	// must show it, not the container target.
	for _, want := range []string{"~/.config/mise", "~/.config/amp (rw)", "read-only", "read/write"} {
		if !strings.Contains(v, want) {
			t.Errorf("mount row missing %q\n%s", want, v)
		}
	}
}

func TestApprovalModelDetailsOpensAndCloses(t *testing.T) {
	req := PromptRequest{ProfileName: "bash", Items: []GatedItem{
		{Field: "mounts", Key: "~/.ssh", Value: "~/.ssh (rw)", Source: testContrib(), Detail: "read/write"},
		{Field: "services", Key: "podman", Value: "podman: privileged=true", Source: testContrib(), Detail: "privileged", Body: "privileged: true\nexposes:\n  podman → /run/podman/podman.sock"},
	}}
	m := newApprovalModel(req)
	m = pump(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	// Select the service, then open details.
	m = pump(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = pump(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if !m.inspecting {
		t.Fatal("d should open the details view")
	}
	v := stripANSI(m.View())
	for _, want := range []string{
		"services", "podman",
		"core/creds/ssh",   // contributor
		"privileged: true", // multi-line body
		"podman → /run/podman/podman.sock",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("details view missing %q\n%s", want, v)
		}
	}
	// esc returns to the list.
	m = pump(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.inspecting {
		t.Error("esc should close the details view")
	}
	if !strings.Contains(stripANSI(m.View()), "[ ]") {
		t.Error("list should render again after closing details")
	}
}

func TestApprovalModelDetailsShowsValueForPlainItems(t *testing.T) {
	req := PromptRequest{ProfileName: "bash", Items: []GatedItem{
		{Field: "env", Key: "AWS_ACCESS_KEY_ID", Value: "AWS_ACCESS_KEY_ID=AKIA...", Source: testContrib(), Detail: "host value"},
	}}
	m := newApprovalModel(req)
	m = pump(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = pump(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	v := stripANSI(m.View())
	if !strings.Contains(v, "AWS_ACCESS_KEY_ID=AKIA...") {
		t.Errorf("details should show the env value\n%s", v)
	}
}

func TestApprovalModelDetailsCtrlCCancels(t *testing.T) {
	req := PromptRequest{ProfileName: "bash", Items: []GatedItem{
		{Field: "mounts", Key: "~/.ssh", Value: "~/.ssh", Source: testContrib(), Detail: "read-only"},
	}}
	m := newApprovalModel(req)
	m = pump(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = pump(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = pump(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.cancelled || !m.done {
		t.Error("ctrl+c from the details view should cancel the prompt")
	}
}

func TestApprovalModelLongKeyShortDetailNotTruncated(t *testing.T) {
	req := PromptRequest{ProfileName: "x", Items: []GatedItem{
		{Field: "dbus.own", Key: "org.freedesktop.secrets", Value: "org.freedesktop.secrets", Source: testContrib(), Detail: "own"},
	}}
	m := newApprovalModel(req)
	m = pump(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	v := stripANSI(m.View())
	if !strings.Contains(v, "org.freedesktop.secrets") {
		t.Errorf("long key with a short detail must not be truncated at the natural box width\n%s", v)
	}
	if !strings.Contains(v, "own") {
		t.Errorf("detail should still be visible\n%s", v)
	}
}

func TestApprovalModelScrollBar(t *testing.T) {
	var items []GatedItem
	for i := 0; i < 30; i++ {
		items = append(items, GatedItem{Field: "env", Key: fmt.Sprintf("K%02d", i), Value: "v", Source: testContrib(), Detail: "host value"})
	}
	thumbRow := func(v string) int {
		for i, line := range strings.Split(v, "\n") {
			if strings.Contains(line, "█") {
				return i
			}
		}
		return -1
	}
	m := newApprovalModel(PromptRequest{ProfileName: "many", Items: items})
	m = pump(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	top := thumbRow(stripANSI(m.View()))
	if top < 0 {
		t.Fatal("long list at the top should show a scrollbar thumb")
	}
	for i := 0; i < 60; i++ {
		m = pump(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	bot := thumbRow(stripANSI(m.View()))
	if bot <= top {
		t.Fatalf("thumb should move down when scrolling: top=%d bottom=%d", top, bot)
	}

	// A short list that fits has no thumb.
	m = newApprovalModel(PromptRequest{ProfileName: "few", Items: items[:3]})
	m = pump(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	if thumbRow(stripANSI(m.View())) >= 0 {
		t.Error("a short list that fits should have no scrollbar thumb")
	}
}
