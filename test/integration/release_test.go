package integration

import (
	"net/http"
	"testing"
)

// GET /v1/app-version is how an installed app learns there is a newer APK. It reads back
// configuration, so what is worth pinning is the contract around it: no token needed, both
// keys always present, and null — not absent, not "" — while nothing is announced.
func TestAppVersion(t *testing.T) {
	t.Parallel()

	t.Run("nothing announced", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)

		body := e.anonymous().get("/v1/app-version").expect(http.StatusOK).json()

		for _, key := range []string{"latest_version", "apk_url"} {
			value, present := body[key]
			if !present {
				t.Errorf("%s is missing; it must be present and null", key)
			}
			if value != nil {
				t.Errorf("%s = %v, want null", key, value)
			}
		}
	})

	t.Run("a release announced", func(t *testing.T) {
		t.Parallel()
		const apkURL = "https://github.com/simonscabello/meu-auto-app/releases/latest/download/meu-auto.apk"
		e := newEnv(t, withAppRelease("1.2.0+7", apkURL))

		body := e.anonymous().get("/v1/app-version").expect(http.StatusOK).json()

		if body["latest_version"] != "1.2.0+7" {
			t.Errorf("latest_version = %v, want 1.2.0+7", body["latest_version"])
		}
		if body["apk_url"] != apkURL {
			t.Errorf("apk_url = %v, want %s", body["apk_url"], apkURL)
		}
	})
}
