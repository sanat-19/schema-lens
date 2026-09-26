// Package web holds the browser UI. The files are built into the schemalens
// binary, so the tool works offline and needs nothing installed.
//
// This package exists only because go:embed can't reach files in a parent
// directory: internal/server couldn't embed ../../web itself.
package web

import "embed"

//go:embed index.html
var FS embed.FS
