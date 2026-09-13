package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
)

// LocalBackend stocke les images sur le système de fichiers local.
// Uses os.Root for traversal-resistant filesystem operations, including symlink races.
type LocalBackend struct {
	basePath string
	root     *os.Root
}

// NewLocalBackend crée un nouveau backend de stockage local.
// Crée le répertoire de base s'il n'existe pas.
func NewLocalBackend(basePath string) (*LocalBackend, error) {
	abs, err := filepath.Abs(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve storage path %s: %w", basePath, err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create storage dir %s: %w", abs, err)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	return &LocalBackend{basePath: abs, root: root}, nil
}

func (b *LocalBackend) Close() error { return b.root.Close() }

// safePath validates relative keys; os.Root enforces confinement at each operation.
func (b *LocalBackend) safePath(path string) (string, error) {
	if !filepath.IsLocal(path) || filepath.Clean(path) == "." {
		return "", fmt.Errorf("path traversal attempt blocked: %s", path)
	}
	return filepath.Clean(path), nil
}

func (b *LocalBackend) Type() string { return "local" }

func (b *LocalBackend) Put(ctx context.Context, path string, reader io.Reader, _ int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	full, err := b.safePath(path)
	if err != nil {
		return 0, err
	}
	if err := b.root.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return 0, fmt.Errorf("mkdir: %w", err)
	}
	tmpName := filepath.Join(filepath.Dir(full), ".pxl-upload-"+uuid.NewString())
	f, err := b.root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, fmt.Errorf("create temporary file: %w", err)
	}
	committed := false
	defer func() {
		_ = f.Close()
		if !committed {
			_ = b.root.Remove(tmpName)
		}
	}()
	if err := f.Chmod(0o640); err != nil {
		return 0, fmt.Errorf("chmod temporary file: %w", err)
	}
	n, err := io.Copy(f, reader)
	if err != nil {
		return n, fmt.Errorf("write: %w", err)
	}
	if err := f.Sync(); err != nil {
		return n, fmt.Errorf("sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return n, fmt.Errorf("close: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return n, err
	}
	if err := b.root.Rename(tmpName, full); err != nil {
		return n, fmt.Errorf("commit: %w", err)
	}
	committed = true
	return n, nil
}

// PutFile atomically commits an already validated staging file. The staging
// file must live inside the storage root so an upload cannot rename an
// arbitrary host file.
func (b *LocalBackend) PutFile(ctx context.Context, path string, file *os.File, size int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	full, err := b.safePath(path)
	if err != nil {
		return 0, err
	}
	source, err := filepath.Abs(file.Name())
	if err != nil {
		return 0, fmt.Errorf("resolve staging file: %w", err)
	}
	if !strings.HasPrefix(source, b.basePath+string(os.PathSeparator)) {
		return 0, ErrFileCommitUnsupported
	}
	source, err = filepath.Rel(b.basePath, source)
	if err != nil {
		return 0, err
	}
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return 0, fmt.Errorf("invalid staging file")
	}
	if err := b.root.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return 0, fmt.Errorf("mkdir: %w", err)
	}
	if err := file.Sync(); err != nil {
		return 0, fmt.Errorf("sync staging file: %w", err)
	}
	if err := file.Chmod(0o640); err != nil {
		return 0, fmt.Errorf("chmod staging file: %w", err)
	}
	// Link into a private unique name, then verify the actual linked inode.
	// A pre-rename stat alone would let a substituted staging symlink win a race.
	staged := filepath.Join(filepath.Dir(full), ".pxl-commit-"+uuid.NewString())
	if err := b.root.Link(source, staged); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return 0, ErrFileCommitUnsupported
		}
		return 0, err
	}
	defer func() { _ = b.root.Remove(staged) }()
	linked, err := b.root.Lstat(staged)
	if err != nil || !linked.Mode().IsRegular() || !os.SameFile(info, linked) {
		return 0, fmt.Errorf("staging file changed before commit")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := b.root.Rename(staged, full); err != nil {
		return 0, err
	}
	_ = b.root.Remove(source)
	return size, nil
}

func (b *LocalBackend) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	full, err := b.safePath(path)
	if err != nil {
		return nil, err
	}
	f, err := b.root.OpenFile(full, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("not a regular storage file")
	}
	return f, nil
}

func (b *LocalBackend) GetRange(ctx context.Context, path string, offset, length int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rc, err := b.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	f := rc.(*os.File)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("seek: %w", err)
	}
	return &limitedReadCloser{Reader: io.LimitReader(f, length), Closer: f}, nil
}

func (b *LocalBackend) Delete(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := b.safePath(path)
	if err != nil {
		return err
	}
	return b.root.Remove(full)
}

func (b *LocalBackend) Exists(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	full, err := b.safePath(path)
	if err != nil {
		return false, err
	}
	_, err = b.root.Stat(full)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (b *LocalBackend) Size(ctx context.Context, path string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	full, err := b.safePath(path)
	if err != nil {
		return 0, err
	}
	info, err := b.root.Stat(full)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

type limitedReadCloser struct {
	io.Reader
	io.Closer
}
