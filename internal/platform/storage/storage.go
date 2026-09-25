// Package storage keeps files that do not belong in Postgres — today, profile photos —
// in private, S3-compatible object storage.
//
// The bucket is private and stays that way: nothing is ever made public, and a client
// reads an object only through a URL this process signs, which expires. The database
// stores the object's key, never a URL, so rotating credentials or moving provider is a
// configuration change (SPEC.md D-06, D-17).
//
// Nothing here is specific to a provider. Railway Buckets speak S3; so do R2, Tigris and
// MinIO. What differs is four variables.
package storage

import (
	"context"
	"io"
	"time"
)

// Store is the one seam the modules see. Identity depends on it, never on the S3 SDK, so
// a test can swap in Memory and a provider change touches only this package.
type Store interface {
	// Put writes an object, replacing any object already at key.
	Put(ctx context.Context, key, contentType string, body io.Reader, size int64) error

	// Delete removes an object. Deleting a key that does not exist is not an error.
	Delete(ctx context.Context, key string) error

	// SignedURL returns a URL that reads the object until it expires.
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}
