package prune

import "testing"

func TestIsTpodVolume(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"tpod-mise", true},
		{"tpod-cache-npm", true},
		{"tpod-cache-cargo", true},
		{"my-volume", false},
		{"docker-volumes", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isTpodVolume(tt.name); got != tt.want {
			t.Errorf("isTpodVolume(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
