package templates

import (
	"embed"
	"html/template"
)

//go:embed *.html
var files embed.FS

// MustParse parses a layout and content template from the embedded files.
func MustParse(names ...string) *template.Template {
	return template.Must(template.ParseFS(files, names...))
}
