// Package release tells the Android app which build it can update to.
//
// The APK is published as a GitHub Release by the app repository's workflow, not through a
// store, so nothing outside the app can tell an owner that a new version exists. This is
// that signal, and it is configuration read back and nothing more: the version and the
// download come from APP_LATEST_VERSION and APP_APK_URL, set by a person once the release
// is up. Publishing a file and asking every phone to install it are separate decisions.
//
// There is no service and no repository, on purpose: nothing here is decided and nothing is
// stored. If a rule ever appears — a minimum version below which the app must stop, say —
// it is a product decision first, and it gets a service then.
package release

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/simonscabello/meu-auto-backend/internal/platform/httpx"
)

// Handler answers GET /v1/app-version from two values config.Load already validated.
type Handler struct {
	latestVersion string
	apkURL        string
}

// NewHandler takes the configured release. Empty means "not announced".
func NewHandler(latestVersion, apkURL string) *Handler {
	return &Handler{latestVersion: latestVersion, apkURL: apkURL}
}

// Mount registers the route under the caller's prefix (/v1).
//
// # Why it is public
//
// The answer is a version number and a public download link, so a token would protect
// nothing. And the app most needs to hear "there is a new version" exactly when something
// between it and the API has broken — a session that no longer refreshes, a response an old
// build can no longer read. Asking for a token here would silence the one message meant for
// that moment.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/app-version", h.appVersion)
}

func (h *Handler) appVersion(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, r, http.StatusOK, appVersionResponse{
		LatestVersion: optional(h.latestVersion),
		APKURL:        optional(h.apkURL),
	})
}
