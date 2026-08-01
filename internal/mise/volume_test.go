package mise

import "testing"

func TestMiseVolume(t *testing.T) {
	v := MiseVolume("/root")
	if v.Name != "tpod-mise" {
		t.Errorf("name = %q, want tpod-mise", v.Name)
	}
	if v.Target != "/mise" {
		t.Errorf("target = %q, want /mise", v.Target)
	}
}

func TestCacheVolume(t *testing.T) {
	v := CacheVolume("npm", "/root/.npm")
	if v.Name != "tpod-cache-npm" {
		t.Errorf("name = %q, want tpod-cache-npm", v.Name)
	}
	if v.Target != "/root/.npm" {
		t.Errorf("target = %q, want /root/.npm", v.Target)
	}
}
