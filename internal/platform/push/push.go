// Package push carries a finished notification to one phone.
//
// It knows nothing about cars or reminders. Deciding who is told what belongs to
// internal/notification; this package only takes a message and a token and delivers it,
// which is what lets the reminder rules be tested against a fake sender without talking
// to Google.
package push

import (
	"context"
	"errors"
)

// ChannelID is the Android notification channel reminders appear in.
//
// It MUST equal the channel the app creates (meu-auto-app, lib/core/push/push_service.dart).
// A message addressed to a channel the phone does not have arrives and is never shown, with
// no error anywhere.
const ChannelID = "lembretes"

// Message is one notification as the phone shows it, plus what the app reads when it is
// tapped.
type Message struct {
	Title string
	Body  string

	// Data travels with the notification and says where the tap should lead. FCM accepts
	// strings only.
	Data map[string]string
}

// ErrUnregistered means the token no longer reaches an installation: the app was removed,
// its data cleared, or the token rotated. The caller forgets the token; retrying is
// pointless.
var ErrUnregistered = errors.New("push: token no longer registered")

// Sender delivers one message to one phone.
type Sender interface {
	Send(ctx context.Context, token string, msg Message) error
}
