package httpx

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
)

// MaxBodyBytes caps request bodies. Every payload in this API is a handful of short
// fields; without a cap a single client can stream gigabytes into the decoder.
const MaxBodyBytes = 32 * 1024

// DecodeBody reads a JSON body into T, turning parse failures into validation errors
// rather than 500s.
func DecodeBody[T any](r *http.Request) (T, error) {
	var dst T

	r.Body = http.MaxBytesReader(nil, r.Body, MaxBodyBytes)

	if err := DecodeJSON(r, &dst); err != nil {
		if errors.Is(err, io.EOF) {
			return dst, apperr.Validation("Envie os dados da requisição.", nil)
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return dst, apperr.Validation("Requisição muito grande.", nil)
		}
		// Unknown fields and type mismatches land here. The cause names the offending
		// field, which is what a client developer needs and reveals nothing sensitive.
		return dst, apperr.Wrap(err, apperr.CodeValidation,
			"Não foi possível ler os dados enviados.")
	}
	return dst, nil
}

// PathUUID reads a uuid from the URL.
//
// A malformed id is reported as not found rather than as a validation error: to the caller
// there is no difference between an id that cannot exist and one that does not, and saying
// so uniformly keeps the two from being told apart.
func PathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, apperr.NotFound("Recurso não encontrado.")
	}
	return id, nil
}

// QueryInt32 reads a bounded integer query parameter, falling back to fallback when it is
// absent or unparseable.
//
// Out-of-range values are clamped rather than rejected. A client asking for 10 000 rows is
// not attacking anything, and failing the request teaches them nothing a capped page does
// not.
func QueryInt32(r *http.Request, name string, fallback, minimum, maximum int32) int32 {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}

	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return fallback
	}

	return min(max(int32(parsed), minimum), maximum)
}
