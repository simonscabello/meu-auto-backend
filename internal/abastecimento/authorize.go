package abastecimento

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/abastecimento/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
)

func (s *Service) authorize(ctx context.Context, userID, id uuid.UUID) (db.Abastecimento, error) {
	row, err := s.repo.ForUser(ctx, id, userID)
	switch {
	case errors.Is(err, ErrNotFound):
		return db.Abastecimento{}, errNotFound()
	case err != nil:
		return db.Abastecimento{}, apperr.Internal(err)
	}
	return row, nil
}

func errNotFound() error {
	return apperr.NotFound("Abastecimento não encontrado.")
}
