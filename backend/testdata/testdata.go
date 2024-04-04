package testdata

import (
	"embed"
	"io/fs"
)

//go:embed *
var content embed.FS

func Content() fs.FS {
	return content
}
