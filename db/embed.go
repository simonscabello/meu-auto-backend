// Package db carries the SQL migrations as an embedded filesystem.
//
// They ship inside the binary so a deploy is a single artifact: no migration files to
// copy, no separate release step, and no chance of the schema and the code drifting apart
// between the build and the run.
//
// This package exists at the repository root because go:embed cannot reach outside its
// own directory — the code that applies these migrations lives in
// internal/platform/database and receives this filesystem as a parameter.
package db

import "embed"

// Migrations holds the golang-migrate files under the "migrations" directory.
//
//go:embed migrations/*.sql
var Migrations embed.FS
