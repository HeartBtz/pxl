//go:build integration

package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HeartBtz/pxl/internal/config"
	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/HeartBtz/pxl/internal/imaging"
	"github.com/HeartBtz/pxl/internal/middleware"
	"github.com/HeartBtz/pxl/internal/repository"
	"github.com/HeartBtz/pxl/internal/service"
	"github.com/HeartBtz/pxl/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type auditFixture struct {
	db       *repository.DB
	cfg      *config.Config
	users    *repository.UserRepository
	images   *repository.ImageRepository
	albums   *repository.AlbumRepository
	auth     *service.AuthService
	imageSvc *service.ImageService
	albumSvc *service.AlbumService
	store    *storage.LocalBackend
	log      *slog.Logger
}

func newAuditFixture(t *testing.T) *auditFixture {
	t.Helper()
	if os.Getenv("PXL_AUDIT_TEST_DB") != "1" {
		t.Skip("requires the isolated audit PostgreSQL socket; no external DSN is accepted")
	}
	// Deliberately no config.Load, dotenv, network host or externally supplied DSN.
	const socketDSN = "host=/tmp/opencode/pxl-audit-db user=postgres sslmode=disable connect_timeout=2 dbname="
	admin, err := sql.Open("postgres", socketDSN+"postgres")
	if err != nil {
		t.Fatal(err)
	}
	name := "pxl_audit_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		admin.Close()
		t.Fatalf("isolated database creation failed: %v", err)
	}
	db, err := repository.NewDB(socketDSN+name, 12, 2, time.Minute)
	if err != nil {
		t.Fatal("isolated database connection failed")
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec("DROP DATABASE " + name + " WITH (FORCE)"); err != nil {
			t.Error("disposable database cleanup failed")
		}
		admin.Close()
	})
	if err := db.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := db.RunMigrations(); err != nil {
		t.Fatal("migration replay failed")
	}
	root := t.TempDir()
	store, err := storage.NewLocalBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := &config.Config{
		Server:    config.ServerConfig{BaseURL: "https://pxl.example"},
		Auth:      config.AuthConfig{JWTSecret: strings.Repeat("fixture-only-", 4), JWTExpiry: time.Minute, RefreshExpiry: time.Hour, DefaultQuotaBytes: 1 << 20, DefaultQuotaImages: 10},
		Upload:    config.UploadConfig{MaxSizeBytes: 1 << 20, MaxFiles: 2, MaxPixels: 1000, TempDir: root, AllowedTypes: []string{"image/png"}},
		Thumbnail: config.ThumbnailConfig{Size: 32},
	}
	f := &auditFixture{db: db, cfg: cfg, store: store, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	f.users = repository.NewUserRepository(db)
	f.images = repository.NewImageRepository(db)
	f.albums = repository.NewAlbumRepository(db)
	f.auth = service.NewAuthService(f.users, repository.NewRefreshTokenRepository(db), cfg, f.log)
	f.imageSvc = service.NewImageService(f.images, f.users, store, imaging.NewProcessor(85), cfg, f.log)
	f.albumSvc = service.NewAlbumService(f.albums, f.images)
	return f
}

func (f *auditFixture) user(t *testing.T, name string) *domain.User {
	t.Helper()
	u, err := f.auth.Register(t.Context(), &domain.CreateUserRequest{Username: name, Email: name + "@example.test", Password: "fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (f *auditFixture) login(t *testing.T, u *domain.User) *domain.AuthRefreshResponse {
	t.Helper()
	r, err := f.auth.Login(t.Context(), &domain.AuthLoginRequest{Username: u.Username, Password: "fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func fixturePNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	m.Set(0, 0, color.NRGBA{R: shade, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, m); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestAuditSessionRevocation(t *testing.T) {
	f := newAuditFixture(t)
	u := f.user(t, "owner")
	login := f.login(t, u)
	if _, _, err := f.auth.ValidateJWTUser(t.Context(), login.Token); err != nil {
		t.Fatal(err)
	}
	if err := f.auth.Logout(t.Context(), u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.auth.ValidateJWTUser(t.Context(), login.Token); err == nil {
		t.Fatal("logged-out JWT accepted")
	}
	if _, err := f.auth.RefreshJWT(t.Context(), login.RefreshToken); err == nil {
		t.Fatal("logged-out refresh accepted")
	}

	login = f.login(t, u)
	key, err := f.auth.GenerateAPIKey(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A failing refresh revocation must roll back the password AND version update.
	_, err = f.db.Exec(`CREATE FUNCTION reject_revoke() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$;
		CREATE TRIGGER reject_revoke BEFORE UPDATE ON refresh_tokens FOR EACH ROW EXECUTE FUNCTION reject_revoke()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.auth.ChangePassword(t.Context(), u.ID, "fixture-password", "replacement-password"); err == nil {
		t.Fatal("revocation failure ignored")
	}
	if _, _, err := f.auth.ValidateJWTUser(t.Context(), login.Token); err != nil {
		t.Fatal("password update was not rolled back")
	}
	if _, err := f.db.Exec("DROP TRIGGER reject_revoke ON refresh_tokens"); err != nil {
		t.Fatal(err)
	}
	if err := f.auth.ChangePassword(t.Context(), u.ID, "fixture-password", "replacement-password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.auth.ValidateJWTUser(t.Context(), login.Token); err == nil {
		t.Fatal("old password JWT accepted")
	}
	if _, err := f.auth.RefreshJWT(t.Context(), login.RefreshToken); err == nil {
		t.Fatal("old password refresh accepted")
	}
	if _, err := f.auth.ValidateAPIKey(t.Context(), key); err == nil {
		t.Fatal("old API key survived password change")
	}
}

func TestAuditRefreshRaces(t *testing.T) {
	f := newAuditFixture(t)
	u := f.user(t, "owner")
	login := f.login(t, u)
	var wg sync.WaitGroup
	results := make(chan *domain.AuthRefreshResponse, 8)
	for range 8 {
		wg.Go(func() {
			if r, err := f.auth.RefreshJWT(t.Context(), login.RefreshToken); err == nil {
				results <- r
			}
		})
	}
	wg.Wait()
	close(results)
	if len(results) != 1 {
		t.Fatalf("refresh winners=%d", len(results))
	}
	// Race a fresh rotation with logout; either order must leave no valid descendant.
	for range 8 {
		login := f.login(t, u)
		var rotated *domain.AuthRefreshResponse
		var logoutErr error
		wg.Go(func() { rotated, _ = f.auth.RefreshJWT(t.Context(), login.RefreshToken) })
		wg.Go(func() { logoutErr = f.auth.Logout(t.Context(), u.ID) })
		wg.Wait()
		if logoutErr != nil {
			t.Fatal(logoutErr)
		}
		if rotated != nil {
			if _, _, err := f.auth.ValidateJWTUser(t.Context(), rotated.Token); err == nil {
				t.Fatal("rotation escaped logout")
			}
			if _, err := f.auth.RefreshJWT(t.Context(), rotated.RefreshToken); err == nil {
				t.Fatal("descendant escaped logout")
			}
		}
	}
}

func TestAuditPrivateRoutesAndAlbums(t *testing.T) {
	f := newAuditFixture(t)
	u, other := f.user(t, "owner"), f.user(t, "other")
	ownerLogin, otherLogin := f.login(t, u), f.login(t, other)
	upload, err := f.imageSvc.Upload(t.Context(), &u.ID, "private-name.png", fixturePNG(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE images SET is_private=true WHERE short_id=$1", upload.ShortID); err != nil {
		t.Fatal(err)
	}
	album, err := f.albumSvc.Create(t.Context(), u.ID, &domain.AlbumRequest{Title: "fixture", ImageIDs: []string{upload.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(album.Images) != 1 {
		t.Fatal("album SQL failed to load images")
	}
	ih := NewImageHandler(f.imageSvc, f.albumSvc, f.cfg, f.log)
	t.Cleanup(ih.Stop)
	wh := NewWebHandler(f.imageSvc, f.albumSvc, f.auth, f.cfg, f.log)
	ah := NewAlbumHandler(f.albumSvc, f.imageSvc, f.log)
	r := chi.NewRouter()
	r.Use(middleware.SecurityHeaders, middleware.OptionalAuth(f.auth))
	r.Get("/v/{shortID}", wh.ViewPage)
	r.Get("/i/{shortID}", ih.ServeImage)
	r.Get("/t/{shortID}", ih.ServeThumbnail)
	r.Get("/api/v1/images/{shortID}", ih.GetInfo)
	r.Get("/api/v1/albums/{shortID}", ah.Get)
	for _, prefix := range []string{"/v/", "/i/", "/t/", "/api/v1/images/"} {
		for _, token := range []string{"", otherLogin.Token, ownerLogin.Token} {
			req := httptest.NewRequest("GET", prefix+upload.ShortID, nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			want := 404
			if token == ownerLogin.Token {
				want = 200
			}
			if rr.Code != want {
				t.Fatalf("%s status=%d want=%d", prefix, rr.Code, want)
			}
			if !strings.Contains(rr.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("private response cacheable")
			}
		}
	}
	for _, token := range []string{"", otherLogin.Token, ownerLogin.Token} {
		req := httptest.NewRequest("GET", "/api/v1/albums/"+album.ShortID, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		var body struct {
			Images []domain.ImageInfoResponse `json:"images"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		want := 0
		if token == ownerLogin.Token {
			want = 1
		}
		if len(body.Images) != want || rr.Code != 200 {
			t.Fatal("public album leaked private metadata or required login")
		}
	}
	if _, err := f.db.Exec("UPDATE albums SET is_private=true WHERE id=$1", album.ID); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/albums/"+album.ShortID, nil))
	if rr.Code != 404 {
		t.Fatal("private album exposed")
	}
	if _, err := f.db.Exec("UPDATE images SET is_private=false,burn_after_view=true WHERE short_id=$1", upload.ShortID); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/t/"+upload.ShortID, nil))
	if rr.Code != 404 {
		t.Fatal("non-burning thumbnail exposed")
	}
}

func TestAuditUploadsQuotasAndCompensation(t *testing.T) {
	f := newAuditFixture(t)
	u := f.user(t, "owner")
	if _, err := f.db.Exec("UPDATE users SET quota_images=1 WHERE id=$1", u.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := range 4 {
		data := fixturePNG(t, uint8(i))
		wg.Go(func() { _, err := f.imageSvc.Upload(t.Context(), &u.ID, "file.png", data); errs <- err })
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, domain.ErrQuotaImagesExceeded) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("quota winners=%d", success)
	}
	var files int
	if err := filepath.WalkDir(f.cfg.Upload.TempDir, func(_ string, d os.DirEntry, err error) error {
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
		t.Fatalf("quota rollback left %d storage files", files)
	}
	first, err := f.imageSvc.Upload(t.Context(), nil, "anonymous.png", fixturePNG(t, 99))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.imageSvc.Upload(t.Context(), nil, "anonymous.png", fixturePNG(t, 99))
	if err != nil {
		t.Fatal(err)
	}
	if first.ShortID == second.ShortID || second.DeleteToken == "" {
		t.Fatal("anonymous dedup shared another uploader's capability")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.imageSvc.Upload(ctx, nil, "cancelled.png", fixturePNG(t, 98)); err == nil {
		t.Fatal("cancelled upload succeeded")
	}
	// A deferred constraint failure occurs at COMMIT, after object storage succeeds.
	_, err = f.db.Exec(`CREATE FUNCTION fail_image_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$;
		CREATE CONSTRAINT TRIGGER fail_image_commit AFTER INSERT ON images DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_image_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE users SET quota_images=10 WHERE id=$1", u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.imageSvc.Upload(t.Context(), &u.ID, "commit-fail.png", fixturePNG(t, 97)); err == nil {
		t.Fatal("commit failure ignored")
	}
	current, err := f.users.GetByID(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.UsedImages != 1 {
		t.Fatal("failed commit charged quota")
	}
	files = 0
	if err := filepath.WalkDir(f.cfg.Upload.TempDir, func(_ string, d os.DirEntry, err error) error {
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
	if files != 3 {
		t.Fatalf("failed commit left %d files, want 3", files)
	}
}

func TestAuditLastAdminRace(t *testing.T) {
	f := newAuditFixture(t)
	a, b := f.user(t, "admin_a"), f.user(t, "admin_b")
	if _, err := f.db.Exec("UPDATE users SET role='admin'"); err != nil {
		t.Fatal(err)
	}
	role := "user"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []uuid.UUID{a.ID, b.ID} {
		wg.Go(func() { errs <- f.users.AdminUpdate(t.Context(), id, &domain.AdminUpdateUserRequest{Role: &role}) })
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, domain.ErrForbidden) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("concurrent admin changes removed last admin")
	}
}

func TestAuditPartialUploadAndHTTPFailures(t *testing.T) {
	f := newAuditFixture(t)
	ih := NewImageHandler(f.imageSvc, f.albumSvc, f.cfg, f.log)
	t.Cleanup(ih.Stop)
	var body bytes.Buffer
	mp := multipart.NewWriter(&body)
	for i := range 3 {
		part, err := mp.CreateFormFile("file", "fixture.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(fixturePNG(t, uint8(i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := mp.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/upload", &body)
	req.Header.Set("Content-Type", mp.FormDataContentType())
	rr := httptest.NewRecorder()
	ih.Upload(rr, req)
	var result struct {
		Images []domain.UploadResponse `json:"images"`
		Error  string                  `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 400 || len(result.Images) != 2 || result.Images[0].DeleteToken == "" {
		t.Fatal("partial upload lost committed capabilities")
	}
	r := chi.NewRouter()
	r.Use(middleware.SecurityHeaders)
	r.Get("/i/{shortID}", ih.ServeImage)
	r.Head("/i/{shortID}", ih.ServeImage)
	r.Get("/t/{shortID}", ih.ServeThumbnail)
	upload := result.Images[0]
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/t/"+upload.ShortID, nil))
	if rr.Code != 200 || !strings.Contains(rr.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("pending thumbnail fallback cached as immutable")
	}
	req = httptest.NewRequest("GET", "/i/"+upload.ShortID, nil)
	req.Header.Set("Range", "bytes=0-3")
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 206 || rr.Body.Len() != 4 {
		t.Fatal("range contract broken")
	}
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("HEAD", "/i/"+upload.ShortID, nil))
	if rr.Code != 200 || rr.Body.Len() != 0 {
		t.Fatal("HEAD contract broken")
	}
	if _, err := f.db.Exec("UPDATE images SET expires_at=NOW()+INTERVAL '1 hour' WHERE short_id=$1", upload.ShortID); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/i/"+upload.ShortID, nil))
	if !strings.Contains(rr.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("expiring image cached beyond expiry")
	}
	img, err := f.images.GetByShortID(t.Context(), upload.ShortID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Delete(t.Context(), img.StoragePath); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", "/i/"+upload.ShortID, nil)
	req.Header.Set("Range", "bytes=0-3")
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 500 || rr.Header().Get("Content-Length") != "" || rr.Header().Get("Content-Range") != "" || rr.Header().Get("ETag") != "" {
		t.Fatal("storage error inherited binary response headers")
	}
}

func TestAuditRoleSuspensionCookiesAndLegacyDeleteToken(t *testing.T) {
	f := newAuditFixture(t)
	u := f.user(t, "owner")
	login := f.login(t, u)
	role := "admin"
	if err := f.users.AdminUpdate(t.Context(), u.ID, &domain.AdminUpdateUserRequest{Role: &role}); err != nil {
		t.Fatal(err)
	}
	if _, current, err := f.auth.ValidateJWTUser(t.Context(), login.Token); err != nil || current.Role != "admin" {
		t.Fatal("role not resolved from current DB state")
	}
	// A second admin allows suspension without violating the last-admin guard.
	other := f.user(t, "second_admin")
	if err := f.users.AdminUpdate(t.Context(), other.ID, &domain.AdminUpdateUserRequest{Role: &role}); err != nil {
		t.Fatal(err)
	}
	if err := f.auth.SetUserActive(t.Context(), u.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := f.auth.SetUserActive(t.Context(), u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.auth.ValidateJWTUser(t.Context(), login.Token); err == nil {
		t.Fatal("suspended JWT resurrected")
	}
	if _, err := f.auth.RefreshJWT(t.Context(), login.RefreshToken); err == nil {
		t.Fatal("suspended refresh resurrected")
	}
	ah := NewAuthHandler(f.auth, service.NewAuditService(repository.NewAuditRepository(f.db), f.log), f.cfg, f.log)
	rr := httptest.NewRecorder()
	ah.BrowserLogin(rr, httptest.NewRequest("POST", "/api/v1/auth/browser-login", strings.NewReader(`{"username":"owner","password":"fixture-password"}`)))
	if rr.Code != 200 {
		t.Fatal("browser login failed")
	}
	if strings.Contains(rr.Body.String(), "refresh_token") || strings.Contains(rr.Body.String(), `"token"`) {
		t.Fatal("browser credentials exposed to JavaScript")
	}
	if len(rr.Result().Cookies()) != 2 {
		t.Fatal("browser cookies missing")
	}
	for _, cookie := range rr.Result().Cookies() {
		if !cookie.HttpOnly || !cookie.Secure || cookie.Domain != "" || cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatal("unsafe browser cookie")
		}
	}
	up, err := f.imageSvc.Upload(t.Context(), nil, "legacy.png", fixturePNG(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	// Replay the shipped hash migration on a synthetic pre-hardening capability.
	if _, err := f.db.Exec("UPDATE images SET delete_token=$1 WHERE short_id=$2", up.DeleteToken, up.ShortID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("DELETE FROM schema_migrations WHERE version=3"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	img, err := f.images.GetByShortID(t.Context(), up.ShortID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.imageSvc.Delete(t.Context(), up.ShortID, nil, img.DeleteToken); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("stored digest accepted as a capability")
	}
	if err := f.imageSvc.Delete(t.Context(), up.ShortID, nil, up.DeleteToken); err != nil {
		t.Fatal("legacy deletion capability broken by migration")
	}
}
