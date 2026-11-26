package server

import "net/http"

// NewMCPHandler returns the MCP handler. The tools are added in a later
// change.
func NewMCPHandler(_ *Store, _ TokenStore, _ LogStreamer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, newAPIError(http.StatusNotImplemented, "path", "MCP is not available"))
	})
}
