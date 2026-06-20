package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var embedFS embed.FS

func GetFileSystem() http.FileSystem {
	subFS, err := fs.Sub(embedFS, "dist")
	if err != nil {
		panic(err)
	}
	return http.FS(subFS)
}
