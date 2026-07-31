package catalog

import "embed"

//go:embed profiles/*.yaml
var Profiles embed.FS

//go:embed presets/*.yaml
var Presets embed.FS
