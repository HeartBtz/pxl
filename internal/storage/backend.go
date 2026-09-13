// Package storage définit l'interface d'abstraction de stockage de PXL
// et ses implémentations (système de fichiers local, S3/MinIO).
//
// L'interface Backend permet de stocker et récupérer des images de manière
// transparente, indépendamment du backend sous-jacent.
package storage

import (
	"context"
	"errors"
	"io"
	"os"
)

var ErrFileCommitUnsupported = errors.New("staged file cannot be committed directly")

// Backend abstracts image storage (local filesystem or S3-compatible).
type Backend interface {
	// Put writes data to the given path, returns bytes written.
	Put(ctx context.Context, path string, reader io.Reader, size int64) (int64, error)

	// Get returns a reader for the content at the given path.
	Get(ctx context.Context, path string) (io.ReadCloser, error)

	// GetRange returns a reader for a byte range.
	GetRange(ctx context.Context, path string, offset, length int64) (io.ReadCloser, error)

	// Delete removes the object at the given path.
	Delete(ctx context.Context, path string) error

	// Exists checks whether an object exists at the given path.
	Exists(ctx context.Context, path string) (bool, error)

	// Size returns the size in bytes of the object.
	Size(ctx context.Context, path string) (int64, error)

	// Type returns the backend type name.
	Type() string
}

// FilePutter is an optional fast path for local storage. When the staged file
// is on the same filesystem, implementations can commit it with an atomic
// rename instead of copying every uploaded byte a second time.
type FilePutter interface {
	PutFile(ctx context.Context, path string, file *os.File, size int64) (int64, error)
}
