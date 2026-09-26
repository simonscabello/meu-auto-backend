package notification

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/simonscabello/meu-auto-backend/internal/notification/db"
)

type Repository struct {
	queries *db.Queries
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{queries: db.New(pool)}
}

// ---------- devices ----------

func (r *Repository) UpsertDevice(ctx context.Context, userID uuid.UUID, token, platform string) error {
	if err := r.queries.UpsertDevice(ctx, db.UpsertDeviceParams{
		UserID: userID, Token: token, Platform: platform,
	}); err != nil {
		return fmt.Errorf("upsert device: %w", err)
	}
	return nil
}

// DeleteDeviceForUser forgets one of the caller's phones. Forgetting a phone that is
// already gone is not an error: signing out twice must not fail.
func (r *Repository) DeleteDeviceForUser(ctx context.Context, userID uuid.UUID, token string) error {
	if _, err := r.queries.DeleteDeviceForUser(ctx, db.DeleteDeviceForUserParams{
		Token: token, UserID: userID,
	}); err != nil {
		return fmt.Errorf("delete device: %w", err)
	}
	return nil
}

func (r *Repository) DeleteDeviceByToken(ctx context.Context, token string) error {
	if err := r.queries.DeleteDeviceByToken(ctx, token); err != nil {
		return fmt.Errorf("delete dead device: %w", err)
	}
	return nil
}

func (r *Repository) DeviceTokensFor(ctx context.Context, userID uuid.UUID) ([]string, error) {
	tokens, err := r.queries.ListDeviceTokensForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list device tokens: %w", err)
	}
	return tokens, nil
}

func (r *Repository) UsersWithDevices(ctx context.Context) ([]uuid.UUID, error) {
	users, err := r.queries.ListUsersWithDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users with devices: %w", err)
	}
	return users, nil
}

// ---------- log ----------

// RecordReminder notes that a reminder is going out, and reports false when the same
// reminder already went out on an earlier run.
func (r *Repository) RecordReminder(ctx context.Context, userID, vehicleID uuid.UUID, item reminder, sentOn time.Time) (bool, error) {
	rows, err := r.queries.RecordReminder(ctx, db.RecordReminderParams{
		UserID:    userID,
		VehicleID: vehicleID,
		Kind:      string(item.stage),
		SubjectID: item.alert.ReferenceID,
		Marker:    item.marker,
		SentOn:    sentOn,
	})
	if err != nil {
		return false, fmt.Errorf("record reminder: %w", err)
	}
	return rows == 1, nil
}

func (r *Repository) VehicleRemindedOn(ctx context.Context, userID, vehicleID uuid.UUID, day time.Time) (bool, error) {
	reminded, err := r.queries.VehicleRemindedOn(ctx, db.VehicleRemindedOnParams{
		UserID: userID, VehicleID: vehicleID, SentOn: day,
	})
	if err != nil {
		return false, fmt.Errorf("vehicle reminded on: %w", err)
	}
	return reminded, nil
}
