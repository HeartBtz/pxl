package service

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HeartBtz/pxl/internal/config"
	"github.com/HeartBtz/pxl/internal/repository"
	"github.com/HeartBtz/pxl/internal/storage"
)

// A local database/sql fixture, not a network driver. INSERT returns a row,
// then COMMIT loses its response; the application cannot infer the outcome.
type uncertainCommitDriver struct{}
type uncertainCommitConn struct{}
type uncertainCommitTx struct{}
type uncertainCommitRows struct {
	insert   bool
	returned bool
}

func (uncertainCommitDriver) Open(string) (driver.Conn, error) { return uncertainCommitConn{}, nil }
func (uncertainCommitConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not supported")
}
func (uncertainCommitConn) Close() error              { return nil }
func (uncertainCommitConn) Begin() (driver.Tx, error) { return uncertainCommitTx{}, nil }
func (uncertainCommitTx) Commit() error               { return io.ErrUnexpectedEOF }
func (uncertainCommitTx) Rollback() error             { return nil }
func (uncertainCommitConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	return &uncertainCommitRows{insert: strings.HasPrefix(q, "INSERT INTO images")}, nil
}
func (r *uncertainCommitRows) Columns() []string { return []string{"created_at"} }
func (r *uncertainCommitRows) Close() error      { return nil }
func (r *uncertainCommitRows) Next(values []driver.Value) error {
	if !r.insert || r.returned {
		return io.EOF
	}
	r.returned = true
	values[0] = time.Now()
	return nil
}

func init() { sql.Register("pxl-uncertain-commit-fixture", uncertainCommitDriver{}) }

func TestUploadPreservesObjectOnUncertainCommit(t *testing.T) {
	db, err := sql.Open("pxl-uncertain-commit-fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	store, err := storage.NewLocalBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := &config.Config{Upload: config.UploadConfig{MaxSizeBytes: 4096, MaxPixels: 100, TempDir: root, AllowedTypes: []string{"image/png"}}}
	svc := NewImageService(repository.NewImageRepository(&repository.DB{DB: db}), nil, store, nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Upload(t.Context(), nil, "fixture.png", data.Bytes()); !errors.Is(err, repository.ErrCommitUncertain) {
		t.Fatal("lost COMMIT not classified as uncertain")
	}
	files := 0
	if err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatal("possibly committed object was destroyed")
	}
}
