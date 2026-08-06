package approval

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jgillich/tpd/internal/profile"
)

func TestDefaultPromptNonTTYReturnsError(t *testing.T) {
	req := PromptRequest{ProfileName: "test", Items: []SensitiveItem{
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
	req := PromptRequest{ProfileName: "test", Items: []SensitiveItem{
		{Field: "mounts", Key: "~/.ssh", Value: "~/.ssh", Source: testContrib()},
		{Field: "env", Key: "A", Value: "A=1", Source: testContrib()},
		{Field: "mounts", Key: "~/x", Value: "~/x", Source: testContrib()},
		{Field: "ports", Key: "8080", Value: "8080 → 1.2.3.4:8080", Source: testContrib()},
	}}
	sections, preselected := fieldSections(req)
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
	if len(preselected) != 3 {
		t.Errorf("preselected buffers = %d, want one per section", len(preselected))
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

func TestFieldSectionsPreselectsAll(t *testing.T) {
	req := PromptRequest{ProfileName: "test", Items: []SensitiveItem{
		{Field: "mounts", Key: "~/.ssh", Value: "~/.ssh", Source: testContrib()},
		{Field: "env", Key: "A", Value: "A=1", Source: testContrib()},
	}}
	sections, preselected := fieldSections(req)
	if len(sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(sections))
	}
	if len(preselected) != 2 {
		t.Fatalf("preselected buffers = %d, want 2", len(preselected))
	}
	if len(preselected[0]) != 1 || preselected[0][0] != "mounts\x00~/.ssh" || len(preselected[1]) != 1 {
		t.Errorf("preselect = %v, want every item pre-selected", preselected)
	}
}
