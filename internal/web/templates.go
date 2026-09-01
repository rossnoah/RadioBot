package web

import (
	"bytes"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
)

//go:embed templates/*.html
var templateFS embed.FS

// templates are parsed once at startup; a broken template is a build-time
// mistake, so parse failure is fatal.
var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// render executes a template into a buffer first, so a mid-render failure does
// not leave a half-written response with a 200 status.
func render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("template rendering failed", "template", name, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		slog.Debug("could not write response", "template", name, "error", err)
	}
}
