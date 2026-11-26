package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginsPageServed(t *testing.T) {
	h := NewHandler(&Store{}, staticTokens{}, &fakeStreamer{})
	srv := httptest.NewServer(h)
	defer srv.Close()

	for path, ct := range map[string]string{
		"/logins":     "text/html",
		"/logins.js":  "text/javascript",
		"/logins.css": "text/css",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d without auth", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, ct) {
			t.Errorf("%s: Content-Type %q", path, got)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
			t.Errorf("%s: CSP %q", path, csp)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control %q", path, resp.Header.Get("Cache-Control"))
		}
		text := string(body)
		for _, banned := range []string{"localStorage", "sessionStorage", "document.cookie", "innerHTML"} {
			if strings.Contains(text, banned) {
				t.Errorf("%s uses %s", path, banned)
			}
		}
	}
}

func TestLoginsPageHasNoInlineScriptOrData(t *testing.T) {
	data, err := webFiles.ReadFile("web/logins.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	if strings.Count(html, "<script") != 1 || !strings.Contains(html, `<script src="/logins.js" defer></script>`) {
		t.Errorf("page must load only /logins.js and have no inline script")
	}
	if strings.Contains(html, " on") && strings.Contains(html, "onclick") {
		t.Errorf("page has inline event handlers")
	}
	for _, s := range []string{"rotatedAt\":", "\"value\":", "rotatedBy\":"} {
		if strings.Contains(html, s) {
			t.Errorf("page contains data %q", s)
		}
	}
}
