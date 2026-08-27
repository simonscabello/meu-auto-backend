package httpx

import (
	"log/slog"
	"net/http"

	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/logging"
)

// statusByCode maps every domain error code to its HTTP status.
//
// The app switches on the code in the body, not on the status, so a code and its status
// are allowed to share a number. What is not allowed is changing a code (SPEC.md D-01).
var statusByCode = map[apperr.Code]int{
	apperr.CodeValidation:       http.StatusUnprocessableEntity,
	apperr.CodeUnauthorized:     http.StatusUnauthorized,
	apperr.CodeForbidden:        http.StatusForbidden,
	apperr.CodeNotFound:         http.StatusNotFound,
	apperr.CodeMethodNotAllowed: http.StatusMethodNotAllowed,
	apperr.CodeConflict:         http.StatusConflict,
	apperr.CodeOdometerRollback: http.StatusUnprocessableEntity,
	apperr.CodeRateLimited:      http.StatusTooManyRequests,

	// 503, not 500. Nothing here is broken and the request is worth repeating, which is
	// exactly what the status is for — and it keeps a supplier's bad afternoon out of the
	// 5xx rate that says our own code is failing.
	apperr.CodeUpstreamUnavailable: http.StatusServiceUnavailable,

	apperr.CodeInternal: http.StatusInternalServerError,
}

// errorBody is the wire format for every error response. It is contract — see SPEC.md
// section 7.
type errorBody struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code    apperr.Code    `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// StatusFor returns the HTTP status for a domain error code, defaulting to 500 for a code
// nobody mapped.
func StatusFor(code apperr.Code) int {
	if status, ok := statusByCode[code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// Error renders err as the standard error response and logs it.
//
// This is the only place in the codebase that turns an error into a response. Handlers
// return early through it; they never write an error body themselves.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	appErr := apperr.From(err)
	if appErr == nil {
		return
	}
	status := StatusFor(appErr.Code)
	log := logging.FromContext(r.Context())

	if status >= http.StatusInternalServerError {
		// The real message names internals — a table, a driver, a constraint. It goes to
		// the log, and the client gets the request id so support can find this exact line.
		log.Error("request failed",
			slog.String("code", string(appErr.Code)),
			slogErr(appErr))

		details := map[string]any{}
		if id := RequestID(r.Context()); id != "" {
			details["request_id"] = id
		}
		JSON(w, r, status, errorBody{errorPayload{
			Code:    appErr.Code,
			Message: appErr.Message,
			Details: details,
		}})
		return
	}

	// Expected outcomes: a 404 or a rejected field is not an operational problem.
	log.Debug("request rejected",
		slog.String("code", string(appErr.Code)),
		slog.Int("status", status))

	JSON(w, r, status, errorBody{errorPayload{
		Code:    appErr.Code,
		Message: appErr.Message,
		Details: appErr.Details,
	}})
}

func slogErr(err error) slog.Attr {
	return slog.String("error", err.Error())
}
