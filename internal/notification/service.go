// Package notification sends the morning push reminders and keeps the phones they go to
// (SPEC.md D-19).
//
// It stores two things and derives everything else. push_devices is where a reminder can
// go; notification_log is what each person was already told. Whether an IPVA is late is
// not decided here: the reminders read the same alert list the app shows, through a port,
// so a push can never disagree with the screen it opens (RN-06, RN-06b).
package notification

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// Service owns the phones an account asked to be reminded on.
type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// RegisterDevice records, or reconfirms, one of the caller's phones.
//
// A token already registered to another account moves to this one (see UpsertDevice): it
// names the installation, and the installation changed hands.
func (s *Service) RegisterDevice(ctx context.Context, userID uuid.UUID, req registerDeviceRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	return s.repo.UpsertDevice(ctx, userID, strings.TrimSpace(req.Token), req.Platform)
}

// ForgetDevice stops reminders to one of the caller's phones. The app calls it when the
// owner signs out, BEFORE it discards the session — afterwards it could not authenticate,
// and the next person on that phone would get the previous owner's reminders.
func (s *Service) ForgetDevice(ctx context.Context, userID uuid.UUID, req forgetDeviceRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	return s.repo.DeleteDeviceForUser(ctx, userID, strings.TrimSpace(req.Token))
}
