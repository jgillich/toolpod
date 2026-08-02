package mise

import (
	"embed"
	"encoding/base64"
	"io/fs"
	"strings"
)

//go:embed plugins
var pluginsFS embed.FS

const appimageBackendPrefix = "appimage:"

// PluginInstallCommand returns a shell command that writes the embedded mise
// plugins into the shared mise data dir, so backend tools such as
// appimage:... resolve during a subsequent `mise install`. Files are
// base64-encoded to survive the shell round-trip unchanged.
func PluginInstallCommand() string {
	var b strings.Builder
	// A leftover file or dangling symlink at the plugin dir would make
	// mkdir -p fail with "File exists"; clear it first (rm -rf on a symlink
	// removes the link, not its target).
	b.WriteString("rm -rf $HOME/.local/share/mise/plugins/appimage && ")
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
		rel := strings.TrimPrefix(p, "plugins/")
		target := "$HOME/.local/share/mise/plugins/" + rel
		dir := target[:strings.LastIndex(target, "/")]
		b.WriteString("mkdir -p " + dir)
		b.WriteString(" && printf '%s' '")
		b.WriteString(base64.StdEncoding.EncodeToString(data))
		b.WriteString("' | base64 -d > " + target)
		b.WriteString(" && ")
		return nil
	})
	return strings.TrimSuffix(b.String(), " && ")
}
