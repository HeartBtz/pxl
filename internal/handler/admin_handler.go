// Package handler — AdminHandler fournit les endpoints du tableau de bord admin.
package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/HeartBtz/pxl/internal/config"
	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/HeartBtz/pxl/internal/middleware"
	"github.com/HeartBtz/pxl/internal/repository"
	"github.com/HeartBtz/pxl/internal/service"
	"github.com/go-chi/chi/v5"
)

// AdminHandler fournit les endpoints d'administration :
// statistiques globales, gestion des images (listing, suppression forcée),
// et consultation des logs d'audit.
type AdminHandler struct {
	imgSvc    *service.ImageService
	auditSvc  *service.AuditService
	imageRepo *repository.ImageRepository
	albumRepo *repository.AlbumRepository
	userRepo  *repository.UserRepository
	cfg       *config.Config
	log       *slog.Logger
}

// NewAdminHandler crée un nouveau handler d'administration.
func NewAdminHandler(
	imgSvc *service.ImageService,
	auditSvc *service.AuditService,
	imageRepo *repository.ImageRepository,
	albumRepo *repository.AlbumRepository,
	userRepo *repository.UserRepository,
	cfg *config.Config,
	logger *slog.Logger,
) *AdminHandler {
	return &AdminHandler{
		imgSvc:    imgSvc,
		auditSvc:  auditSvc,
		imageRepo: imageRepo,
		albumRepo: albumRepo,
		userRepo:  userRepo,
		cfg:       cfg,
		log:       logger,
	}
}

// Stats handles GET /api/v1/admin/stats
func (h *AdminHandler) Stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	stats := domain.AdminStatsResponse{StorageBackend: h.cfg.Storage.Backend}
	err := h.albumRepo.DB().QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM users), (SELECT COUNT(*) FROM users WHERE is_active=true),
		COUNT(*), COALESCE(SUM(size_bytes),0),
		COUNT(*) FILTER (WHERE created_at > NOW() - INTERVAL '24 hours'),
		(SELECT COUNT(*) FROM albums)
		FROM images WHERE deleted_at IS NULL`).Scan(&stats.TotalUsers, &stats.ActiveUsers,
		&stats.TotalImages, &stats.TotalBytes, &stats.RecentUploads, &stats.TotalAlbums)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "statistics unavailable")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// ListAllImages handles GET /api/v1/admin/images
func (h *AdminHandler) ListAllImages(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	imgs, total, err := h.imageRepo.ListAll(r.Context(), page, perPage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list images")
		return
	}

	items := make([]domain.ImageInfoResponse, 0, len(imgs))
	for _, img := range imgs {
		items = append(items, *h.imgSvc.GetInfo(img))
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

// ForceDeleteImage handles DELETE /api/v1/admin/images/{shortID}
func (h *AdminHandler) ForceDeleteImage(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")

	img, err := h.imageRepo.GetByShortID(r.Context(), shortID)
	if err != nil {
		writeError(w, http.StatusNotFound, "image not found")
		return
	}

	// Force delete by admin (admin passed their own userID to bypass ownership check)
	adminUID := getUserUUID(r)
	if err := h.imgSvc.ForceDelete(r.Context(), img); err != nil {
		writeError(w, http.StatusInternalServerError, safeErrorMessage(err))
		return
	}

	adminIDStr, _ := middleware.GetUserID(r.Context())
	h.auditSvc.Log(r.Context(), adminUID, adminIDStr, service.AuditAdminDelete, "image", shortID,
		map[string]any{"original_name": img.OriginalName, "size_bytes": img.SizeBytes}, r)

	w.WriteHeader(http.StatusNoContent)
}

// AuditLogs handles GET /api/v1/admin/audit
func (h *AdminHandler) AuditLogs(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 50
	}
	action := r.URL.Query().Get("action")

	var logs []domain.AuditLog
	var total int64
	var err error

	if action != "" {
		logs, total, err = h.auditSvc.ListByAction(r.Context(), action, page, perPage)
	} else {
		logs, total, err = h.auditSvc.List(r.Context(), page, perPage)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list audit logs")
		return
	}

	entries := make([]domain.AuditLogEntry, 0, len(logs))
	for _, l := range logs {
		entries = append(entries, domain.AuditLogEntry{
			ID:         l.ID.String(),
			ActorName:  l.ActorName,
			Action:     l.Action,
			TargetType: l.TargetType,
			TargetID:   l.TargetID,
			Details:    l.Details,
			IPAddress:  l.IPAddress,
			CreatedAt:  l.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	totalPages := (total + int64(perPage) - 1) / int64(perPage)
	writeJSON(w, http.StatusOK, domain.AuditLogResponse{
		Logs:       entries,
		Total:      total,
		Page:       page,
		PerPage:    perPage,
		TotalPages: int(totalPages),
	})
}
