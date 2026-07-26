package localactivity

import (
	"embed"
	"fmt"
	"html/template"
)

//go:embed assets/* templates/*
var embeddedFiles embed.FS

var (
	embeddedPageStyles = template.CSS(mustReadEmbeddedFile("assets/local-activity.css"))
	embeddedBrandLogo  = template.HTML(mustReadEmbeddedFile("assets/gesta-lockup-horizontal.svg"))
	pageTemplates      = template.Must(
		template.New("local-activity").
			Funcs(template.FuncMap{
				"brandLogo":  func() template.HTML { return embeddedBrandLogo },
				"pageStyles": func() template.CSS { return embeddedPageStyles },
			}).
			ParseFS(embeddedFiles, "templates/*.html"),
	)
)

func mustReadEmbeddedFile(path string) string {
	content, err := embeddedFiles.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("read embedded local activity asset %q: %v", path, err))
	}
	return string(content)
}
