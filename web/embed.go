// Package web holds the static UI served by the server.
package web

import "embed"

//go:embed index.html vendor
var Files embed.FS
