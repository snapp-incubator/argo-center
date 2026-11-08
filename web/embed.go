// Package web embeds the built frontend (web/dist) into the Go binary.
package web

import "embed"

// Dist holds the Vite build output. Run `make web` before `go build` to populate it;
// without it the binary still runs but serves no UI.
//
//go:embed all:dist
var Dist embed.FS
