// Package apperr defines the typed domain errors that services return and the HTTP layer
// translates (SPEC.md D-07).
//
// Services never touch http.ResponseWriter and never pick a status code. They return an
// *Error with a Code; a single middleware maps Code to status and renders the response.
//
// # Codes are contract
//
// The Flutter app switches on Code, and a shipped app cannot be force-updated (SPEC.md
// D-01). Never rename a code, never repurpose one, and never remove one. Adding a code is
// safe only because the app treats unknown codes as a generic failure.
//
// Message is pt-BR and may be displayed to the user. Code is English and machine-readable.
package apperr

import (
	"errors"
	"fmt"
)

// Code is the stable, machine-readable error identifier sent to the client.
type Code string

const (
	CodeValidation       Code = "validation_failed"
	CodeUnauthorized     Code = "unauthorized"
	CodeForbidden        Code = "forbidden"
	CodeNotFound         Code = "not_found"
	CodeMethodNotAllowed Code = "method_not_allowed"
	CodeConflict         Code = "conflict"
	CodeOdometerRollback Code = "odometer_rollback"
	CodeRateLimited      Code = "rate_limited"

	// CodeUpstreamUnavailable is a third party we depend on failing to answer — today,
	// only the vehicle catalogue's FIPE provider.
	//
	// Provider-neutral by name, deliberately. A code called "fipe_unavailable" would be a
	// supplier's name in a contract the app switches on, and changing supplier would then
	// mean either a lie or a breaking change. The app's correct reaction is the same
	// either way: this is not your fault, nothing is broken, try again shortly.
	//
	// It is NOT CodeRateLimited even when the cause is a quota. That code means the caller
	// is going too fast; the quota that ran out is ours and shared, and blaming the person
	// who happened to tap next would be a lie the app repeats to them.
	CodeUpstreamUnavailable Code = "upstream_unavailable"

	CodeInternal Code = "internal"
)

// Error is a domain error carrying a stable code, a pt-BR message safe to show the user,
// and optional structured details.
type Error struct {
	Code    Code
	Message string
	Details map[string]any

	cause error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the underlying cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.cause }

// WithDetails attaches structured details rendered under "error.details".
//
// Details reach the client, so they must never carry anything the caller is not already
// allowed to see.
func (e *Error) WithDetails(details map[string]any) *Error {
	e.Details = details
	return e
}

// New builds an error with the given code and pt-BR message.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Wrap builds an error that preserves cause for logging while presenting message to the
// client.
func Wrap(cause error, code Code, message string) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

// From converts any error into an *Error, defaulting to CodeInternal.
//
// This is the single place where an unexpected error becomes a client-visible response,
// which is why the fallback message is deliberately generic: an internal error's real
// text goes to the log, never to the client.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}
	return Wrap(err, CodeInternal, "Ocorreu um erro inesperado. Tente novamente.")
}

// Validation reports invalid input. Fields maps each rejected field to its reason.
func Validation(message string, fields map[string]any) *Error {
	e := New(CodeValidation, message)
	if len(fields) > 0 {
		e.Details = map[string]any{"fields": fields}
	}
	return e
}

// NotFound reports a missing resource.
//
// Per SPEC.md RN-07, a resource the caller is not allowed to see is also reported as not
// found — responding "forbidden" would confirm that it exists.
func NotFound(message string) *Error { return New(CodeNotFound, message) }

// Unauthorized reports missing or invalid credentials.
func Unauthorized(message string) *Error { return New(CodeUnauthorized, message) }

// Conflict reports a violated uniqueness or state constraint.
func Conflict(message string) *Error { return New(CodeConflict, message) }

// Internal reports an unexpected failure. The cause is logged, never sent to the client.
func Internal(cause error) *Error {
	return Wrap(cause, CodeInternal, "Ocorreu um erro inesperado. Tente novamente.")
}
