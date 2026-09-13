package web

import "embed"

// StaticFiles embeds the static/ directory so the binary is self-contained.
//
//go:embed static
var StaticFiles embed.FS
