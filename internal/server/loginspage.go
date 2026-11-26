package server

import (
	"embed"
	"net/http"
)

//go:embed web/logins.html web/logins.js web/logins.css
var webFiles embed.FS

// loginsCSP allows only same-origin scripts, styles and API calls. The page
// has no inline script and stores nothing.
const loginsCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'"

// mountLoginsPage serves the owner Logins page. The page holds no data; it
// calls /v1/logins with the token the owner types in.
func mountLoginsPage(mux *http.ServeMux) {
	serve := func(name, contentType string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			data, err := webFiles.ReadFile("web/" + name)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			h := w.Header()
			h.Set("Content-Type", contentType)
			h.Set("Content-Security-Policy", loginsCSP)
			h.Set("Cache-Control", "no-store")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			_, _ = w.Write(data)
		}
	}
	mux.HandleFunc("GET /logins", serve("logins.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /logins.js", serve("logins.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /logins.css", serve("logins.css", "text/css; charset=utf-8"))
}
