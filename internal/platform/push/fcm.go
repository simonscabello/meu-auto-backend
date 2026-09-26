package push

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Credentials identify the service account allowed to send through one Firebase project.
type Credentials struct {
	ProjectID    string
	ClientEmail  string
	PrivateKeyID string
	PrivateKey   *rsa.PrivateKey
	TokenURI     string
}

const (
	defaultTokenURI = "https://oauth2.googleapis.com/token"
	fcmEndpoint     = "https://fcm.googleapis.com"
	messagingScope  = "https://www.googleapis.com/auth/firebase.messaging"

	// Google issues access tokens for an hour. One is reused until shortly before it
	// expires, so a morning's reminders cost one token request, not one per phone.
	assertionLifetime = time.Hour
	renewalMargin     = 5 * time.Minute

	requestTimeout = 10 * time.Second
)

// ParseServiceAccount reads the JSON key file the Firebase console downloads (Project
// settings → Service accounts → Generate new private key).
//
// The error names what is wrong and never repeats the key itself: it ends up in a deploy
// log.
func ParseServiceAccount(raw []byte) (Credentials, error) {
	var file struct {
		Type         string `json:"type"`
		ProjectID    string `json:"project_id"`
		PrivateKeyID string `json:"private_key_id"`
		PrivateKey   string `json:"private_key"`
		ClientEmail  string `json:"client_email"`
		TokenURI     string `json:"token_uri"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return Credentials{}, errors.New("is not the JSON key file of a service account")
	}
	if file.Type != "" && file.Type != "service_account" {
		return Credentials{}, fmt.Errorf("is a %q key, not a service account", file.Type)
	}

	var missing []string
	if strings.TrimSpace(file.ProjectID) == "" {
		missing = append(missing, "project_id")
	}
	if strings.TrimSpace(file.ClientEmail) == "" {
		missing = append(missing, "client_email")
	}
	if strings.TrimSpace(file.PrivateKey) == "" {
		missing = append(missing, "private_key")
	}
	if len(missing) > 0 {
		return Credentials{}, fmt.Errorf("has no %s", strings.Join(missing, ", "))
	}

	key, err := parsePrivateKey(file.PrivateKey)
	if err != nil {
		return Credentials{}, err
	}

	tokenURI := strings.TrimSpace(file.TokenURI)
	if tokenURI == "" {
		tokenURI = defaultTokenURI
	}
	return Credentials{
		ProjectID:    file.ProjectID,
		ClientEmail:  file.ClientEmail,
		PrivateKeyID: file.PrivateKeyID,
		PrivateKey:   key,
		TokenURI:     tokenURI,
	}, nil
}

func parsePrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("has a private_key that is not PEM")
	}
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("has a private_key that is not RSA")
		}
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("has a private_key that cannot be read")
}

// FCM sends through Firebase Cloud Messaging's HTTP v1 API.
//
// Written against the HTTP API rather than the Admin SDK: the SDK would bring a large
// dependency tree to make one POST, and signing the OAuth assertion needs nothing the
// project does not already have (golang-jwt signs the access tokens too).
type FCM struct {
	creds  Credentials
	client *http.Client
	now    func() time.Time

	// endpoint is FCM's base URL; the tests point it, and the token URI, at a stand-in.
	endpoint string

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

func NewFCM(creds Credentials) *FCM {
	return &FCM{
		creds:    creds,
		client:   &http.Client{Timeout: requestTimeout},
		now:      time.Now,
		endpoint: fcmEndpoint,
	}
}

// Send delivers one message to one phone.
func (f *FCM) Send(ctx context.Context, token string, msg Message) error {
	access, err := f.token(ctx)
	if err != nil {
		return err
	}

	data := msg.Data
	if data == nil {
		data = map[string]string{}
	}
	payload, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"token": token,
			"notification": map[string]string{
				"title": msg.Title,
				"body":  msg.Body,
			},
			"data": data,
			"android": map[string]any{
				"priority": "HIGH",
				"notification": map[string]string{
					"channel_id": ChannelID,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("fcm: encode message: %w", err)
	}

	sendURL := fmt.Sprintf("%s/v1/projects/%s/messages:send",
		f.endpoint, url.PathEscape(f.creds.ProjectID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sendURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("fcm: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")

	res, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("fcm: send: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	return sendError(res)
}

// sendError turns a refusal into ErrUnregistered when the token itself is what was
// refused, and into an ordinary error otherwise.
//
// Deliberately narrow. A bare 404 or a bare INVALID_ARGUMENT can also mean a wrong project
// id or a malformed message — a bug on this side — and treating those as dead tokens would
// quietly unsubscribe every phone on the first bad deploy.
func sendError(res *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))

	var body struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)

	for _, detail := range body.Error.Details {
		if detail.ErrorCode == "UNREGISTERED" {
			return ErrUnregistered
		}
	}
	if body.Error.Status == "INVALID_ARGUMENT" &&
		strings.Contains(strings.ToLower(body.Error.Message), "registration token") {
		return ErrUnregistered
	}
	return fmt.Errorf("fcm: send refused: %d %s: %s",
		res.StatusCode, body.Error.Status, body.Error.Message)
}

// token returns an OAuth access token for FCM, asking Google for a new one only when the
// cached one is about to expire.
func (f *FCM) token(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := f.now()
	if f.accessToken != "" && now.Before(f.expiresAt.Add(-renewalMargin)) {
		return f.accessToken, nil
	}

	assertion := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   f.creds.ClientEmail,
		"scope": messagingScope,
		"aud":   f.creds.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(assertionLifetime).Unix(),
	})
	if f.creds.PrivateKeyID != "" {
		assertion.Header["kid"] = f.creds.PrivateKeyID
	}
	signed, err := assertion.SignedString(f.creds.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("fcm: sign assertion: %w", err)
	}

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {signed},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.creds.TokenURI,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("fcm: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fcm: token: %w", err)
	}
	defer res.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("fcm: token: status %d, unreadable body", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK || body.AccessToken == "" {
		return "", fmt.Errorf("fcm: token refused: %d %s %s",
			res.StatusCode, body.Error, body.Description)
	}

	f.accessToken = body.AccessToken
	f.expiresAt = now.Add(time.Duration(body.ExpiresIn) * time.Second)
	return f.accessToken, nil
}
