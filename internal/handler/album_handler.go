package handler

import (
	"log/slog"
	"net/http"

	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/HeartBtz/pxl/internal/middleware"
	"github.com/HeartBtz/pxl/internal/service"
	"github.com/go-chi/chi/v5"
)

// AlbumHandler gère les endpoints de gestion des albums :
// création, listage, ajout d'images, suppression.
type AlbumHandler struct {
	svc    *service.AlbumService
	imgSvc *service.ImageService
	log    *slog.Logger
}

// NewAlbumHandler crée un nouveau handler d'albums.
func NewAlbumHandler(svc *service.AlbumService, imgSvc *service.ImageService, logger *slog.Logger) *AlbumHandler {
	return &AlbumHandler{svc: svc, imgSvc: imgSvc, log: logger}
}

// Create handles POST /api/v1/albums
func (h *AlbumHandler) Create(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUserID(w, r)
	if !ok {
		return
	}
	var req domain.AlbumRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title == "" || len(req.Title) > 255 {
		writeError(w, http.StatusBadRequest, "title required (max 255 chars)")
		return
	}
	if len(req.Description) > 5000 {
		writeError(w, http.StatusBadRequest, "description too long (max 5000 chars)")
		return
	}
	album, err := h.svc.Create(r.Context(), uid, &req)
	if err != nil {
		writeError(w, mapErrorStatus(err), safeErrorMessage(err))
		return
	}
	writeJSON(w, http.StatusCreated, h.albumToResponse(album))
}

// Get handles GET /api/v1/albums/{shortID}
func (h *AlbumHandler) Get(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")
	album, err := h.svc.GetByShortID(r.Context(), shortID)
	if err != nil {
		writeError(w, mapErrorStatus(err), safeErrorMessage(err))
		return
	}
	if album.IsPrivate {
		uidStr, ok := middleware.GetUserID(r.Context())
		if !ok || uidStr != album.UserID.String() {
			writeError(w, http.StatusNotFound, "album not found")
			return
		}
	}
	visible := album.Images[:0]
	viewer := getUserUUID(r)
	for _, img := range album.Images {
		if !img.IsPrivate || (viewer != nil && img.UserID != nil && *viewer == *img.UserID) {
			visible = append(visible, img)
		}
	}
	album.Images = visible
	writeJSON(w, http.StatusOK, h.albumToResponse(album))
}

// List handles GET /api/v1/albums
func (h *AlbumHandler) List(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUserID(w, r)
	if !ok {
		return
	}
	albums, err := h.svc.ListByUser(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list albums")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"albums": albums})
}

// Delete handles DELETE /api/v1/albums/{shortID}
func (h *AlbumHandler) Delete(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUserID(w, r)
	if !ok {
		return
	}
	shortID := chi.URLParam(r, "shortID")
	if err := h.svc.Delete(r.Context(), shortID, uid); err != nil {
		status := mapErrorStatus(err)
		writeError(w, status, safeErrorMessage(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AddImage handles POST /api/v1/albums/{shortID}/images
func (h *AlbumHandler) AddImage(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUserID(w, r)
	if !ok {
		return
	}
	albumShortID := chi.URLParam(r, "shortID")
	var body struct {
		ImageShortID string `json:"image_short_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.ImageShortID == "" {
		writeError(w, http.StatusBadRequest, "image_short_id required")
		return
	}
	if err := h.svc.AddImage(r.Context(), albumShortID, body.ImageShortID, uid); err != nil {
		status := mapErrorStatus(err)
		writeError(w, status, safeErrorMessage(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveImage handles DELETE /api/v1/albums/{shortID}/images/{imageShortID}
func (h *AlbumHandler) RemoveImage(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUserID(w, r)
	if !ok {
		return
	}
	albumShortID := chi.URLParam(r, "shortID")
	imageShortID := chi.URLParam(r, "imageShortID")
	if err := h.svc.RemoveImage(r.Context(), albumShortID, imageShortID, uid); err != nil {
		status := mapErrorStatus(err)
		writeError(w, status, safeErrorMessage(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// albumToResponse converts a domain Album to an API response with full image URLs.
func (h *AlbumHandler) albumToResponse(album *domain.Album) map[string]any {
	images := make([]domain.ImageInfoResponse, 0, len(album.Images))
	for i := range album.Images {
		images = append(images, *h.imgSvc.GetInfo(&album.Images[i]))
	}
	return map[string]any{
		"id":          album.ID.String(),
		"short_id":    album.ShortID,
		"title":       album.Title,
		"description": album.Description,
		"is_private":  album.IsPrivate,
		"images":      images,
		"created_at":  album.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		"updated_at":  album.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}
