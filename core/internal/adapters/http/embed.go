package http

import "embed"

//go:embed all:static
var StaticFiles embed.FS
