package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
)

func TestStatusForKnownCodes(t *testing.T) {
	t.Parallel()

	cases := map[apperr.Code]int{
		apperr.CodeValidation:       http.StatusUnprocessableEntity,
		apperr.CodeUnauthorized:     http.StatusUnauthorized,
		apperr.CodeForbidden:        http.StatusForbidden,
		apperr.CodeNotFound:         http.StatusNotFound,
		apperr.CodeMethodNotAllowed: http.StatusMethodNotAllowed,
		apperr.CodeConflict:         http.StatusConflict,
		apperr.CodeOdometerRollback: http.StatusUnprocessableEntity,
		apperr.CodeRateLimited:      http.StatusTooManyRequests,
		apperr.CodeInternal:         http.StatusInternalServerError,
	}

	for code, want := range cases {
		if got := StatusFor(code); got != want {
			t.Errorf("StatusFor(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestStatusForUnknownCodeIsInternal(t *testing.T) {
	t.Parallel()

	if got := StatusFor("something_nobody_mapped"); got != http.StatusInternalServerError {
		t.Errorf("StatusFor(unknown) = %d, want %d", got, http.StatusInternalServerError)
	}
}

// A plain error must never reach the client as its own text: an unexpected failure names
// tables, drivers and constraints.
func TestErrorHidesInternalDetailFromClient(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/vehicles", nil)
	req = req.WithContext(WithRequestID(req.Context(), "req-123"))

	Error(rec, req, errWithSecret{})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Error.Code != string(apperr.CodeInternal) {
		t.Errorf("code = %q, want %q", body.Error.Code, apperr.CodeInternal)
	}
	if body.Error.Message == secretDetail {
		t.Error("internal error detail leaked to the client")
	}
	if body.Error.Details["request_id"] != "req-123" {
		t.Errorf("request_id = %v, want req-123", body.Error.Details["request_id"])
	}
}

// A domain error keeps its own code, message and details.
func TestErrorRendersDomainError(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/vehicles/x/odometer", nil)

	appErr := apperr.New(apperr.CodeOdometerRollback,
		"A quilometragem informada é menor que a última registrada.").
		WithDetails(map[string]any{"current_mileage_km": 98200})

	Error(rec, req, appErr)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}

	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Error.Code != string(apperr.CodeOdometerRollback) {
		t.Errorf("code = %q, want %q", body.Error.Code, apperr.CodeOdometerRollback)
	}
	if body.Error.Details["current_mileage_km"] != float64(98200) {
		t.Errorf("details.current_mileage_km = %v, want 98200",
			body.Error.Details["current_mileage_km"])
	}
}

const secretDetail = `pq: relation "users" does not exist`

type errWithSecret struct{}

func (errWithSecret) Error() string { return secretDetail }
