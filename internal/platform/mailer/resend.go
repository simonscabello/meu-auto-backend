package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"time"
)

const (
	resendEndpoint = "https://api.resend.com/emails"
	resendTimeout  = 10 * time.Second

	// Enough of the provider's response to diagnose a failure, without copying an
	// arbitrarily large body into a log line.
	maxErrorBodyBytes = 1024
)

// ResendMailer sends through Resend's HTTP API.
//
// Plain net/http rather than the vendor SDK: this is one POST with three fields, and the
// SDK would be a dependency carrying its own transitive tree for no gain.
type ResendMailer struct {
	APIKey string
	From   string
	Client *http.Client
}

// NewResend builds a Resend-backed mailer.
func NewResend(apiKey, from string) *ResendMailer {
	return &ResendMailer{
		APIKey: apiKey,
		From:   from,
		Client: &http.Client{Timeout: resendTimeout},
	}
}

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
	Text    string   `json:"text"`
}

func (m *ResendMailer) SendPasswordReset(ctx context.Context, to, name, resetURL string) error {
	// name comes from user input and lands inside HTML; escaping it here is what stops a
	// display name from injecting markup into the message.
	safeName := html.EscapeString(name)
	safeURL := html.EscapeString(resetURL)

	body := resendRequest{
		From:    m.From,
		To:      []string{to},
		Subject: "Redefinição de senha — Meu Auto",
		HTML: fmt.Sprintf(`<p>Olá, %s.</p>
<p>Recebemos um pedido para redefinir a senha da sua conta no Meu Auto.</p>
<p><a href="%s">Redefinir minha senha</a></p>
<p>O link expira em 1 hora e só pode ser usado uma vez.</p>
<p>Se não foi você que pediu, ignore este e-mail: sua senha continua a mesma.</p>`,
			safeName, safeURL),
		Text: fmt.Sprintf(`Olá, %s.

Recebemos um pedido para redefinir a senha da sua conta no Meu Auto.

Redefinir minha senha: %s

O link expira em 1 hora e só pode ser usado uma vez.

Se não foi você que pediu, ignore este e-mail: sua senha continua a mesma.`,
			name, resetURL),
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("mailer: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		resendEndpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("mailer: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.Client.Do(req)
	if err != nil {
		return fmt.Errorf("mailer: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusMultipleChoices {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return fmt.Errorf("mailer: resend responded %d: %s", resp.StatusCode, detail)
	}

	// The body carries a message id that nothing tracks yet. Drain it so the connection
	// returns to the pool instead of being closed.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
	return nil
}
