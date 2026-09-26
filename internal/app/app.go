// Package app assembles the HTTP application: it wires every module together and returns
// the router.
//
// It exists so that the object graph has exactly one definition. cmd/api boots the
// process — config, pool, migrations, signals — and then asks this package for a handler;
// the integration suite asks for the same handler against a throwaway database. If the
// wiring lived in cmd/api, the tests would have to reproduce it, and the copy would drift
// from the real thing precisely when it mattered.
package app

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/simonscabello/meu-auto-backend/internal/abastecimento"
	"github.com/simonscabello/meu-auto-backend/internal/catalog"
	"github.com/simonscabello/meu-auto-backend/internal/catalog/fipe"
	"github.com/simonscabello/meu-auto-backend/internal/identity"
	"github.com/simonscabello/meu-auto-backend/internal/insight"
	"github.com/simonscabello/meu-auto-backend/internal/maintenance"
	"github.com/simonscabello/meu-auto-backend/internal/notification"
	"github.com/simonscabello/meu-auto-backend/internal/obligation"
	"github.com/simonscabello/meu-auto-backend/internal/platform/auth"
	"github.com/simonscabello/meu-auto-backend/internal/platform/config"
	"github.com/simonscabello/meu-auto-backend/internal/platform/mailer"
	"github.com/simonscabello/meu-auto-backend/internal/platform/push"
	"github.com/simonscabello/meu-auto-backend/internal/platform/storage"
	"github.com/simonscabello/meu-auto-backend/internal/release"
	"github.com/simonscabello/meu-auto-backend/internal/vehicle"
)

// Deps are the runtime collaborators the modules need, built by the caller.
//
// They are parameters rather than something this package constructs because each one is
// the seam a test replaces: a throwaway pool, a mailer that captures instead of sends, a
// fixed location.
type Deps struct {
	Pool     *pgxpool.Pool
	Mailer   mailer.Mailer
	Location *time.Location
	Log      *slog.Logger

	// Photos is where profile pictures live: the bucket in production, memory in
	// development without one and in the integration suite.
	Photos storage.Store

	// Push delivers the morning reminders: FCM, the log (NOTIFICATIONS_DEBUG), or a
	// capturing fake in the integration suite. Nil when none is configured, and then there
	// is no reminder job at all — so nothing is ever recorded as sent without being sent.
	Push push.Sender
}

// App is what cmd/api serves and runs.
type App struct {
	Handler http.Handler

	// Reminders is the morning push job, nil when Deps.Push is. cmd/api starts it; the
	// integration suite calls its Run with the clock at nine.
	Reminders *notification.Reminders
}

// New wires every module and returns the HTTP handler and the reminder job.
//
// The construction order below is not stylistic. It follows the dependencies between the
// modules, and each departure from the obvious order is commented where it happens.
func New(cfg config.Config, deps Deps) App {
	tokens := auth.NewTokenService([]byte(cfg.JWTSecret), config.JWTIssuer)

	// The vehicle catalogue is a leaf: it depends on the database and on its provider, and
	// on no other module. It is built first because vehicle takes it as a CatalogPort.
	catalogService := catalog.NewService(
		catalog.NewRepository(deps.Pool),
		fipe.New(cfg.FipeAPIURL, cfg.FipeAPIToken, deps.Log),
		deps.Log)
	catalogHandler := catalog.NewHandler(catalogService, tokens)

	// Vehicle is built next because identity takes it as a UserDataEraser: vehicles carry
	// no user_id, so deleting an account cannot cascade to them.
	//
	// catalogService satisfies vehicle.CatalogPort structurally — the interface is declared
	// in vehicle and its signature is primitives only, so neither package imports the
	// other and the wiring is the only place that knows they meet.
	//
	// abastecimento.Capability satisfies vehicle.RefuelingPort the same way: it reads
	// fuel_type and does not know any abastecimento table.
	vehicleService := vehicle.NewService(
		vehicle.NewRepository(deps.Pool), catalogService, abastecimento.Capability{},
		deps.Location, deps.Log)
	vehicleHandler := vehicle.NewHandler(vehicleService, tokens)

	// Maintenance depends on vehicle for authorisation, and vehicle depends on
	// maintenance to materialise a new vehicle's suggested plans. The cycle is real, so
	// it is broken here, in the open, with a setter — rather than hidden inside a
	// container that would not make it go away.
	maintenanceService := maintenance.NewService(
		maintenance.NewRepository(deps.Pool), vehicleService, deps.Location)
	vehicleService.SetPlanInitializer(maintenanceService)
	maintenanceHandler := maintenance.NewHandler(maintenanceService, tokens)

	obligationService := obligation.NewService(
		obligation.NewRepository(deps.Pool), vehicleService, deps.Location)
	obligationHandler := obligation.NewHandler(obligationService, tokens)

	abastecimentoService := abastecimento.NewService(
		abastecimento.NewRepository(deps.Pool), vehicleService, deps.Location)
	abastecimentoHandler := abastecimento.NewHandler(abastecimentoService, tokens)

	// The read model composes the other modules rather than re-deriving anything, so it is
	// built last and depends on all of them. Consumption on last_abastecimento is asked of
	// abastecimentoService through the port — not recomputed here.
	insightService := insight.NewService(insight.NewRepository(deps.Pool), vehicleService,
		maintenanceService, obligationService, abastecimentoService, deps.Location)
	insightHandler := insight.NewHandler(insightService, tokens)

	identityHandler := identity.NewHandler(
		identity.NewService(identity.NewRepository(deps.Pool), tokens, deps.Mailer, deps.Log,
			deps.Photos, deps.Location, cfg.PasswordResetURL, vehicleService),
		tokens,
		cfg.TrustProxy,
	)

	// The release notice depends on nothing but configuration: no database, no module.
	releaseHandler := release.NewHandler(cfg.AppLatestVersion, cfg.AppAPKURL)

	// The phones are registered whether or not anything can send to them yet: a phone
	// that registers today is reached the day the key is configured.
	//
	// The reminders read cars and alerts through the adapters in reminder_ports.go —
	// the same alert list the app shows, so a push never disagrees with the screen.
	notificationRepo := notification.NewRepository(deps.Pool)
	notificationHandler := notification.NewHandler(notification.NewService(notificationRepo), tokens)

	var reminders *notification.Reminders
	if deps.Push != nil {
		reminders = notification.NewReminders(notificationRepo,
			reminderVehicles{vehicles: vehicleService},
			reminderAlerts{insight: insightService},
			deps.Push, deps.Location, deps.Log)
	}

	return App{
		Handler: newRouter(cfg, deps.Pool, identityHandler, vehicleHandler, catalogHandler,
			maintenanceHandler, obligationHandler, abastecimentoHandler, insightHandler,
			releaseHandler, notificationHandler),
		Reminders: reminders,
	}
}
