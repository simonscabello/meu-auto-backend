package database

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	// Registers the "postgres" database driver with golang-migrate.
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Migrate applies every pending migration from the embedded filesystem.
//
// It runs at boot rather than as a separate release step, because Railway has no release
// phase and a VPS deploy is just a container restart. Concurrency is safe: the
// golang-migrate postgres driver takes a pg_advisory_lock for the duration of the run, so
// if two instances start together one applies the migrations and the other waits and then
// finds nothing to do (SPEC.md D-05).
func Migrate(databaseURL string, fsys fs.FS, log *slog.Logger) error {
	source, err := iofs.New(fsys, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}

	migrator, err := migrate.NewWithSourceInstance("iofs", source, normalizeURL(databaseURL))
	if err != nil {
		return fmt.Errorf("initialise migrator: %w", err)
	}
	defer func() {
		// The migrator opens its own connection, separate from the pool. Leaking it would
		// hold an idle connection for the life of the process.
		if sourceErr, dbErr := migrator.Close(); sourceErr != nil || dbErr != nil {
			log.Warn("failed to close migrator",
				slog.Any("source_error", sourceErr),
				slog.Any("database_error", dbErr))
		}
	}()

	// A dirty schema means a previous run failed partway through. Applying more
	// migrations on top of an unknown state is how a half-migrated database becomes an
	// unrecoverable one — stop and make a human look.
	if version, dirty, err := migrator.Version(); err != nil {
		if !errors.Is(err, migrate.ErrNilVersion) {
			return fmt.Errorf("read schema version: %w", err)
		}
		log.Info("no migrations applied yet, initialising schema")
	} else if dirty {
		return fmt.Errorf(
			"schema is dirty at version %d: a previous migration failed partway through; "+
				"inspect the database, fix it by hand, then run `migrate force <version>`",
			version)
	}

	err = migrator.Up()
	switch {
	case errors.Is(err, migrate.ErrNoChange):
		log.Info("schema is up to date")
		return nil
	case err != nil:
		return fmt.Errorf("apply migrations: %w", err)
	}

	version, _, err := migrator.Version()
	if err != nil {
		// The migrations did apply; only the read-back failed. Not worth failing the boot.
		log.Warn("migrations applied but schema version could not be read", slog.Any("error", err))
		return nil
	}
	log.Info("migrations applied", slog.Uint64("schema_version", uint64(version)))
	return nil
}
