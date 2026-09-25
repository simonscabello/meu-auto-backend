package integration

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/simonscabello/meu-auto-backend/internal/identity"
)

// ---------- personal data ----------

// An app installed before the profile grew keeps sending {name} alone, and it must keep
// working exactly as it did.
func TestUpdateMeNameAloneStillWorks(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	body := u.patch("/v1/me", map[string]any{"name": "  Ana Maria  "}).
		expect(http.StatusOK).json()
	if body["name"] != "Ana Maria" {
		t.Fatalf("name = %v, want trimmed Ana Maria", body["name"])
	}
	for _, field := range []string{"birth_date", "phone", "cnh_category", "cnh_expires_on", "photo_url"} {
		value, present := body[field]
		if !present || value != nil {
			t.Errorf("%s = %v (present %v), want an explicit null", field, value, present)
		}
	}
}

func TestUpdateMePersonalData(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	body := u.patch("/v1/me", map[string]any{
		"birth_date":     "1990-04-12",
		"phone":          "(11) 91234-5678",
		"cnh_category":   "ab",
		"cnh_expires_on": "2031-06-30",
	}).expect(http.StatusOK).json()

	want := map[string]any{
		"birth_date":     "1990-04-12",
		"phone":          "11912345678",
		"cnh_category":   "AB",
		"cnh_expires_on": "2031-06-30",
	}
	for field, value := range want {
		if body[field] != value {
			t.Errorf("%s = %v, want %v", field, body[field], value)
		}
	}

	// A PATCH that names one field leaves the rest alone, and GET agrees.
	u.patch("/v1/me", map[string]any{"phone": "+55 21 3456-7890"}).expect(http.StatusOK)
	got := u.get("/v1/me").expect(http.StatusOK).json()
	if got["phone"] != "2134567890" || got["birth_date"] != "1990-04-12" {
		t.Fatalf("after a phone-only PATCH: phone = %v, birth_date = %v", got["phone"], got["birth_date"])
	}

	// clear empties a field; the others stay.
	cleared := u.patch("/v1/me", map[string]any{"clear": []string{"phone", "cnh_category"}}).
		expect(http.StatusOK).json()
	if cleared["phone"] != nil || cleared["cnh_category"] != nil {
		t.Fatalf("clear left phone = %v, cnh_category = %v", cleared["phone"], cleared["cnh_category"])
	}
	if cleared["cnh_expires_on"] != "2031-06-30" {
		t.Fatalf("clear touched cnh_expires_on: %v", cleared["cnh_expires_on"])
	}
}

func TestUpdateMeRejectsBadPersonalData(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	cases := []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"empty name", map[string]any{"name": "   "}, "name"},
		{"unparseable birth date", map[string]any{"birth_date": "12/04/1990"}, "birth_date"},
		{"birth date in the future", map[string]any{"birth_date": "2999-01-01"}, "birth_date"},
		{"younger than 16", map[string]any{"birth_date": e.today().AddDate(-10, 0, 0).Format("2006-01-02")}, "birth_date"},
		{"a typo of a century", map[string]any{"birth_date": "1826-04-12"}, "birth_date"},
		{"phone without DDD", map[string]any{"phone": "91234-5678"}, "phone"},
		{"phone with a leading zero", map[string]any{"phone": "011 91234-5678"}, "phone"},
		{"unknown licence category", map[string]any{"cnh_category": "F"}, "cnh_category"},
		{"unparseable expiry", map[string]any{"cnh_expires_on": "amanhã"}, "cnh_expires_on"},
		{"clear an unknown field", map[string]any{"clear": []string{"email"}}, "clear"},
		{"clear and set together", map[string]any{"phone": "11912345678", "clear": []string{"phone"}}, "phone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := u.patch("/v1/me", tc.body).
				expectError(http.StatusUnprocessableEntity, "validation_failed").json()
			details, _ := body["error"].(map[string]any)["details"].(map[string]any)
			fields, _ := details["fields"].(map[string]any)
			if _, ok := fields[tc.field]; !ok {
				t.Fatalf("error does not name %s: %v", tc.field, body)
			}
		})
	}
}

// An expired licence is a fact about the owner, not an input error.
func TestUpdateMeAcceptsAnExpiredLicence(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	u.patch("/v1/me", map[string]any{"cnh_expires_on": "2020-01-31"}).expect(http.StatusOK)
}

// ---------- photo ----------

func TestProfilePhotoRoundTrip(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	first := u.putPhoto(jpegBytes(t)).expect(http.StatusOK).json()
	firstURL, _ := first["photo_url"].(string)
	if firstURL == "" {
		t.Fatalf("photo_url is empty after an upload: %v", first)
	}
	keys := e.photos.Keys()
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "users/"+u.ID+"/photo-") ||
		!strings.HasSuffix(keys[0], ".jpg") {
		t.Fatalf("stored keys = %v, want one users/%s/photo-*.jpg", keys, u.ID)
	}
	if obj, _ := e.photos.Object(keys[0]); obj.ContentType != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg", obj.ContentType)
	}

	// GET /v1/me signs the same object.
	me := u.get("/v1/me").expect(http.StatusOK).json()
	if me["photo_url"] == nil {
		t.Fatal("GET /v1/me lost the photo")
	}

	// A new photo is a new key, and the old object goes.
	second := u.putPhoto(pngBytes(t)).expect(http.StatusOK).json()
	if second["photo_url"] == firstURL {
		t.Fatal("a new photo kept the old URL; a cached image would never refresh")
	}
	keys = e.photos.Keys()
	if len(keys) != 1 || !strings.HasSuffix(keys[0], ".png") {
		t.Fatalf("after replacing: keys = %v, want only the new png", keys)
	}

	// The photo reaches a login response too.
	login := e.anonymous().post("/v1/auth/login", map[string]any{
		"email": u.Email, "password": u.Password,
	}).expect(http.StatusOK).json()
	if login["user"].(map[string]any)["photo_url"] == nil {
		t.Fatal("login response has no photo_url")
	}

	u.delete("/v1/me/photo", nil).expect(http.StatusNoContent)
	if keys := e.photos.Keys(); len(keys) != 0 {
		t.Fatalf("after removing: keys = %v, want none", keys)
	}
	if u.get("/v1/me").expect(http.StatusOK).json()["photo_url"] != nil {
		t.Fatal("photo_url survived DELETE /v1/me/photo")
	}
	// Removing again is not an error.
	u.delete("/v1/me/photo", nil).expect(http.StatusNoContent)
}

func TestProfilePhotoRejectsWhatIsNotAPicture(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	// The name says .jpg; the bytes say text. The bytes win.
	u.putPhoto([]byte("isto não é uma foto, é um texto qualquer")).
		expectError(http.StatusUnprocessableEntity, "validation_failed")

	// A JSON body is not an upload.
	u.do(http.MethodPut, "/v1/me/photo", map[string]any{"photo": "base64"}).
		expectError(http.StatusUnprocessableEntity, "validation_failed")

	// Past the ceiling, refused without being stored.
	big := append(jpegBytes(t), bytes.Repeat([]byte{0}, identity.MaxPhotoBytes)...)
	u.putPhoto(big).expectError(http.StatusUnprocessableEntity, "validation_failed")

	if keys := e.photos.Keys(); len(keys) != 0 {
		t.Fatalf("a rejected upload was stored: %v", keys)
	}
}

// Deleting the account takes the photo with it (LGPD, SPEC.md D-10).
func TestDeleteAccountErasesThePhoto(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	u.putPhoto(jpegBytes(t)).expect(http.StatusOK)
	u.delete("/v1/me", map[string]any{"password": u.Password}).expect(http.StatusNoContent)

	if keys := e.photos.Keys(); len(keys) != 0 {
		t.Fatalf("the photo outlived the account: %v", keys)
	}
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, tinyImage(), nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, tinyImage()); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func tinyImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := range 8 {
		for y := range 8 {
			img.Set(x, y, color.RGBA{R: 0x5B, G: 0x9D, B: 0xFF, A: 0xFF})
		}
	}
	return img
}
