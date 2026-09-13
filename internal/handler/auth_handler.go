package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/HeartBtz/pxl/internal/config"
	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/HeartBtz/pxl/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// AuthHandler gère les endpoints d'authentification PXL :
// login, register, refresh, password change, logout, profil, clés API,
// et gestion admin des utilisateurs (list, update, suspend, delete).
type AuthHandler struct {
	svc      *service.AuthService
	auditSvc *service.AuditService
	cfg      *config.Config
	log      *slog.Logger
}

// NewAuthHandler crée un nouveau handler d'authentification.
func NewAuthHandler(svc *service.AuthService, auditSvc *service.AuditService, cfg *config.Config, logger *slog.Logger) *AuthHandler {
	return &AuthHandler{svc: svc, auditSvc: auditSvc, cfg: cfg, log: logger}
}

// Login handles POST /api/v1/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req domain.AuthLoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}

	resp, err := h.svc.Login(r.Context(), &req)
	if err != nil {
		h.auditSvc.Log(r.Context(), nil, req.Username, service.AuditLoginFailed, "user", req.Username, map[string]any{"reason": err.Error()}, r)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	uid, _ := uuid.Parse(resp.User.ID)
	h.auditSvc.Log(r.Context(), &uid, req.Username, service.AuditLogin, "user", resp.User.ID, nil, r)
	writeJSON(w, http.StatusOK, resp)
}

// BrowserLogin creates HttpOnly cookies and returns no bearer or refresh token
// to JavaScript. The token-returning Login endpoint remains available to API
// clients.
func (h *AuthHandler) BrowserLogin(w http.ResponseWriter, r *http.Request) {
	var req domain.AuthLoginRequest
	if err := decodeJSON(r, &req); err != nil || req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}
	resp, err := h.svc.Login(r.Context(), &req)
	if err != nil {
		h.auditSvc.Log(r.Context(), nil, req.Username, service.AuditLoginFailed, "user", req.Username, map[string]any{"reason": "invalid_credentials"}, r)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	h.setBrowserCookies(w, resp)
	uid, _ := uuid.Parse(resp.User.ID)
	h.auditSvc.Log(r.Context(), &uid, req.Username, service.AuditLogin, "user", resp.User.ID, nil, r)
	writeJSON(w, http.StatusOK, map[string]any{"user": resp.User, "expires_in": resp.ExpiresIn})
}

// BrowserRefresh rotates the HttpOnly refresh cookie and renews the session.
func (h *AuthHandler) BrowserRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("pxl_refresh")
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "refresh session required")
		return
	}
	resp, err := h.svc.RefreshJWT(r.Context(), cookie.Value)
	if err != nil {
		// A simultaneous rotation may already have issued replacement cookies.
		// A losing response must not erase the winner's session.
		writeError(w, http.StatusUnauthorized, "invalid or expired refresh session")
		return
	}
	h.setBrowserCookies(w, resp)
	writeJSON(w, http.StatusOK, map[string]any{"user": resp.User, "expires_in": resp.ExpiresIn})
}

// BrowserRegister creates a normal user and immediately establishes HttpOnly
// browser cookies without exposing the returned credentials to JavaScript.
func (h *AuthHandler) BrowserRegister(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Auth.AllowRegistration {
		writeError(w, http.StatusForbidden, "registration is disabled")
		return
	}
	var req domain.CreateUserRequest
	if err := decodeJSON(r, &req); err != nil || req.Username == "" || req.Email == "" || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "invalid registration")
		return
	}
	if len(req.Username) < 3 || len(req.Username) > 30 {
		writeError(w, http.StatusBadRequest, "username must be between 3 and 30 characters")
		return
	}
	req.Role = "user"
	user, err := h.svc.Register(r.Context(), &req)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrUserExists) {
			status = http.StatusConflict
		} else if errors.Is(err, domain.ErrInvalidInput) {
			status = http.StatusBadRequest
		}
		writeError(w, status, safeErrorMessage(err))
		return
	}
	resp, err := h.svc.Login(r.Context(), &domain.AuthLoginRequest{Username: req.Username, Password: req.Password})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "registration completed; login required")
		return
	}
	h.setBrowserCookies(w, resp)
	h.auditSvc.Log(r.Context(), &user.ID, user.Username, service.AuditRegister, "user", user.ID.String(), nil, r)
	writeJSON(w, http.StatusCreated, map[string]any{"user": resp.User, "expires_in": resp.ExpiresIn})
}

// Refresh handles POST /api/v1/auth/refresh
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req domain.RefreshRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "refresh_token required")
		return
	}

	resp, err := h.svc.RefreshJWT(r.Context(), req.RefreshToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Logout handles POST /api/v1/auth/logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUserID(w, r)
	if !ok {
		return
	}
	if err := h.svc.Logout(r.Context(), uid); err != nil {
		h.log.Error("logout error", slog.String("error", err.Error()))
		writeError(w, http.StatusInternalServerError, "logout failed; retry required")
		return
	}
	h.auditSvc.Log(r.Context(), &uid, "", service.AuditLogout, "user", uid.String(), nil, r)
	h.clearBrowserCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) setBrowserCookies(w http.ResponseWriter, resp *domain.AuthRefreshResponse) {
	secure := strings.HasPrefix(strings.ToLower(h.cfg.Server.BaseURL), "https://")
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure follows the validated public URL; production is HTTPS
		Name: "pxl_session", Value: resp.Token, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: int(resp.ExpiresIn),
	})
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure follows the validated public URL; production is HTTPS
		Name: "pxl_refresh", Value: resp.RefreshToken, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: int(h.cfg.Auth.RefreshExpiry.Seconds()),
	})
}

func (h *AuthHandler) clearBrowserCookies(w http.ResponseWriter) {
	secure := strings.HasPrefix(strings.ToLower(h.cfg.Server.BaseURL), "https://")
	for _, cookie := range []*http.Cookie{
		{Name: "pxl_session", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1}, // #nosec G124 -- production URL is HTTPS
		{Name: "pxl_refresh", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1}, // #nosec G124 -- production URL is HTTPS
	} {
		http.SetCookie(w, cookie)
	}
}

// ChangePassword handles POST /api/v1/auth/password
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUserID(w, r)
	if !ok {
		return
	}
	var req domain.ChangePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "old_password and new_password required")
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	if err := h.svc.ChangePassword(r.Context(), uid, req.OldPassword, req.NewPassword); err != nil {
		if errors.Is(err, domain.ErrInvalidCreds) {
			writeError(w, http.StatusUnauthorized, "old password is incorrect")
			return
		}
		if errors.Is(err, domain.ErrInvalidInput) || errors.Is(err, domain.ErrPasswordTooWeak) {
			writeError(w, http.StatusBadRequest, safeErrorMessage(err))
			return
		}
		writeError(w, http.StatusInternalServerError, safeErrorMessage(err))
		return
	}
	h.auditSvc.Log(r.Context(), &uid, "", service.AuditPasswordChange, "user", uid.String(), nil, r)
	h.clearBrowserCookies(w)
	writeJSON(w, http.StatusOK, map[string]string{"message": "password changed"})
}

// Register handles POST /api/v1/admin/users (admin only)
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req domain.CreateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username, email and password required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	user, err := h.svc.Register(r.Context(), &req)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrUserExists) {
			status = http.StatusConflict
		} else if errors.Is(err, domain.ErrInvalidInput) {
			status = http.StatusBadRequest
		}
		writeError(w, status, safeErrorMessage(err))
		return
	}

	adminUID, _ := parseUserID(w, r)
	h.auditSvc.Log(r.Context(), &adminUID, "", service.AuditAdminUserCreate, "user", user.ID.String(),
		map[string]any{"username": user.Username, "role": user.Role}, r)
	writeJSON(w, http.StatusCreated, domain.NewUserPublicResponse(user))
}

// Me handles GET /api/v1/auth/me
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUserID(w, r)
	if !ok {
		return
	}
	user, err := h.svc.GetUser(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, domain.NewUserPublicResponse(user))
}

// GenerateAPIKey handles POST /api/v1/auth/apikey
func (h *AuthHandler) GenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUserID(w, r)
	if !ok {
		return
	}
	key, err := h.svc.GenerateAPIKey(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate API key")
		return
	}
	h.auditSvc.Log(r.Context(), &uid, "", service.AuditAPIKeyGenerate, "user", uid.String(), nil, r)
	writeJSON(w, http.StatusCreated, map[string]string{"api_key": key})
}

// PublicRegister handles POST /api/v1/auth/register
// Accessible à tous si PXL_AUTH_ALLOW_REGISTRATION=true.
// Crée uniquement des comptes avec le rôle "user".
func (h *AuthHandler) PublicRegister(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Auth.AllowRegistration {
		writeError(w, http.StatusForbidden, "registration is disabled")
		return
	}

	var req domain.CreateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username, email and password required")
		return
	}
	if len(req.Username) < 3 || len(req.Username) > 30 {
		writeError(w, http.StatusBadRequest, "username must be between 3 and 30 characters")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	// Force role to "user" — public registration cannot create admins
	req.Role = "user"

	user, err := h.svc.Register(r.Context(), &req)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrUserExists) {
			status = http.StatusConflict
		} else if errors.Is(err, domain.ErrInvalidInput) {
			status = http.StatusBadRequest
		}
		writeError(w, status, safeErrorMessage(err))
		return
	}

	h.auditSvc.Log(r.Context(), &user.ID, user.Username, service.AuditRegister, "user", user.ID.String(), nil, r)

	// Auto-login: return JWT + refresh token directly after registration
	loginResp, err := h.svc.Login(r.Context(), &domain.AuthLoginRequest{
		Username: req.Username,
		Password: req.Password,
	})
	if err != nil {
		// Registration succeeded but auto-login failed — return user info only
		writeJSON(w, http.StatusCreated, domain.NewUserPublicResponse(user))
		return
	}
	writeJSON(w, http.StatusCreated, loginResp)
}

// ── Admin endpoints ──────────────────────────────────

// AdminListUsers handles GET /api/v1/admin/users
func (h *AuthHandler) AdminListUsers(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	search := r.URL.Query().Get("q")
	users, total, err := h.svc.ListUsers(r.Context(), page, perPage, search)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list users")
		return
	}

	items := make([]*domain.UserPublicResponse, 0, len(users))
	for _, u := range users {
		items = append(items, domain.NewUserPublicResponse(u))
	}
	totalPages := (total + int64(perPage) - 1) / int64(perPage)

	writeJSON(w, http.StatusOK, domain.UserListResponse{
		Users:      items,
		Total:      total,
		Page:       page,
		PerPage:    perPage,
		TotalPages: int(totalPages),
	})
}

// AdminGetUser handles GET /api/v1/admin/users/{userID}
func (h *AuthHandler) AdminGetUser(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user ID")
		return
	}
	user, err := h.svc.GetUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, domain.NewUserPublicResponse(user))
}

// AdminUpdateUser handles PATCH /api/v1/admin/users/{userID}
func (h *AuthHandler) AdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	adminUID, ok := parseUserID(w, r)
	if !ok {
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user ID")
		return
	}

	var req domain.AdminUpdateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.svc.AdminUpdateUser(r.Context(), adminUID, userID, &req); err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if errors.Is(err, domain.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, safeErrorMessage(err))
			return
		}
		if errors.Is(err, domain.ErrForbidden) {
			writeError(w, http.StatusConflict, "cannot remove or disable the current or last active administrator")
			return
		}
		writeError(w, http.StatusInternalServerError, safeErrorMessage(err))
		return
	}

	details := map[string]any{}
	if req.Role != nil {
		details["role"] = *req.Role
	}
	if req.IsActive != nil {
		details["is_active"] = *req.IsActive
	}
	if req.QuotaBytes != nil {
		details["quota_bytes"] = *req.QuotaBytes
	}
	if req.QuotaImages != nil {
		details["quota_images"] = *req.QuotaImages
	}
	h.auditSvc.Log(r.Context(), &adminUID, "", service.AuditAdminUserEdit, "user", userID.String(), details, r)

	user, _ := h.svc.GetUser(r.Context(), userID)
	if user != nil {
		writeJSON(w, http.StatusOK, domain.NewUserPublicResponse(user))
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

// AdminDeleteUser handles DELETE /api/v1/admin/users/{userID}
func (h *AuthHandler) AdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	adminUID, ok := parseUserID(w, r)
	if !ok {
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user ID")
		return
	}

	if err := h.svc.DeleteUser(r.Context(), adminUID, userID); err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if errors.Is(err, domain.ErrForbidden) {
			writeError(w, http.StatusConflict, "cannot delete the current or last active administrator")
			return
		}
		writeError(w, http.StatusInternalServerError, safeErrorMessage(err))
		return
	}

	h.auditSvc.Log(r.Context(), &adminUID, "", service.AuditAdminUserDelete, "user", userID.String(), nil, r)
	w.WriteHeader(http.StatusNoContent)
}
