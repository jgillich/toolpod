package prune

import "testing"

func TestIsToolpodVolume(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"toolpod-mise", true},
		{"toolpod-cache-npm", true},
		{"toolpod-cache-cargo", true},
		{"my-volume", false},
		{"docker-volumes", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isToolpodVolume(tt.name); got != tt.want {
			t.Errorf("isToolpodVolume(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestIsToolpodImage(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"toolpod/opencode:latest", true},
		{"toolpod/shell:latest", true},
		{"toolpod/myprof:latest", true},
		{"alpine:latest", false},
		{"ghcr.io/jdx/mise:latest", false},
	}
	for _, tt := range tests {
		if got := isToolpodImage(tt.ref); got != tt.want {
			t.Errorf("isToolpodImage(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}
