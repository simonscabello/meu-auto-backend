package identity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/identity/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
)

// Profile photo limits.
//
// The app shrinks a photo to 1024px before sending it, which lands at a few hundred KB;
// 5 MB leaves room for a client that does not, without letting one request hold the
// server's 30-second write window for long.
const (
	MaxPhotoBytes = 5 << 20

	// How long a signed photo URL reads. A client refetches /v1/me on every launch and
	// gets a fresh one; a day covers an app left open overnight.
	photoURLTTL = 24 * time.Hour
)

// The formats a photo may be. Decided by the bytes, never by the name or the header the
// client sent: those say whatever the client wants them to.
var photoExtensions = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/webp": "webp",
}

// UpdateProfile applies a PATCH to the caller's account.
func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, req updateMeRequest) (db.User, error) {
	update, err := req.validate(civil.Today(s.now, s.loc))
	if err != nil {
		return db.User{}, err
	}

	user, err := s.repo.UpdateUserProfile(ctx, userID, update)
	switch {
	case errors.Is(err, ErrUserNotFound):
		return db.User{}, apperr.Unauthorized("Sessão inválida. Entre novamente.")
	case err != nil:
		return db.User{}, apperr.Internal(err)
	}
	return user, nil
}

// SetPhoto stores a new profile photo and records it on the account.
//
// The object is written under a key it has never had — users/{id}/photo-{uuid}.{ext} —
// so a client's cached copy of the old URL can never show the new picture or the other
// way round, and a write that fails half way cannot damage the photo in use. The old
// object goes only once the row names the new one; if that delete fails, a private
// orphan is left and logged, never a broken profile.
func (s *Service) SetPhoto(ctx context.Context, userID uuid.UUID, data []byte) (db.User, error) {
	if len(data) == 0 {
		return db.User{}, apperr.Validation("Não foi possível salvar a foto.",
			map[string]any{"photo": "Envie uma foto."})
	}
	if len(data) > MaxPhotoBytes {
		return db.User{}, photoTooLarge()
	}
	contentType := http.DetectContentType(data)
	ext, ok := photoExtensions[contentType]
	if !ok {
		return db.User{}, apperr.Validation("Não foi possível salvar a foto.",
			map[string]any{"photo": "Envie uma foto em JPEG, PNG ou WebP."})
	}

	objectID, err := uuid.NewV7()
	if err != nil {
		return db.User{}, apperr.Internal(err)
	}
	key := fmt.Sprintf("users/%s/photo-%s.%s", userID, objectID, ext)
	if err := s.photos.Put(ctx, key, contentType, bytes.NewReader(data), int64(len(data))); err != nil {
		return db.User{}, apperr.Internal(err)
	}

	user, previous, err := s.repo.SetUserPhoto(ctx, userID, &key)
	if err != nil {
		// The row does not name the object, so nothing would ever reach it.
		s.dropPhoto(ctx, key)
		if errors.Is(err, ErrUserNotFound) {
			return db.User{}, apperr.Unauthorized("Sessão inválida. Entre novamente.")
		}
		return db.User{}, apperr.Internal(err)
	}
	if previous != nil && *previous != key {
		s.dropPhoto(ctx, *previous)
	}
	return user, nil
}

// RemovePhoto clears the profile photo. Removing one that is not there is not an error.
func (s *Service) RemovePhoto(ctx context.Context, userID uuid.UUID) error {
	_, previous, err := s.repo.SetUserPhoto(ctx, userID, nil)
	switch {
	case errors.Is(err, ErrUserNotFound):
		return apperr.Unauthorized("Sessão inválida. Entre novamente.")
	case err != nil:
		return apperr.Internal(err)
	}
	if previous != nil {
		s.dropPhoto(ctx, *previous)
	}
	return nil
}

// PhotoURL signs a URL for the user's photo, or returns nil when there is none.
//
// A signing failure costs the picture, not the response: the app falls back to the
// initial, and a profile that fails to load over a photo would be the worse bug.
func (s *Service) PhotoURL(ctx context.Context, user db.User) *string {
	if user.PhotoKey == nil {
		return nil
	}
	url, err := s.photos.SignedURL(ctx, *user.PhotoKey, photoURLTTL)
	if err != nil {
		s.log.Error("failed to sign photo url",
			slog.String("user_id", user.ID.String()), slog.Any("error", err))
		return nil
	}
	return &url
}

func (s *Service) dropPhoto(ctx context.Context, key string) {
	if err := s.photos.Delete(ctx, key); err != nil {
		s.log.Warn("failed to delete replaced photo, object left behind",
			slog.String("key", key), slog.Any("error", err))
	}
}

func photoTooLarge() error {
	return apperr.Validation("Não foi possível salvar a foto.",
		map[string]any{"photo": "A foto passa de 5 MB."})
}
