package scaffold

import (
	"strings"
	"testing"
)

func TestMergeYAMLExtendsUnionPreservesComments(t *testing.T) {
	existing := `version: 1
# keep this comment
command: ["bash", "-l"]
extends:
  - core/mise
mounts:
  ~/.gitconfig:
    source: ~/.gitconfig
    optional: true
`
	generated := "version: 1\nextends:\n    - core/opencode\n    - core/lang/javascript\n"
	out, err := mergeYAML([]byte(existing), []byte(generated))
	if err != nil {
		t.Fatal(err)
	}
	// Existing extends keep their position; new ones are appended in order.
	mi := strings.Index(out, "- core/mise")
	oi := strings.Index(out, "- core/opencode")
	ji := strings.Index(out, "- core/lang/javascript")
	if mi < 0 || oi < 0 || ji < 0 || !(mi < oi && oi < ji) {
		t.Errorf("extends order wrong: core/mise before core/opencode before core/lang/javascript, got:\n%s", out)
	}
	// Comments, custom command, and unrelated mounts survive the round-trip.
	if !strings.Contains(out, "# keep this comment") {
		t.Errorf("comment wiped:\n%s", out)
	}
	if !strings.Contains(out, `command: ["bash", "-l"]`) {
		t.Errorf("custom command clobbered:\n%s", out)
	}
	if !strings.Contains(out, "~/.gitconfig") {
		t.Errorf("mounts wiped:\n%s", out)
	}
}

func TestMergeYAMLDedupsExistingBase(t *testing.T) {
	existing := "version: 1\nextends:\n  - core/opencode\n"
	generated := "version: 1\nextends:\n    - core/opencode\n    - core/lang/javascript\n"
	out, err := mergeYAML([]byte(existing), []byte(generated))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "- core/opencode"); n != 1 {
		t.Errorf("core/opencode appears %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "- core/lang/javascript") {
		t.Errorf("new extends missing:\n%s", out)
	}
}

func TestMergeYAMLAddsMissingKeys(t *testing.T) {
	existing := "version: 1\n"
	generated := "version: 1\ncommand:\n    - bash\n"
	out, err := mergeYAML([]byte(existing), []byte(generated))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "- bash") {
		t.Errorf("command fallback not added:\n%s", out)
	}
}

func TestMergeYAMLKeepsExistingCommand(t *testing.T) {
	existing := "version: 1\ncommand: [zsh]\n"
	generated := "version: 1\ncommand:\n    - bash\n"
	out, err := mergeYAML([]byte(existing), []byte(generated))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "- bash") {
		t.Errorf("existing command replaced by bash fallback:\n%s", out)
	}
	if !strings.Contains(out, "zsh") {
		t.Errorf("existing command lost:\n%s", out)
	}
}

func TestMergeYAMLEmptyExistingFallsBack(t *testing.T) {
	generated := "version: 1\nextends:\n    - core/mise\n"
	out, err := mergeYAML([]byte(""), []byte(generated))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "core/mise") {
		t.Errorf("empty existing should fall back to generated:\n%s", out)
	}
}

func TestMergeYAMLScalarExtendsNormalized(t *testing.T) {
	existing := "version: 1\nextends: core/mise\ncommand: [zsh]\n"
	generated := "version: 1\nextends:\n    - core/opencode\n    - core/lang/javascript\n"
	out, err := mergeYAML([]byte(existing), []byte(generated))
	if err != nil {
		t.Fatal(err)
	}
	// The scalar extends becomes a list so the generated bases are not dropped.
	for _, want := range []string{"- core/mise", "- core/opencode", "- core/lang/javascript"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s after scalar-extends merge, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "extends: core/mise") {
		t.Errorf("scalar extends should be rewritten as a list, got:\n%s", out)
	}
}

func TestMergeYAMLTabsRejected(t *testing.T) {
	generated := "version: 1\nextends:\n    - core/mise\n"
	existing := "version: 1\n\tcommand: [bash]\n"
	if _, err := mergeYAML([]byte(existing), []byte(generated)); err == nil {
		t.Fatal("expected error for tab-indented existing file")
	}
}
