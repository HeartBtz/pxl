// Package handler contient les handlers HTTP de PXL :
//   - ImageHandler : upload, affichage, suppression d'images
//   - AuthHandler  : authentification (login, register, clés API)
//   - AlbumHandler : gestion des albums
//   - WebHandler   : interface web (page d'upload, galerie, vue image)
//
// Tous les handlers utilisent le routeur chi v5 et renvoient du JSON
// sauf les routes /i/ et /t/ qui renvoient du binaire (images).
package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HeartBtz/pxl/internal/config"
	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/HeartBtz/pxl/internal/metrics"
	"github.com/HeartBtz/pxl/internal/middleware"
	"github.com/HeartBtz/pxl/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ImageHandler gère les endpoints liés aux images :
// upload (POST /api/v1/upload), affichage direct (/i/{shortID}),
// miniatures (/t/{shortID}), infos (GET /api/v1/images/{shortID}),
// listage, suppression par token ou par propriétaire.
type ImageHandler struct {
	svc         *service.ImageService
	albumSvc    *service.AlbumService
	cfg         *config.Config
	log         *slog.Logger
	viewsWorker chan uuid.UUID
	viewsCancel context.CancelFunc
	viewsDone   sync.WaitGroup
}

func NewImageHandler(svc *service.ImageService, albumSvc *service.AlbumService, cfg *config.Config, logger *slog.Logger) *ImageHandler {
	ih := &ImageHandler{
		svc:         svc,
		albumSvc:    albumSvc,
		cfg:         cfg,
		log:         logger,
		viewsWorker: make(chan uuid.UUID, 10000), // Buffer against traffic spikes
	}

	ctx, cancel := context.WithCancel(context.Background())
	ih.viewsCancel = cancel
	ih.viewsDone.Add(1)
	go ih.runViewsWorker(ctx)

	return ih
}

func (h *ImageHandler) runViewsWorker(ctx context.Context) {
	defer h.viewsDone.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	pending := make(map[uuid.UUID]int64, 512)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		batch := pending
		pending = make(map[uuid.UUID]int64, 512)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := h.svc.IncrementViewsBatch(ctx, batch); err != nil {
			h.log.Warn("view counter batch failed", slog.Int("images", len(batch)), slog.String("error", err.Error()))
		}
		cancel()
	}
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case id := <-h.viewsWorker:
					pending[id]++
				default:
					flush()
					return
				}
			}
		case id := <-h.viewsWorker:
			pending[id]++
			if len(pending) >= 512 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// Stop flushes buffered view counters during graceful shutdown.
func (h *ImageHandler) Stop() {
	h.viewsCancel()
	h.viewsDone.Wait()
}

// Upload handles POST /api/v1/upload
func (h *ImageHandler) Upload(w http.ResponseWriter, r *http.Request) {
	userID := getUserUUID(r)
	_, sessionErr := r.Cookie("pxl_session")
	_, refreshErr := r.Cookie("pxl_refresh")
	if userID == nil && (r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" || sessionErr == nil || refreshErr == nil) {
		writeError(w, http.StatusUnauthorized, "invalid upload credentials; login required")
		return
	}
	multipartReader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	results := make([]domain.UploadResponse, 0, 1)
	fail := func(status int, message string) {
		if len(results) > 0 {
			// Earlier parts are committed individually. Return their one-time
			// deletion capabilities even when a later part fails.
			writeJSON(w, status, map[string]any{"error": message, "images": results})
			return
		}
		writeError(w, status, message)
	}
	for {
		part, err := multipartReader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				fail(http.StatusRequestEntityTooLarge, "request too large")
			} else {
				fail(http.StatusBadRequest, "invalid multipart form")
			}
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		if len(results) >= h.cfg.Upload.MaxFiles {
			fail(http.StatusBadRequest, "too many files")
			return
		}

		filename := part.FileName()
		resp, err := h.svc.UploadReader(r.Context(), userID, filename, part)
		if err != nil {
			h.log.Error("upload failed", slog.String("filename", filename), slog.String("error", err.Error()))
			metrics.UploadErrors.Inc()
			status := mapErrorStatus(err)
			fail(status, safeErrorMessage(err))
			return
		}
		_ = part.Close()
		results = append(results, *resp)
	}
	if len(results) == 0 {
		writeError(w, http.StatusBadRequest, "no file provided")
		return
	}

	if len(results) == 1 {
		writeJSON(w, http.StatusCreated, results[0])
	} else {
		// Auto-create albums by default; bulk clients can opt out per request.
		var albumInfo map[string]string
		if r.URL.Query().Get("auto_album") != "false" && userID != nil && *userID != uuid.Nil && h.albumSvc != nil {
			imageIDs := make([]string, len(results))
			for i, r := range results {
				imageIDs[i] = r.ID
			}
			title := time.Now().Format("Album 02/01/2006 15:04")
			req := &domain.AlbumRequest{Title: title, ImageIDs: imageIDs}
			album, err := h.albumSvc.Create(r.Context(), *userID, req)
			if err != nil {
				h.log.Warn("auto-album creation failed", slog.String("error", err.Error()))
			} else {
				albumInfo = map[string]string{
					"id":       album.ID.String(),
					"short_id": album.ShortID,
					"title":    album.Title,
					"url":      h.cfg.Server.BaseURL + "/a/" + album.ShortID,
				}
			}
		}
		resp := map[string]any{"images": results}
		if albumInfo != nil {
			resp["album"] = albumInfo
		}
		writeJSON(w, http.StatusCreated, resp)
	}
}

// ServeImage handles GET /i/{shortID}.{ext} and GET /i/{shortID}
func (h *ImageHandler) ServeImage(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")
	if len(shortID) > 12 {
		http.NotFound(w, r)
		return
	}

	img, err := h.svc.GetByShortIDForUser(r.Context(), shortID, getUserUUID(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if img.IsPrivate || img.BurnAfterView || img.ExpiresAt != nil || len(w.Header().Values("Set-Cookie")) > 0 {
		w.Header().Set("Cache-Control", "private, no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}

	// Burn-after-view must be claimed before conditional or range responses;
	// otherwise a known ETag could produce a non-burning 304 response.
	if r.Method != http.MethodHead && img.BurnAfterView {
		claimed, err := h.svc.ClaimBurnView(r.Context(), img)
		if err != nil || !claimed {
			http.NotFound(w, r)
			return
		}
		defer h.svc.FinalizeBurn(context.Background(), img)
	}

	// ETag
	etag := `"` + img.SHA256 + `"`
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", img.MIMEType)
	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	offset, length, partial, rangeErr := parseSingleRange(r.Header.Get("Range"), img.SizeBytes)
	if r.Header.Get("If-Range") != "" && r.Header.Get("If-Range") != etag {
		offset, length, partial, rangeErr = 0, img.SizeBytes, false, nil
	}
	if rangeErr != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", img.SizeBytes))
		http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, img.SizeBytes))
	}

	// HEAD is metadata-only: it neither burns nor increments the view count.
	if r.Method == http.MethodHead {
		if partial {
			w.WriteHeader(http.StatusPartialContent)
		}
		return
	}

	var reader io.ReadCloser
	if partial {
		reader, err = h.svc.ServeRange(r.Context(), img, offset, length)
	} else {
		reader, err = h.svc.ServeRaw(r.Context(), img)
	}
	if err != nil {
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Range")
		w.Header().Del("ETag")
		w.Header().Set("Cache-Control", "private, no-store")
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	defer reader.Close()
	if partial {
		w.WriteHeader(http.StatusPartialContent)
	}

	written, copyErr := io.Copy(w, reader)
	if copyErr != nil {
		h.log.Debug("image serve interrupted", slog.String("short_id", shortID), slog.String("error", copyErr.Error()))
	}

	// Async view count (skip for burn-after-view, already handled)
	if written > 0 && !img.BurnAfterView {
		select {
		case h.viewsWorker <- img.ID:
			// View logged asynchronously
		default:
			h.log.Warn("view counter worker queue full, dropping count", slog.String("short_id", shortID))
		}
	}

	// Metrics
	metrics.ImageServed.Add(float64(written))
}

// ServeThumbnail handles GET /t/{shortID}.{ext}
func (h *ImageHandler) ServeThumbnail(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")
	img, err := h.svc.GetByShortIDForUser(r.Context(), shortID, getUserUUID(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// A thumbnail (including original fallback) is still a view of the content.
	// Do not offer a non-burning preview for one-shot images.
	if img.BurnAfterView {
		http.NotFound(w, r)
		return
	}

	reader, mime, size, ready, err := h.svc.ServeThumbnail(r.Context(), img, h.cfg.Thumbnail.Size)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if img.IsPrivate || img.BurnAfterView || img.ExpiresAt != nil || !ready || len(w.Header().Values("Set-Cookie")) > 0 {
		w.Header().Set("Cache-Control", "private, no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	if ready {
		w.Header().Set("ETag", `"`+img.SHA256+"-t"+strconv.Itoa(h.cfg.Thumbnail.Size)+`"`)
	} else {
		w.Header().Set("ETag", `"`+img.SHA256+`"`)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, reader); err != nil {
		h.log.Debug("thumbnail serve interrupted", slog.String("short_id", shortID), slog.String("error", err.Error()))
	}
}

var errInvalidRange = errors.New("invalid byte range")

// parseSingleRange parses one RFC 7233 byte range. Multi-range responses are
// deliberately rejected because clients can retry each immutable segment
// independently without multipart response overhead.
func parseSingleRange(header string, size int64) (offset, length int64, partial bool, err error) {
	if header == "" {
		return 0, size, false, nil
	}
	if size < 0 || !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, false, errInvalidRange
	}
	spec := strings.TrimSpace(strings.TrimPrefix(header, "bytes="))
	startText, endText, ok := strings.Cut(spec, "-")
	if !ok || (startText == "" && endText == "") {
		return 0, 0, false, errInvalidRange
	}
	if startText == "" {
		suffix, parseErr := strconv.ParseInt(endText, 10, 64)
		if parseErr != nil || suffix <= 0 || size == 0 {
			return 0, 0, false, errInvalidRange
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, suffix, true, nil
	}
	start, parseErr := strconv.ParseInt(startText, 10, 64)
	if parseErr != nil || start < 0 || start >= size {
		return 0, 0, false, errInvalidRange
	}
	end := size - 1
	if endText != "" {
		end, parseErr = strconv.ParseInt(endText, 10, 64)
		if parseErr != nil || end < start {
			return 0, 0, false, errInvalidRange
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end - start + 1, true, nil
}

func etagMatches(header, etag string) bool {
	for value := range strings.SplitSeq(header, ",") {
		value = strings.TrimSpace(value)
		if value == "*" || value == etag || strings.TrimPrefix(value, "W/") == etag {
			return true
		}
	}
	return false
}

// GetInfo handles GET /api/v1/images/{shortID}
func (h *ImageHandler) GetInfo(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")
	img, err := h.svc.GetByShortIDForUser(r.Context(), shortID, getUserUUID(r))
	if err != nil {
		writeError(w, mapErrorStatus(err), safeErrorMessage(err))
		return
	}
	writeJSON(w, http.StatusOK, h.svc.GetInfo(img))
}

// Delete handles DELETE /api/v1/images/{shortID}
func (h *ImageHandler) Delete(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")
	// Delete tokens are accepted only in a header. Query parameters leak via
	// browser history, reverse-proxy logs and Referer headers.
	deleteToken := r.Header.Get("X-Delete-Token")
	userID := getUserUUID(r)

	if err := h.svc.Delete(r.Context(), shortID, userID, deleteToken); err != nil {
		status := mapErrorStatus(err)
		writeError(w, status, safeErrorMessage(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListMyImages handles GET /api/v1/images
func (h *ImageHandler) ListMyImages(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUserID(w, r)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	imgs, total, err := h.svc.ListByUser(r.Context(), uid, page, perPage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list images")
		return
	}

	items := make([]domain.ImageInfoResponse, 0, len(imgs))
	for _, img := range imgs {
		items = append(items, *h.svc.GetInfo(img))
	}

	totalPages := (total + int64(perPage) - 1) / int64(perPage)

	writeJSON(w, http.StatusOK, domain.ImageListResponse{
		Images:     items,
		Total:      total,
		Page:       page,
		PerPage:    perPage,
		TotalPages: int(totalPages),
	})
}

// ── helpers ─────────────────────────

func getUserUUID(r *http.Request) *uuid.UUID {
	uidStr, ok := middleware.GetUserID(r.Context())
	if !ok {
		return nil
	}
	uid, err := uuid.Parse(uidStr)
	if err != nil {
		return nil
	}
	return &uid
}

func mapErrorStatus(err error) int {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	switch {
	case errors.Is(err, domain.ErrImageNotFound), errors.Is(err, domain.ErrImageDeleted), errors.Is(err, domain.ErrAlbumNotFound):
		return http.StatusNotFound
	case errors.Is(err, domain.ErrImageExpired):
		return http.StatusGone
	case errors.Is(err, domain.ErrBlockedType), errors.Is(err, domain.ErrInvalidImage), errors.Is(err, domain.ErrInvalidInput):
		return http.StatusBadRequest
	case errors.Is(err, domain.ErrFileTooLarge), errors.Is(err, domain.ErrQuotaExceeded), errors.Is(err, domain.ErrQuotaImagesExceeded):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, domain.ErrInsufficientStorage):
		return http.StatusInsufficientStorage
	case errors.Is(err, domain.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, domain.ErrUnauthorized), errors.Is(err, domain.ErrInvalidToken):
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}
