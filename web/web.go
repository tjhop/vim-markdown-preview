// Package web embeds the browser-side assets into the Go binary.
package web

import "embed"

// Assets contains all browser-side files: HTML, JS, CSS, and fonts.
// These are served by the HTTP server at runtime.
//
//go:embed index.html vendor-manifest.json js css
var Assets embed.FS
