package web

import "embed"

//go:embed *.html *.css *.js *.png *.json
var Files embed.FS
