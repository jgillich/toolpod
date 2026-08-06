package scaffold

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBuildFragmentNav(t *testing.T) {
	names := []string{"cloud/aws", "cloud/azure", "gui/display", "lang/go", "lang/javascript", "top"}
	descs := map[string]string{"lang/go": "Go toolchain with GOPATH cache", "top": "top-level"}
	nav := buildFragmentNav(names, descs)
	if !reflect.DeepEqual(nav.folders, []string{"cloud", "gui", "lang"}) {
		t.Errorf("folders = %v, want [cloud gui lang]", nav.folders)
	}
	cloud := nav.byFolder["cloud"]
	if len(cloud) != 2 || cloud[0].display != "cloud/aws" || cloud[1].display != "cloud/azure" {
		t.Errorf("cloud items = %v", cloud)
	}
	if got := nav.byFolder["lang"][0]; got.label != "go — Go toolchain with GOPATH cache" || got.kind != itemFragment {
		t.Errorf("lang/go row = %+v", got)
	}
	if len(nav.topFrags) != 1 || nav.topFrags[0].display != "top" || nav.topFrags[0].label != "top — top-level" {
		t.Errorf("top frags = %+v", nav.topFrags)
	}
}

func TestBuildFragmentNavFlattensDeep(t *testing.T) {
	nav := buildFragmentNav([]string{"lang/js/node", "lang/js/tsc"}, nil)
	if !reflect.DeepEqual(nav.folders, []string{"lang"}) {
		t.Errorf("folders = %v, want [lang]", nav.folders)
	}
	lang := nav.byFolder["lang"]
	if len(lang) != 2 || lang[0].display != "lang/js/node" || lang[0].label != "js/node" {
		t.Errorf("deep fragments flatten to their remainder: %+v", lang)
	}
}

func TestBuildFragmentNavEmpty(t *testing.T) {
	nav := buildFragmentNav(nil, nil)
	if len(nav.folders) != 0 || len(nav.topFrags) != 0 || len(nav.byFolder) != 0 {
		t.Errorf("empty nav = %+v", nav)
	}
}

func TestFragmentNavItemLabels(t *testing.T) {
	descs := map[string]string{"lang/go": "Go toolchain"}
	if got := fragmentRow("lang/go", "go", descs).label; got != "go — Go toolchain" {
		t.Errorf("described label = %q", got)
	}
	if got := fragmentRow("vcs/git", "git", descs).label; got != "git" {
		t.Errorf("undescribed label = %q", got)
	}
}

func TestFolderNavEnterExpandsFolder(t *testing.T) {
	nav := buildFragmentNav([]string{"lang/go", "lang/javascript", "vcs/git"}, nil)
	m := newFolderNavModel("Folder", nav)
	if len(m.list.VisibleItems()) != 2 {
		t.Fatalf("initial rows = %d, want 2", len(m.list.VisibleItems()))
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(folderNavModel)
	if !m.expanded["lang"] {
		t.Error("enter should expand the highlighted folder")
	}
	if len(m.list.VisibleItems()) != 4 {
		t.Errorf("expanded rows = %d, want 4", len(m.list.VisibleItems()))
	}
	if cmd != nil {
		t.Error("expanding must not quit the program")
	}
	if it := m.list.SelectedItem().(folderNavItem); it.display != "lang" {
		t.Errorf("cursor moved off the folder to %q", it.display)
	}
}

func TestFolderNavEnterCollapsesFolder(t *testing.T) {
	nav := buildFragmentNav([]string{"lang/go", "lang/javascript"}, nil)
	m := newFolderNavModel("Folder", nav)
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(folderNavModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(folderNavModel)
	if m.expanded["lang"] {
		t.Error("second enter should collapse the folder")
	}
	if len(m.list.VisibleItems()) != 1 {
		t.Errorf("collapsed rows = %d, want 1", len(m.list.VisibleItems()))
	}
}

func TestFolderNavSpaceTogglesFragment(t *testing.T) {
	nav := buildFragmentNav([]string{"lang/go", "lang/javascript"}, nil)
	m := newFolderNavModel("Folder", nav)
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(folderNavModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = u.(folderNavModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = u.(folderNavModel)
	if !m.picked["lang/go"] {
		t.Error("space should pick the highlighted fragment")
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = u.(folderNavModel)
	if m.picked["lang/go"] {
		t.Error("space should unpick a picked fragment")
	}
}

func TestFolderNavSpaceExpandsFolder(t *testing.T) {
	nav := buildFragmentNav([]string{"lang/go"}, nil)
	m := newFolderNavModel("Folder", nav)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(folderNavModel)
	if !m.expanded["lang"] {
		t.Error("space should expand the highlighted folder")
	}
	if len(m.list.VisibleItems()) != 2 {
		t.Errorf("expanded rows = %d, want 2", len(m.list.VisibleItems()))
	}
	if cmd != nil {
		t.Error("expanding must not quit the program")
	}
}

func TestFolderNavEnterTogglesFragment(t *testing.T) {
	nav := buildFragmentNav([]string{"lang/go"}, nil)
	m := newFolderNavModel("Folder", nav)
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(folderNavModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = u.(folderNavModel)
	u, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(folderNavModel)
	if !m.picked["lang/go"] {
		t.Error("enter should pick the highlighted fragment")
	}
	if cmd != nil {
		t.Error("toggling must not quit the program")
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(folderNavModel)
	if m.picked["lang/go"] {
		t.Error("enter should unpick a picked fragment")
	}
}

func TestFolderNavTabFocusesDone(t *testing.T) {
	nav := buildFragmentNav([]string{"lang/go"}, nil)
	m := newFolderNavModel("Folder", nav)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(folderNavModel)
	if !m.focused {
		t.Fatal("tab should focus the Done section")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if res := updated.(folderNavModel).result; res != browserDone {
		t.Errorf("enter on focused Done = %q, want %s", res, browserDone)
	}
}

func TestFolderNavTabTogglesBackToList(t *testing.T) {
	nav := buildFragmentNav([]string{"lang/go"}, nil)
	m := newFolderNavModel("Folder", nav)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(folderNavModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if updated.(folderNavModel).focused {
		t.Error("second tab should return focus to the list")
	}
}

func TestFolderNavEscCancels(t *testing.T) {
	nav := buildFragmentNav([]string{"lang/go"}, nil)
	m := newFolderNavModel("Folder", nav)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if res := updated.(folderNavModel).result; res != browserCancel {
		t.Errorf("esc = %q, want %s", res, browserCancel)
	}
}

func TestFolderNavCtrlCCancels(t *testing.T) {
	nav := buildFragmentNav([]string{"lang/go"}, nil)
	m := newFolderNavModel("Folder", nav)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if res := updated.(folderNavModel).result; res != browserCancel {
		t.Errorf("ctrl+c = %q, want %s", res, browserCancel)
	}
}

func TestFolderNavDelegateMarkers(t *testing.T) {
	nav := buildFragmentNav([]string{"lang/go", "lang/javascript", "vcs/git"}, nil)
	m := newFolderNavModel("Folder", nav)
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // expand lang
	m = u.(folderNavModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown}) // highlight lang/go
	m = u.(folderNavModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace}) // pick lang/go
	m = u.(folderNavModel)
	v := m.View()
	for _, want := range []string{"▾ lang", "✓ go", "• javascript"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in view:\n%s", want, v)
		}
	}
	if strings.Contains(v, "▸ lang") {
		t.Errorf("expanded folder must show ▾, got:\n%s", v)
	}
}
