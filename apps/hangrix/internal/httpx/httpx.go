// Package httpx holds the tiny JSON-handler primitives every chi route in
// the codebase needs: status+body writer, error wrapper, numeric path
// parameter parser. Every handler module wired these by hand before; the
// hoist removes ~40 LoC of identical boilerplate and centralises the
// content-type / status-code conventions so they're not silently drifting
// across modules.
package httpx

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// WriteJSON encodes v as JSON, sets Content-Type, and writes status. The
// encoder errors (object → io error) are swallowed: there is nothing
// useful a handler can do after partially writing the response, and the
// connection is the client's problem at that point.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError encodes {"error": msg} with the given status. Matches the
// shape the SPA already consumes.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// ParseID parses a positive int64 path parameter and writes a 400 on
// failure (returning ok=false so the caller can early-return). The "id"
// label in the error message is intentionally generic — every handler
// uses this for numeric primary keys.
func ParseID(w http.ResponseWriter, raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		WriteError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}
