package mise

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"sort"
	"strings"
)

//go:embed plugins
var pluginsFS embed.FS

const appimageBackendPrefix = "appimage:"

// pluginInstallVersion hashes the embedded plugin tree. The versioned install
// directory name and the skip-work marker both key off it, so any plugin
// change forces a fresh install while unchanged content is never rewritten.
func pluginInstallVersion() string {
	var files []string
	_ = fs.WalkDir(pluginsFS, "plugins", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		data, err := fs.ReadFile(pluginsFS, f)
		if err != nil {
			continue
		}
		h.Write([]byte(f))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// PluginInstallCommand returns a shell command that installs the embedded mise
// plugins into the shared mise data dir, so backend tools such as appimage:...
// resolve during a subsequent `mise install`. Files are base64-encoded to
// survive the shell round-trip unchanged.
//
// Files land in a content-versioned sibling directory and the pointer
// `plugins/appimage` is a symlink repointed only after every file (the marker
// last) has been written, so mise — which follows the symlink — never sees a
// partially written plugin dir. A run whose marker and pointer already match
// the embedded content does nothing.
func PluginInstallCommand() string {
	version := pluginInstallVersion()
	plugins := `"$HOME"/.local/share/mise/plugins`
	pointer := plugins + "/appimage"
	vdir := plugins + "/appimage-" + version
	marker := vdir + "/.tpd-plugin"

	var b strings.Builder
	b.WriteString(`if [ ! -f ` + marker + ` ] || [ "$(cat ` + marker + ` 2>/dev/null)" != '` + version + `' ] || [ "$(readlink ` + pointer + ` 2>/dev/null)" != 'appimage-` + version + `' ]; then `)
	b.WriteString(`rm -rf ` + vdir + ` && mkdir -p ` + vdir)
	_ = fs.WalkDir(pluginsFS, "plugins", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(pluginsFS, p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "plugins/appimage/")
		dir := vdir
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			dir = vdir + "/" + rel[:i]
		}
		b.WriteString(` && mkdir -p ` + dir)
		b.WriteString(` && printf '%s' '`)
		b.WriteString(base64.StdEncoding.EncodeToString(data))
		b.WriteString(`' | base64 -d > ` + vdir + "/" + rel)
		return nil
	})
	b.WriteString(` && printf '%s' '` + version + `' > ` + marker)
	b.WriteString(` && if [ ! -L ` + pointer + ` ]; then rm -rf ` + pointer + `; fi`)
	// ln -sf would unlink then recreate the pointer, leaving a window where a
	// concurrent container sees no plugin; rename(2) via mv -Tf replaces the
	// pointer atomically. rm -f first clears a stale .tmp left by a SIGKILL
	// between ln -s and mv, which would otherwise fail every later install
	// with EEXIST until manually cleaned.
	b.WriteString(` && rm -f ` + pointer + `.tmp`)
	b.WriteString(` && ln -s 'appimage-` + version + `' ` + pointer + `.tmp`)
	b.WriteString(` && mv -Tf ` + pointer + `.tmp ` + pointer)
	b.WriteString(`; fi`)
	return b.String()
}
