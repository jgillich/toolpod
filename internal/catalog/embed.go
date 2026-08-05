package catalog

import "embed"

//go:embed profiles/*.yaml
var Profiles embed.FS

//go:embed fragments
var Fragments embed.FS
