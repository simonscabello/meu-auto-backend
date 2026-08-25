// Package httpx holds the generic HTTP building blocks: rendering, middleware and the
// translation from domain errors to responses.
//
// It knows nothing about any domain module. The dependency always points inwards —
// modules import httpx, never the other way around.
package httpx

import (
	"encoding/json"
	"net/http"

	"github.com/simonscabello/meu-auto-backend/internal/platform/logging"
)

// JSON writes v as a JSON response with the given status code.
//
// If encoding fails the header is already on the wire, so there is nothing useful left to
// tell the client — the failure is logged and the connection is left to close.
func JSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logging.FromContext(r.Context()).Error("failed to encode response body",
			slogErr(err))
	}
}

// NoContent writes a 204 with no body.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// DecodeJSON reads and strictly decodes the request body into dst.
//
// Unknown fields are rejected: silently ignoring them would let a client typo a field
// name and believe a value was saved when it was dropped.
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
