package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets/*
var assets embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(assets, "assets")
	return http.FileServer(http.FS(root))
}
