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
