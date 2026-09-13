// Package service contient la logique métier de PXL :
//   - ImageService : upload, validation MIME, déduplication SHA-256, stockage
//   - AuthService  : authentification JWT, gestion des utilisateurs
//   - AlbumService : gestion des albums d'images
//
// Les services orchestrent les repositories et le stockage, et appliquent
// les règles métier (quotas, validations, déduplication, burn-after-view).
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/HeartBtz/pxl/internal/config"
	"github.com/HeartBtz/pxl/internal/crypto"
	"github.com/HeartBtz/pxl/internal/domain"
	imgproc "github.com/HeartBtz/pxl/internal/imaging"
	"github.com/HeartBtz/pxl/internal/metrics"
	"github.com/HeartBtz/pxl/internal/repository"
	"github.com/HeartBtz/pxl/internal/storage"
	"github.com/google/uuid"
	_ "golang.org/x/image/webp"
)

// ThumbQueuer is satisfied by the thumbnail worker.
type ThumbQueuer interface {
	Queue(img *domain.Image)
}

type ImageService struct {
	imageRepo *repository.ImageRepository
	userRepo  *repository.UserRepository
	store     storage.Backend
	processor *imgproc.Processor
	thumbQ    ThumbQueuer
	cfg       *config.Config
	log       *slog.Logger
	spaceMu   sync.Mutex
	inFlight  int64
}

func NewImageService(
	ir *repository.ImageRepository,
	ur *repository.UserRepository,
	store storage.Backend,
	proc *imgproc.Processor,
	cfg *config.Config,
	logger *slog.Logger,
) *ImageService {
	return &ImageService{imageRepo: ir, userRepo: ur, store: store, processor: proc, cfg: cfg, log: logger}
}

// SetThumbQueuer is called after thumbnail worker is created to break circular deps.
func (s *ImageService) SetThumbQueuer(tq ThumbQueuer) { s.thumbQ = tq }

// Upload processes a single image upload.
func (s *ImageService) Upload(ctx context.Context, userID *uuid.UUID, filename string, data []byte) (*domain.UploadResponse, error) {
	return s.UploadReader(ctx, userID, filename, bytes.NewReader(data))
}

// UploadReader streams an upload to a private temporary file. This keeps
// memory usage bounded even when many clients upload large images at once.
func (s *ImageService) UploadReader(ctx context.Context, userID *uuid.UUID, filename string, src io.Reader) (*domain.UploadResponse, error) {
	releaseSpace, err := s.reserveUploadSpace()
	if err != nil {
		return nil, err
	}
	defer releaseSpace()
	tmp, err := os.CreateTemp(s.cfg.Upload.TempDir, "pxl-upload-*")
	if err != nil {
		return nil, fmt.Errorf("create upload buffer: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	hasher := sha256.New()
	limited := io.LimitReader(src, s.cfg.Upload.MaxSizeBytes+1)
	written, err := io.CopyBuffer(io.MultiWriter(tmp, hasher), limited, make([]byte, 128*1024))
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}
	if written > s.cfg.Upload.MaxSizeBytes {
		return nil, domain.ErrFileTooLarge
	}
	if written == 0 {
		return nil, domain.ErrInvalidImage
	}

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek upload: %w", err)
	}
	header := make([]byte, 512)
	n, readErr := io.ReadFull(tmp, header)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read image header: %w", readErr)
	}
	detectedMIME := http.DetectContentType(header[:n])
	if !s.isAllowed(detectedMIME) {
		return nil, domain.ErrBlockedType
	}

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek upload: %w", err)
	}
	imageCfg, _, err := image.DecodeConfig(tmp)
	if err != nil {
		return nil, domain.ErrInvalidImage
	}
	if !validDimensions(imageCfg.Width, imageCfg.Height, s.cfg.Upload.MaxPixels) {
		return nil, domain.ErrInvalidImage
	}

	sha := hex.EncodeToString(hasher.Sum(nil))

	// 4. Dedup check
	if userID != nil {
		existing, err := s.imageRepo.GetBySHA256(ctx, sha, userID)
		if err != nil && !errors.Is(err, domain.ErrImageNotFound) {
			return nil, err
		}
		if err == nil && existing != nil {
			// Delete credentials are deliberately one-time output. Owners can still
			// delete deduplicated images with their authenticated session.
			return s.buildResponse(existing), nil
		}
	}

	// 5. Reject obvious quota failures before touching storage. The repository
	// repeats this check under a row lock when it commits, so concurrent uploads
	// still cannot oversubscribe the account.
	if userID != nil {
		u, err := s.userRepo.GetByID(ctx, *userID)
		if err != nil {
			return nil, err
		}
		if u.QuotaRemaining() < written {
			return nil, domain.ErrQuotaExceeded
		}
		if u.QuotaImagesRemaining() <= 0 {
			return nil, domain.ErrQuotaImagesExceeded
		}
	}

	// 6. IDs (with collision retry)
	var shortID string
	for attempt := 0; attempt < 3; attempt++ {
		shortID = crypto.GenerateShortID(8)
		if _, err := s.imageRepo.GetByShortID(ctx, shortID); errors.Is(err, domain.ErrImageNotFound) {
			break // Not found = no collision
		} else if err != nil {
			return nil, err
		}
		if attempt == 2 {
			return nil, fmt.Errorf("short ID collision after 3 attempts")
		}
	}
	deleteToken := crypto.GenerateDeleteToken()
	ext := crypto.ExtensionFromMIME(detectedMIME)
	storedName := shortID + ext
	// Storage keys are independent of public short IDs, including deleted rows.
	// A short-ID collision must never overwrite another object's bytes.
	storagePath := generateStoragePath(shortID, uuid.NewString()+ext)

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek upload: %w", err)
	}
	// 7. On local storage, commit a staging file on the same filesystem with
	// one atomic rename. S3 and cross-filesystem configurations retain the
	// streaming Put fallback.
	var storeErr error
	if fp, ok := s.store.(storage.FilePutter); ok {
		_, storeErr = fp.PutFile(ctx, storagePath, tmp, written)
		if errors.Is(storeErr, storage.ErrFileCommitUnsupported) {
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return nil, fmt.Errorf("seek upload fallback: %w", err)
			}
			_, storeErr = s.store.Put(ctx, storagePath, tmp, written)
		}
	} else {
		_, storeErr = s.store.Put(ctx, storagePath, tmp, written)
	}
	if storeErr != nil {
		return nil, fmt.Errorf("storage put: %w", storeErr)
	}

	// 8. DB record and owner quota reservation are committed atomically.
	img := &domain.Image{
		ShortID:      shortID,
		UserID:       userID,
		OriginalName: sanitizeFilename(filename),
		StoredName:   storedName,
		StoragePath:  storagePath,
		MIMEType:     detectedMIME,
		Extension:    ext,
		SizeBytes:    written,
		Width:        imageCfg.Width,
		Height:       imageCfg.Height,
		SHA256:       sha,
		DeleteToken:  hashDeleteToken(deleteToken),
	}
	if err := s.imageRepo.CreateWithQuota(ctx, img); err != nil {
		// Cleanup storage on DB failure
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		// A lost COMMIT response may still be in flight on the primary. Even a
		// fresh read can see no row before it finishes: never compensate blindly.
		if errors.Is(err, repository.ErrCommitUncertain) {
			s.log.Warn("upload commit uncertain; retained object for reconciliation", slog.String("path", storagePath), slog.String("image_id", img.ID.String()))
		} else {
			if cleanupErr := s.store.Delete(cleanupCtx, storagePath); cleanupErr != nil {
				s.log.Warn("upload compensation failed", slog.String("path", storagePath))
			}
		}
		return nil, fmt.Errorf("create image: %w", err)
	}

	// 9. Queue thumbnail
	if s.thumbQ != nil && s.cfg.Thumbnail.Enabled {
		s.thumbQ.Queue(img)
	}

	resp := s.buildResponse(img)
	resp.DeleteToken = deleteToken
	resp.DeleteURL = s.deleteURL(shortID, deleteToken)

	// Metrics
	metrics.UploadsTotal.Inc()
	metrics.UploadBytesTotal.Add(float64(written))

	return resp, nil
}

func validDimensions(width, height int, maxPixels int64) bool {
	return width > 0 && height > 0 && maxPixels > 0 && int64(width) <= maxPixels/int64(height)
}

// GetByShortID retrieves an image, handling expiration and burn-after-view.
func (s *ImageService) GetByShortID(ctx context.Context, shortID string) (*domain.Image, error) {
	img, err := s.imageRepo.GetByShortID(ctx, shortID)
	if err != nil {
		return nil, err
	}
	if img.ExpiresAt != nil && img.ExpiresAt.Before(time.Now()) {
		return nil, domain.ErrImageExpired
	}
	return img, nil
}

// GetByShortIDForUser retrieves an image with privacy enforcement.
// Public images are returned to anyone; private images only to the owner.
func (s *ImageService) GetByShortIDForUser(ctx context.Context, shortID string, userID *uuid.UUID) (*domain.Image, error) {
	img, err := s.GetByShortID(ctx, shortID)
	if err != nil {
		return nil, err
	}
	if img.IsPrivate {
		if userID == nil || img.UserID == nil || *userID != *img.UserID {
			return nil, domain.ErrImageNotFound
		}
	}
	return img, nil
}

// ServeRaw returns the raw image reader from storage.
func (s *ImageService) ServeRaw(ctx context.Context, img *domain.Image) (io.ReadCloser, error) {
	return s.store.Get(ctx, img.StoragePath)
}

// ServeRange returns a range of bytes for the image.
func (s *ImageService) ServeRange(ctx context.Context, img *domain.Image, offset, length int64) (io.ReadCloser, error) {
	return s.store.GetRange(ctx, img.StoragePath, offset, length)
}

// ServeThumbnail returns the thumbnail reader.
func (s *ImageService) ServeThumbnail(ctx context.Context, img *domain.Image, size int) (io.ReadCloser, string, int64, bool, error) {
	thumbPath := thumbStoragePath(img.StoragePath, size)
	rc, err := s.store.Get(ctx, thumbPath)
	if err != nil {
		// Fallback to original
		rc2, err2 := s.store.Get(ctx, img.StoragePath)
		return rc2, img.MIMEType, img.SizeBytes, false, err2
	}
	// Thumbnail MIME: same as original for png, jpeg for others
	mime := "image/jpeg"
	if img.MIMEType == "image/png" || img.MIMEType == "image/gif" {
		mime = "image/png"
	}
	thumbSize, sizeErr := s.store.Size(ctx, thumbPath)
	if sizeErr != nil {
		_ = rc.Close()
		return nil, "", 0, false, sizeErr
	}
	return rc, mime, thumbSize, true, nil
}

// IncrementViews increments view count and handles burn-after-view atomically.
func (s *ImageService) IncrementViews(ctx context.Context, img *domain.Image) error {
	if err := s.imageRepo.IncrementViews(ctx, img.ID); err != nil {
		return err
	}
	metrics.ImageViews.Inc()

	if img.BurnAfterView {
		// Atomic burn: soft-delete only if view_count >= 1 after increment
		deleted, err := s.imageRepo.BurnAfterViewDelete(ctx, img.ID)
		if err != nil {
			return err
		}
		if deleted {
			s.log.Info("burn-after-view: image deleted", slog.String("short_id", img.ShortID))
		}
	}
	return nil
}

func (s *ImageService) IncrementViewsBatch(ctx context.Context, counts map[uuid.UUID]int64) error {
	if err := s.imageRepo.IncrementViewsBatch(ctx, counts); err != nil {
		return err
	}
	var total int64
	for _, count := range counts {
		total += count
	}
	metrics.ImageViews.Add(float64(total))
	return nil
}

// ClaimBurnView atomically increments view count and marks a burn-after-view
// image as deleted. Returns true if this request claimed the view.
// This prevents the race condition where multiple concurrent requests
// can all serve the image before any burn triggers.
func (s *ImageService) ClaimBurnView(ctx context.Context, img *domain.Image) (bool, error) {
	deleted, err := s.imageRepo.ClaimBurnView(ctx, img)
	if err != nil {
		return false, err
	}
	if deleted {
		metrics.ImageViews.Inc()
		s.log.Info("burn-after-view: image claimed and deleted", slog.String("short_id", img.ShortID))
	}
	return deleted, nil
}

// FinalizeBurn removes the bytes of an already claimed burn-after-view image.
// Its database row remains soft-deleted for auditability.
func (s *ImageService) FinalizeBurn(ctx context.Context, img *domain.Image) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.store.Delete(cleanupCtx, img.StoragePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Warn("burn-after-view: failed to delete original", slog.String("path", img.StoragePath), slog.String("error", err.Error()))
	}
	thumbPath := thumbStoragePath(img.StoragePath, s.cfg.Thumbnail.Size)
	if err := s.store.Delete(cleanupCtx, thumbPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Debug("burn-after-view: failed to delete thumbnail", slog.String("path", thumbPath), slog.String("error", err.Error()))
	}
}

// DeleteFiles removes an image and its thumbnail from storage. Database state
// is handled separately so cleanup can safely retry storage failures.
func (s *ImageService) DeleteFiles(ctx context.Context, img *domain.Image) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.store.Delete(ctx, img.StoragePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Warn("failed to delete image file", slog.String("path", img.StoragePath), slog.String("error", err.Error()))
	}
	thumbPath := thumbStoragePath(img.StoragePath, s.cfg.Thumbnail.Size)
	if err := s.store.Delete(ctx, thumbPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Debug("failed to delete thumbnail", slog.String("path", thumbPath), slog.String("error", err.Error()))
	}
}

// ListByUser returns paginated images for a user.
func (s *ImageService) ListByUser(ctx context.Context, userID uuid.UUID, page, perPage int) ([]*domain.Image, int64, error) {
	return s.imageRepo.ListByUser(ctx, userID, page, perPage)
}

// Delete removes an image by owner or delete token.
func (s *ImageService) Delete(ctx context.Context, shortID string, userID *uuid.UUID, deleteToken string) error {
	img, err := s.imageRepo.GetByShortID(ctx, shortID)
	if err != nil {
		return err
	}

	authorized := false
	if deleteToken != "" {
		if strings.HasPrefix(img.DeleteToken, "sha256:") {
			authorized = subtle.ConstantTimeCompare([]byte(img.DeleteToken), []byte(hashDeleteToken(deleteToken))) == 1
		} else {
			// Compatibility for rows created before delete-token hashing. These
			// should be migrated after deployment.
			authorized = subtle.ConstantTimeCompare([]byte(img.DeleteToken), []byte(deleteToken)) == 1
		}
	}
	if userID != nil && img.UserID != nil && *userID == *img.UserID {
		authorized = true
	}
	if !authorized {
		return domain.ErrForbidden
	}

	// Soft delete and quota release are one database transaction.
	if err := s.imageRepo.SoftDeleteWithQuota(ctx, img); err != nil {
		return err
	}

	// Each image has a unique storage path, even when another owner uploaded
	// identical bytes. Delete that path directly.
	s.DeleteFiles(ctx, img)

	return nil
}

// ForceDelete removes an image after authorization has already been enforced
// by the admin route. It avoids treating a stored credential as an input token.
func (s *ImageService) ForceDelete(ctx context.Context, img *domain.Image) error {
	if err := s.imageRepo.SoftDeleteWithQuota(ctx, img); err != nil {
		return err
	}
	s.DeleteFiles(ctx, img)
	return nil
}

// GetInfo builds a public info response for an image.
func (s *ImageService) GetInfo(img *domain.Image) *domain.ImageInfoResponse {
	base := s.cfg.Server.BaseURL
	resp := &domain.ImageInfoResponse{
		ID:           img.ID.String(),
		ShortID:      img.ShortID,
		URL:          fmt.Sprintf("%s/v/%s", base, img.ShortID),
		DirectURL:    fmt.Sprintf("%s/i/%s%s", base, img.ShortID, img.Extension),
		ThumbURL:     fmt.Sprintf("%s/t/%s%s", base, img.ShortID, img.Extension),
		OriginalName: img.OriginalName,
		MIMEType:     img.MIMEType,
		SizeBytes:    img.SizeBytes,
		Width:        img.Width,
		Height:       img.Height,
		SHA256:       img.SHA256,
		ViewCount:    img.ViewCount,
		CreatedAt:    img.CreatedAt.Format(time.RFC3339),
	}
	if img.ExpiresAt != nil {
		t := img.ExpiresAt.Format(time.RFC3339)
		resp.ExpiresAt = &t
	}
	return resp
}

// ── helpers ────────────────────────────────

func (s *ImageService) isAllowed(mime string) bool {
	for _, t := range s.cfg.Upload.AllowedTypes {
		if t == mime {
			return true
		}
	}
	return false
}

func (s *ImageService) buildResponse(img *domain.Image) *domain.UploadResponse {
	base := s.cfg.Server.BaseURL
	return &domain.UploadResponse{
		ID:        img.ID.String(),
		ShortID:   img.ShortID,
		URL:       fmt.Sprintf("%s/v/%s", base, img.ShortID),
		DirectURL: fmt.Sprintf("%s/i/%s%s", base, img.ShortID, img.Extension),
		ThumbURL:  fmt.Sprintf("%s/t/%s%s", base, img.ShortID, img.Extension),
		MIMEType:  img.MIMEType,
		Extension: img.Extension,
		SizeBytes: img.SizeBytes,
		Width:     img.Width,
		Height:    img.Height,
		SHA256:    img.SHA256,
	}
}

func (s *ImageService) deleteURL(shortID, _ string) string {
	return fmt.Sprintf("%s/api/v1/images/%s", s.cfg.Server.BaseURL, shortID)
}

func generateStoragePath(shortID, name string) string {
	// Distribute across 2-level directory tree: ab/cd/filename
	if len(shortID) < 4 {
		return name
	}
	return filepath.Join(shortID[0:2], shortID[2:4], name)
}

func thumbStoragePath(originalPath string, size int) string {
	ext := filepath.Ext(originalPath)
	base := strings.TrimSuffix(originalPath, ext)
	thumbExt := ext
	switch ext {
	case ".png", ".gif":
		thumbExt = ".png"
	default:
		thumbExt = ".jpg"
	}
	return fmt.Sprintf("%s_t%d%s", base, size, thumbExt)
}

func hashDeleteToken(token string) string {
	return "sha256:" + crypto.HashAPIKey(token)
}

func (s *ImageService) reserveUploadSpace() (func(), error) {
	s.spaceMu.Lock()
	defer s.spaceMu.Unlock()
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.cfg.Upload.TempDir, &stat); err != nil {
		return nil, fmt.Errorf("inspect upload capacity: %w", err)
	}
	if stat.Bsize <= 0 || stat.Bavail > uint64(^uint64(0)>>1)/uint64(stat.Bsize) {
		return nil, fmt.Errorf("upload capacity exceeds supported range")
	}
	available := int64(stat.Bavail * uint64(stat.Bsize)) // #nosec G115 -- product is bounded by MaxInt64 above
	reservation := s.cfg.Upload.MaxSizeBytes
	if reservation > available-s.cfg.Upload.MinFreeBytes-s.inFlight {
		return nil, domain.ErrInsufficientStorage
	}
	s.inFlight += reservation
	return func() {
		s.spaceMu.Lock()
		s.inFlight -= reservation
		s.spaceMu.Unlock()
	}, nil
}

func sanitizeFilename(name string) string {
	name = strings.ToValidUTF8(filepath.Base(name), "")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." {
		name = "image"
	}
	for len(name) > 255 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}
