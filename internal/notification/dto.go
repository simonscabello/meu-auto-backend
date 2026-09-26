package notification

import (
	"strings"

	"github.com/simonscabello/meu-auto-backend/internal/platform/validate"
)

// maxTokenLength is far above any FCM registration token (about 160 characters today) and
// low enough that the column cannot be used as storage.
const maxTokenLength = 4096

// platformAndroid is the only platform reminders reach for now: iOS needs an APNs key and
// a build this project cannot make yet.
const platformAndroid = "android"

type registerDeviceRequest struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
}

func (r registerDeviceRequest) validate() error {
	errs := validate.New()
	addTokenProblems(errs, r.Token)
	if r.Platform != platformAndroid {
		errs.Add("platform", `Plataforma não suportada: use "android".`)
	}
	return errs.Err("Requisição inválida.")
}

type forgetDeviceRequest struct {
	Token string `json:"token"`
}

func (r forgetDeviceRequest) validate() error {
	errs := validate.New()
	addTokenProblems(errs, r.Token)
	return errs.Err("Requisição inválida.")
}

func addTokenProblems(errs validate.Errors, token string) {
	switch {
	case strings.TrimSpace(token) == "":
		errs.Add("token", "Informe o token do aparelho.")
	case len(token) > maxTokenLength:
		errs.Add("token", "Token do aparelho longo demais.")
	}
}
