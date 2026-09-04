package main

import (
	"embed"
	"errors"
	"io/fs"
	"os"
)

// The compiled dashboard is embedded into the binary, which is what lets a
// single file be copied to a server and run with no other assets.
//
// The directory is committed with a placeholder so this always compiles, even
// before the frontend has been built. embeddedFrontend reports whether a real
// bundle is present.
//
//go:embed all:frontend
var frontendAssets embed.FS

// embeddedFrontend returns the compiled dashboard, or an error when the binary
// was built without one.
func embeddedFrontend() (fs.FS, error) {
	sub, err := fs.Sub(frontendAssets, "frontend")
	if err != nil {
		return nil, err
	}
	// index.html is the marker for a real build; the placeholder alone is not
	// a usable dashboard.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, errors.New("no dashboard bundle is embedded in this binary")
	}
	return sub, nil
}

// osDirFS serves the dashboard from a directory on disk.
func osDirFS(dir string) (fs.FS, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("not a directory")
	}
	return os.DirFS(dir), nil
}
