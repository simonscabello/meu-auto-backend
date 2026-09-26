package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/insight"
	"github.com/simonscabello/meu-auto-backend/internal/notification"
	"github.com/simonscabello/meu-auto-backend/internal/vehicle"
)

// The reminder job reads cars and alerts through ports it declares itself. These adapters
// are the only place that knows those ports are the vehicle module and the insight read
// model: nothing imports insight (SPEC.md section 5), and notification is no exception.

type reminderVehicles struct {
	vehicles *vehicle.Service
}

func (a reminderVehicles) VehiclesOf(ctx context.Context, userID uuid.UUID) ([]notification.Vehicle, error) {
	rows, err := a.vehicles.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]notification.Vehicle, 0, len(rows))
	for _, row := range rows {
		out = append(out, notification.Vehicle{
			ID:       row.ID,
			Brand:    row.Brand,
			Model:    row.Model,
			Nickname: row.Nickname,
		})
	}
	return out, nil
}

type reminderAlerts struct {
	insight *insight.Service
}

// AlertsFor is the alerts screen, item for item and in its order: what the push says late
// is exactly what the screen it opens says late.
func (a reminderAlerts) AlertsFor(ctx context.Context, userID, vehicleID uuid.UUID) ([]notification.Alert, error) {
	alerts, err := a.insight.Alerts(ctx, userID, vehicleID)
	if err != nil {
		return nil, err
	}
	out := make([]notification.Alert, 0, len(alerts))
	for _, alert := range alerts {
		referenceID, err := uuid.Parse(alert.ReferenceID)
		if err != nil {
			// Every reference is a row id today. One that is not cannot be deduplicated,
			// and a reminder that might repeat is worse than one skipped.
			continue
		}
		out = append(out, notification.Alert{
			Kind:          string(alert.Kind),
			Severity:      string(alert.Severity),
			Title:         alert.Title,
			DueOn:         alert.DueOn,
			DueAtKm:       alert.DueAtKm,
			RemainingDays: alert.RemainingDays,
			RemainingKm:   alert.RemainingKm,
			ReferenceType: alert.ReferenceType,
			ReferenceID:   referenceID,
		})
	}
	return out, nil
}
