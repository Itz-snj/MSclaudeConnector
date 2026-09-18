package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:web
var webFS embed.FS

// Handler serves the embedded placeholder React app shell.
func Handler() (http.Handler, error) {
	fsys, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	return http.FileServer(http.FS(fsys)), nil
}
