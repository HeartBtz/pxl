// Package service — AuditService fournit le logging d'audit pour les actions sensibles.
package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"

	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/HeartBtz/pxl/internal/repository"
	"github.com/google/uuid"
)

// Actions d'audit prédéfinies.
const (
	AuditLogin           = "auth.login"
	AuditLoginFailed     = "auth.login_failed"
	AuditRegister        = "auth.register"
	AuditPasswordChange  = "auth.password_change"
	AuditLogout          = "auth.logout"
	AuditUpload          = "image.upload"
	AuditDelete          = "image.delete"
	AuditAdminDelete     = "admin.image.delete"
	AuditAdminUserEdit   = "admin.user.edit"
	AuditAdminUserCreate = "admin.user.create"
	AuditAdminUserDelete = "admin.user.delete"
	AuditAdminSuspend    = "admin.user.suspend"
	AuditAPIKeyGenerate  = "auth.apikey.generate" // #nosec G101 -- audit event name, not a credential
)

// AuditService écrit les logs d'audit en base de données.
type AuditService struct {
	repo *repository.AuditRepository
	log  *slog.Logger
}

// NewAuditService crée un nouveau service d'audit.
func NewAuditService(repo *repository.AuditRepository, logger *slog.Logger) *AuditService {
	return &AuditService{repo: repo, log: logger}
}

// Log enregistre une action d'audit. Appel non-bloquant (fire-and-forget en cas d'erreur).
func (s *AuditService) Log(ctx context.Context, actorID *uuid.UUID, actorName, action, targetType, targetID string, details map[string]any, r *http.Request) {
	detailsJSON, _ := json.Marshal(details)
	if detailsJSON == nil {
		detailsJSON = []byte("{}")
	}

	ip := ""
	if r != nil {
		ip = extractIP(r)
	}

	entry := &domain.AuditLog{
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Details:    string(detailsJSON),
		IPAddress:  ip,
	}

	if err := s.repo.Create(ctx, entry); err != nil {
		s.log.Error("failed to write audit log",
			slog.String("action", action),
			slog.String("error", err.Error()),
		)
	}
}

// List retourne les logs d'audit paginés.
func (s *AuditService) List(ctx context.Context, page, perPage int) ([]domain.AuditLog, int64, error) {
	return s.repo.List(ctx, page, perPage)
}

// ListByAction filtre les logs par action.
func (s *AuditService) ListByAction(ctx context.Context, action string, page, perPage int) ([]domain.AuditLog, int64, error) {
	return s.repo.ListByAction(ctx, action, page, perPage)
}

func extractIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
