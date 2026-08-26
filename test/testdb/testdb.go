// Package testdb hands each test its own migrated PostgreSQL database.
//
// SPEC.md D-09 is explicit that there are no database mocks here: sqlc guarantees types,
// not semantics, and a mocked query only ever proves the mock agrees with itself. So the
// tests talk to a real Postgres — the same major version production runs.
//
// # How isolation works
//
// One container per test binary. Migrations are applied once, to a template database;
// every test then gets a fresh database cloned from that template with
// CREATE DATABASE ... TEMPLATE, which Postgres does by copying files rather than by
// replaying the migrations. A clone costs tens of milliseconds, so isolation is cheap
// enough that no test has to clean up after itself.
//
// Cloning rather than truncating is also the safer choice here. TRUNCATE users CASCADE
// reaches *tables*, not rows, and would wipe the global maintenance_items catalogue that
// migration 000005 seeds — which never runs again (CLAUDE.md). A test that quietly lost
// the catalogue would fail somewhere else entirely.
package testdb

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/simonscabello/meu-auto-backend/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/database"
)

// image is pinned to the major version production runs. Postgres 16 is what
// docker-compose.yml serves locally and what Railway provisions; a suite passing on a
// different major would be testing a database nobody deploys.
const image = "postgres:16-alpine"

// startTimeout covers pulling the image on a cold machine, not just booting it.
const startTimeout = 5 * time.Minute

var (
	// adminURL points at a database that is neither the template nor any test's, so it can
	// stay connected while databases are created and dropped around it.
	adminURL string

	templateName string

	container *postgres.PostgresContainer

	// Postgres locks the template for the duration of CREATE DATABASE, and a second
	// concurrent clone of the same template fails outright. Serialising the clone keeps
	// t.Parallel() usable — it is the only part of a test's setup that cannot overlap.
	cloneMu sync.Mutex

	dbCounter atomic.Uint64
)

// DB is one test's private database.
type DB struct {
	Pool *pgxpool.Pool
	URL  string
	Name string
}

// Main runs the package's tests with a container and a migrated template in place.
//
// Every integration package needs exactly this TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }
func Main(m *testing.M) int {
	ctx := context.Background()

	if err := start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "testdb: %v\n", err)
		return 1
	}
	defer stop(ctx)

	return m.Run()
}

// New returns a fresh, fully migrated database for the calling test.
//
// The database is dropped when the test ends. Set TESTDB_KEEP=1 to leave behind the
// databases of failing tests — the name is logged so it can be inspected with psql.
func New(t *testing.T) *DB {
	t.Helper()

	if adminURL == "" {
		t.Fatal("testdb: not initialised — the package needs " +
			"func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }")
	}

	ctx := t.Context()
	name := databaseNameFor(t)

	if err := clone(ctx, name); err != nil {
		t.Fatalf("testdb: create database %s: %v", name, err)
	}

	dbURL := urlForDatabase(adminURL, name)
	pool, err := database.NewPool(ctx, dbURL)
	if err != nil {
		t.Fatalf("testdb: connect to %s: %v", name, err)
	}

	t.Cleanup(func() {
		pool.Close()

		if t.Failed() && os.Getenv("TESTDB_KEEP") != "" {
			t.Logf("testdb: keeping database %q for inspection", name)
			return
		}

		// A context of its own: the test's is already cancelled by the time cleanup runs.
		dropCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := drop(dropCtx, name); err != nil {
			t.Logf("testdb: drop database %s: %v", name, err)
		}
	})

	return &DB{Pool: pool, URL: dbURL, Name: name}
}

// ---------- lifecycle ----------

func start(ctx context.Context) error {
	// An external server short-circuits the container. It is the fast inner loop —
	// `docker compose up -d` is already running on port 5433 for local development — and
	// it is also the escape hatch for a CI runner without a usable Docker socket.
	//
	// It must point at a *server*, not at the application's own database: this package
	// creates and drops databases on it.
	if external := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")); external != "" {
		adminURL = external
	} else {
		startCtx, cancel := context.WithTimeout(ctx, startTimeout)
		defer cancel()

		pg, err := postgres.Run(startCtx, image,
			postgres.WithDatabase("postgres"),
			postgres.WithUsername("meuauto"),
			postgres.WithPassword("meuauto"),
			postgres.BasicWaitStrategies(),
		)
		if err != nil {
			return fmt.Errorf("start %s: %w", image, err)
		}
		container = pg

		adminURL, err = pg.ConnectionString(startCtx, "sslmode=disable")
		if err != nil {
			return fmt.Errorf("read connection string: %w", err)
		}
	}

	// Unique per process rather than a fixed name: against an external server, two test
	// binaries running at once (go test ./... builds one per package) would otherwise
	// drop each other's template mid-run.
	templateName = fmt.Sprintf("meuauto_tpl_%d_%d", os.Getpid(), time.Now().UnixNano()%1e6)

	if err := exec(ctx, adminURL, `CREATE DATABASE `+quote(templateName)); err != nil {
		return fmt.Errorf("create template database: %w", err)
	}

	// The real migration path, not a schema dump: a migration that fails to apply is a bug
	// the suite should catch before a deploy does.
	if err := database.Migrate(
		urlForDatabase(adminURL, templateName), db.Migrations, silentLogger()); err != nil {
		return fmt.Errorf("migrate template database: %w", err)
	}
	return nil
}

func stop(ctx context.Context) {
	if container != nil {
		// Terminating the container takes the template with it.
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "testdb: terminate container: %v\n", err)
		}
		return
	}
	if templateName != "" {
		if err := drop(ctx, templateName); err != nil {
			fmt.Fprintf(os.Stderr, "testdb: drop template: %v\n", err)
		}
	}
}

// ---------- database plumbing ----------

func clone(ctx context.Context, name string) error {
	cloneMu.Lock()
	defer cloneMu.Unlock()

	return exec(ctx, adminURL,
		fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, quote(name), quote(templateName)))
}

// drop uses WITH (FORCE) so a leaked connection cannot leave an undroppable database
// behind and fail every later run against the same server.
func drop(ctx context.Context, name string) error {
	return exec(ctx, adminURL,
		fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quote(name)))
}

// exec opens a connection, runs one statement and closes it. CREATE DATABASE cannot run
// inside a transaction, and pooling a connection for administrative work would keep a
// session open on a database somebody is about to drop.
func exec(ctx context.Context, dsn, sql string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	_, err = conn.Exec(ctx, sql)
	return err
}

func urlForDatabase(base, name string) string {
	u, err := url.Parse(base)
	if err != nil {
		// start() already connected through this URL, so it parses.
		panic(fmt.Sprintf("testdb: unparsable database URL: %v", err))
	}
	u.Path = "/" + name
	return u.String()
}

// quote renders an identifier for SQL. These names are built here and never supplied by a
// caller, but a database name still reaches the server as text, and doubling the quotes is
// one line — the habit is worth more than the argument that this particular input is safe.
func quote(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

var unsafeNameChars = regexp.MustCompile(`[^a-z0-9]+`)

// databaseNameFor builds a name that says which test owns it, for the times one is left
// behind by TESTDB_KEEP or by a killed run.
func databaseNameFor(t *testing.T) string {
	// Subtests carry a slash; only the leaf is worth keeping.
	slug := unsafeNameChars.ReplaceAllString(strings.ToLower(path.Base(t.Name())), "_")
	slug = strings.Trim(slug, "_")

	// Postgres truncates identifiers at 63 bytes, and a truncated name could collide with
	// another test's. Budget for the counter and cut the slug, not the suffix.
	const maxSlug = 40
	if len(slug) > maxSlug {
		slug = slug[:maxSlug]
	}
	return fmt.Sprintf("test_%s_%d", slug, dbCounter.Add(1))
}

// silentLogger keeps the migration run's "migrations applied" lines out of the test
// output. A failure surfaces through the returned error, which is what a test reads.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
