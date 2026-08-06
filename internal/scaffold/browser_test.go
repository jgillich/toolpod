package scaffold

import (
	"reflect"
	"testing"
)

func TestFragTreeRoot(t *testing.T) {
	names := []string{"cloud/aws", "cloud/azure", "gui/display", "lang/go", "lang/javascript", "top"}
	dirs, frags := fragTree(names, nil)
	if !reflect.DeepEqual(dirs, []string{"cloud", "gui", "lang"}) {
		t.Errorf("dirs = %v, want [cloud gui lang]", dirs)
	}
	if !reflect.DeepEqual(frags, []string{"top"}) {
		t.Errorf("frags = %v, want [top]", frags)
	}
}

func TestFragTreeNested(t *testing.T) {
	names := []string{"lang/go", "lang/javascript", "lang/js/node", "lang/js/tsc"}
	dirs, frags := fragTree(names, []string{"lang"})
	if !reflect.DeepEqual(dirs, []string{"js"}) {
		t.Errorf("dirs = %v, want [js]", dirs)
	}
	if !reflect.DeepEqual(frags, []string{"go", "javascript"}) {
		t.Errorf("frags = %v, want [go javascript]", frags)
	}
	dirs, frags = fragTree(names, []string{"lang", "js"})
	if len(dirs) != 0 {
		t.Errorf("dirs = %v, want empty", dirs)
	}
	if !reflect.DeepEqual(frags, []string{"node", "tsc"}) {
		t.Errorf("frags = %v, want [node tsc]", frags)
	}
}

func TestFragTreeEmpty(t *testing.T) {
	dirs, frags := fragTree(nil, nil)
	if len(dirs) != 0 || len(frags) != 0 {
		t.Errorf("dirs=%v frags=%v, want both empty", dirs, frags)
	}
	dirs, frags = fragTree([]string{"lang/go"}, []string{"vcs"})
	if len(dirs) != 0 || len(frags) != 0 {
		t.Errorf("dirs=%v frags=%v, want both empty for a path with no matches", dirs, frags)
	}
}

func TestFragDisplayName(t *testing.T) {
	if got := fragDisplayName(nil, "aws"); got != "aws" {
		t.Errorf("root leaf = %q, want aws", got)
	}
	if got := fragDisplayName([]string{"cloud"}, "aws"); got != "cloud/aws" {
		t.Errorf("nested leaf = %q, want cloud/aws", got)
	}
}
