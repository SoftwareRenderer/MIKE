package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
)

// The SPA compiled into this binary. `all:` is needed: the default pattern skips the dotfile that
// keeps the directory in git, and a pattern matching nothing is a compile error.
//
//go:embed all:frontend/dist
var embeddedDist embed.FS

// frontendAssets returns the SPA this process serves and the source to name in the log.
func frontendAssets(devDir string) (assets fs.FS, source string, err error) {
	embedded, err := fs.Sub(embeddedDist, "frontend/dist")
	if err != nil {
		return nil, "", fmt.Errorf("frontend assets: %w", err)
	}
	return chooseAssets(devDir, embedded)
}

// chooseAssets prefers the dev directory, so `npm run build` reaches a running server without a Go
// rebuild, and falls back to the compiled-in copy when that directory holds no document.
func chooseAssets(devDir string, embedded fs.FS) (fs.FS, string, error) {
	if devDir != "" {
		if dir := os.DirFS(devDir); hasDocument(dir) {
			return dir, devDir, nil
		}
		if hasDocument(embedded) {
			return embedded, fmt.Sprintf("compiled in (%s holds no index.html — `make frontend` to serve that directory instead)", devDir), nil
		}
		return nil, "", fmt.Errorf("no frontend to serve: neither %s nor this binary holds an index.html; run `make frontend` before building", devDir)
	}
	if !hasDocument(embedded) {
		return nil, "", fmt.Errorf("no frontend to serve: this binary was compiled without one; run `make frontend` before building")
	}
	return embedded, "compiled in", nil
}

func hasDocument(assets fs.FS) bool {
	info, err := fs.Stat(assets, "index.html")
	return err == nil && !info.IsDir()
}
