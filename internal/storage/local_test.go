package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLocalBackend_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	b, err := NewLocalBackend(dir)
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}

	ctx := context.Background()

	// The safePath function sanitizes paths so traversal is neutralized.
	// Verify that "../outside.txt" resolves to a path WITHIN basePath, not outside.
	tests := []struct {
		name string
		path string
	}{
		{"dotdot", "../outside.txt"},
		{"dotdot_multi", "../../etc/passwd"},
		{"absolute", "/etc/passwd"},
		{"dotdot_hidden", "foo/../../outside.txt"},
	}

	for _, tt := range tests {
		t.Run("Put_"+tt.name, func(t *testing.T) {
			data := []byte("test")
			_, err := b.Put(ctx, tt.path, bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return // error is fine too
			}
			// Verify file ended up inside basePath
			resolved, _ := b.safePath(tt.path)
			rel, relErr := filepath.Rel(dir, resolved)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				t.Errorf("path %q resolved to %q which is outside basePath %q", tt.path, resolved, dir)
			}
			// Clean up
			os.Remove(resolved)
		})
	}

	// Verify no file was created outside basePath
	parentEntries, _ := os.ReadDir(filepath.Dir(dir))
	for _, e := range parentEntries {
		if e.Name() == "outside.txt" {
			t.Error("outside.txt should not exist in parent directory")
		}
	}
}

func TestLocalBackend_ValidPaths(t *testing.T) {
	dir := t.TempDir()
	b, err := NewLocalBackend(dir)
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}

	ctx := context.Background()
	data := []byte("hello world")

	// Normal file
	n, err := b.Put(ctx, "images/test.jpg", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n != int64(len(data)) {
		t.Errorf("Put wrote %d bytes, want %d", n, len(data))
	}

	// Check exists
	exists, err := b.Exists(ctx, "images/test.jpg")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("file should exist")
	}

	// Read back
	rc, err := b.Get(ctx, "images/test.jpg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	buf.ReadFrom(rc)
	if buf.String() != "hello world" {
		t.Errorf("Got %q, want %q", buf.String(), "hello world")
	}

	// Size
	sz, err := b.Size(ctx, "images/test.jpg")
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if sz != int64(len(data)) {
		t.Errorf("Size = %d, want %d", sz, len(data))
	}

	// Delete
	if err := b.Delete(ctx, "images/test.jpg"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	exists, _ = b.Exists(ctx, "images/test.jpg")
	if exists {
		t.Error("file should not exist after delete")
	}
}

func TestLocalBackend_AbsoluteBasePath(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.Base(dir)
	// Change to parent to test relative resolution
	oldWd, _ := os.Getwd()
	os.Chdir(filepath.Dir(dir))
	defer os.Chdir(oldWd)

	b, err := NewLocalBackend(rel)
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	if !filepath.IsAbs(b.basePath) {
		t.Errorf("basePath should be absolute, got %q", b.basePath)
	}
}

func TestLocalBackend_PutFileAtomicCommit(t *testing.T) {
	dir := t.TempDir()
	b, err := NewLocalBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	stagingDir := filepath.Join(dir, ".tmp")
	if err := os.MkdirAll(stagingDir, 0o750); err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(stagingDir, "upload-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data := []byte("atomic upload")
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	source := f.Name()
	if _, err := b.PutFile(context.Background(), "ab/cd/image.jpg", f, int64(len(data))); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("staging file still exists: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "ab/cd/image.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("committed data = %q", got)
	}

	external, err := os.CreateTemp(t.TempDir(), "upload-*")
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	if _, err := b.PutFile(context.Background(), "outside.jpg", external, 0); !errors.Is(err, ErrFileCommitUnsupported) {
		t.Fatalf("external staging error = %v", err)
	}
}

func TestLocalBackendSymlinkConfinement(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	b, err := NewLocalBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	if err := os.WriteFile(filepath.Join(outside, "marker"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if rc, err := b.Get(ctx, "escape/marker"); err == nil {
		rc.Close()
		t.Fatal("read escaped root")
	}
	if rc, err := b.GetRange(ctx, "escape/marker", 0, 1); err == nil {
		rc.Close()
		t.Fatal("range escaped root")
	}
	if _, err := b.Put(ctx, "escape/marker", strings.NewReader("changed"), 7); err == nil {
		t.Fatal("write escaped root")
	}
	if err := b.Delete(ctx, "escape/marker"); err == nil {
		t.Fatal("delete escaped root")
	}
	if _, err := b.Size(ctx, "escape/marker"); err == nil {
		t.Fatal("stat escaped root")
	}
	f, err := os.CreateTemp(root, "staging-")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("validated"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PutFile(ctx, "escape/marker", f, 9); err == nil {
		t.Fatal("staging commit escaped root")
	}
	if err := os.Remove(f.Name()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "marker"), f.Name()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PutFile(ctx, "published", f, 9); err == nil {
		t.Fatal("substituted staging file accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "published")); !os.IsNotExist(err) {
		t.Fatal("substituted staging file published")
	}
	got, err := os.ReadFile(filepath.Join(outside, "marker"))
	if err != nil || string(got) != "outside" {
		t.Fatal("outside file changed")
	}
}

func TestLocalBackendDirectorySwapRace(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	b, err := NewLocalBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := os.Mkdir(filepath.Join(root, "slot"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "marker"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Rename(filepath.Join(root, "slot"), filepath.Join(root, "held"))
			_ = os.Symlink(outside, filepath.Join(root, "slot"))
			_ = os.Remove(filepath.Join(root, "slot"))
			_ = os.Rename(filepath.Join(root, "held"), filepath.Join(root, "slot"))
		}
	})
	for range 100 {
		if rc, err := b.Get(t.Context(), "slot/marker"); err == nil {
			data, _ := io.ReadAll(rc)
			rc.Close()
			if string(data) == "outside" {
				t.Error("read escaped during swap")
			}
		}
		_, _ = b.Put(t.Context(), "slot/marker", strings.NewReader("inside"), 6)
		_ = b.Delete(t.Context(), "slot/marker")
	}
	close(stop)
	wg.Wait()
	data, err := os.ReadFile(filepath.Join(outside, "marker"))
	if err != nil || string(data) != "outside" {
		t.Fatal("outside bytes changed during swap")
	}
}
