package runtime

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
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
