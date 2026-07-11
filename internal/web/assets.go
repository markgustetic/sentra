package web

import (
	"embed"
	"io/fs"
)

//go:embed assets
var embedded embed.FS

// Assets is the frontend rooted at the assets directory (index.html, app.css,
// app.js, images at its top level), served entirely from the binary — no CDN,
// no external fetch, matching the self-contained rule the preview follows.
var Assets fs.FS = mustSub(embedded, "assets")

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic("web: embed sub " + dir + ": " + err.Error())
	}
	return sub
}
