package handler

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/HeartBtz/pxl/internal/config"
	"github.com/HeartBtz/pxl/internal/middleware"
	"github.com/HeartBtz/pxl/internal/service"
	"github.com/go-chi/chi/v5"
)

// templateFS contient les templates HTML embarquées dans le binaire PXL.
//
//go:embed templates/*.html
var templateFS embed.FS

// WebHandler gère l'interface web de PXL (page d'upload, galerie, vue image, admin).
type WebHandler struct {
	imgSvc   *service.ImageService
	albumSvc *service.AlbumService
	authSvc  *service.AuthService
	cfg      *config.Config
	log      *slog.Logger
	tmpl     *template.Template
}

func NewWebHandler(imgSvc *service.ImageService, albumSvc *service.AlbumService, authSvc *service.AuthService, cfg *config.Config, logger *slog.Logger) *WebHandler {
	tmpl := template.Must(template.ParseFS(templateFS, "templates/*.html"))
	return &WebHandler{imgSvc: imgSvc, albumSvc: albumSvc, authSvc: authSvc, cfg: cfg, log: logger, tmpl: tmpl}
}

// UploadPage handles GET /
func (h *WebHandler) UploadPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"BaseURL":        h.cfg.Server.BaseURL,
		"MaxSize":        h.cfg.Upload.MaxSizeBytes,
		"MaxFiles":       h.cfg.Upload.MaxFiles,
		"AllowedTypes":   h.cfg.Upload.AllowedTypes,
		"AllowAnonymous": h.cfg.Auth.AllowAnonymous,
		"Nonce":          middleware.GetCSPNonce(r.Context()),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "upload.html", data); err != nil {
		h.log.Error("template error", slog.String("error", err.Error()))
	}
}

// LoginPage handles GET /login
func (h *WebHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"BaseURL":           h.cfg.Server.BaseURL,
		"AllowRegistration": h.cfg.Auth.AllowRegistration,
		"Nonce":             middleware.GetCSPNonce(r.Context()),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "login.html", data); err != nil {
		h.log.Error("template error", slog.String("error", err.Error()))
	}
}

// AccountPage handles GET /account
func (h *WebHandler) AccountPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"BaseURL": h.cfg.Server.BaseURL,
		"Nonce":   middleware.GetCSPNonce(r.Context()),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "account.html", data); err != nil {
		h.log.Error("template error", slog.String("error", err.Error()))
	}
}

// ViewPage handles GET /v/{shortID}
func (h *WebHandler) ViewPage(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")
	img, err := h.imgSvc.GetByShortIDForUser(r.Context(), shortID, getUserUUID(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	info := h.imgSvc.GetInfo(img)
	data := map[string]any{
		"BaseURL": h.cfg.Server.BaseURL,
		"Image":   info,
		"ShortID": shortID,
		"IsOwner": false,
		"Nonce":   middleware.GetCSPNonce(r.Context()),
	}

	// Check if viewer is owner
	if uidStr, ok := middleware.GetUserID(r.Context()); ok {
		if img.UserID != nil && uidStr == img.UserID.String() {
			data["IsOwner"] = true
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "view.html", data); err != nil {
		h.log.Error("template error", slog.String("error", err.Error()))
	}
	// Note: view count is incremented when the browser loads the image via /i/{shortID}
}

// GalleryPage handles GET /gallery
func (h *WebHandler) GalleryPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"BaseURL": h.cfg.Server.BaseURL,
		"Nonce":   middleware.GetCSPNonce(r.Context()),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "gallery.html", data); err != nil {
		h.log.Error("template error", slog.String("error", err.Error()))
	}
}

// AdminPage handles GET /admin
func (h *WebHandler) AdminPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"BaseURL": h.cfg.Server.BaseURL,
		"Nonce":   middleware.GetCSPNonce(r.Context()),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "admin.html", data); err != nil {
		h.log.Error("template error", slog.String("error", err.Error()))
	}
}

// AlbumPage handles GET /a/{shortID}
func (h *WebHandler) AlbumPage(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")
	data := map[string]any{
		"BaseURL": h.cfg.Server.BaseURL,
		"ShortID": shortID,
		"Nonce":   middleware.GetCSPNonce(r.Context()),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "album.html", data); err != nil {
		h.log.Error("template error", slog.String("error", err.Error()))
	}
}
