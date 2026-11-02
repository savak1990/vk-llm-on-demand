// Package store loads and saves the control plane's single state document.
package store

import (
	"context"
	"errors"
)

type Version string

type Store interface {
	Load(ctx context.Context) (Document, Version, error)
	Save(ctx context.Context, d Document, v Version) (Version, error)
}

var (
	ErrConflict         = errors.New("store: version conflict")
	ErrRetriesExhausted = errors.New("store: conflict retries exhausted")
	ErrTooLarge         = errors.New("store: document above 256 KiB")
	ErrCorrupt          = errors.New("store: document unreadable")
	ErrSchemaTooNew     = errors.New("store: schemaVersion newer than binary")
	ErrInstallation     = errors.New("store: stored installationId differs from configured")
	ErrStoreReset       = errors.New("store: document reset during transition")
	ErrStoreMissing     = errors.New("store: state ConfigMap not found")
)
