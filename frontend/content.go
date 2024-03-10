package frontend

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed dist/*
var content embed.FS

func Content() (fs.FS, error) {
	f, err := fs.Sub(content, "dist")
	if err != nil {
		return nil, fmt.Errorf("unable to select embeded resources: %w", err)
	}
	return f, err
}
