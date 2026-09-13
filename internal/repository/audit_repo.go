// Package repository — AuditRepository gère les opérations CRUD sur les logs d'audit.
package repository

import (
	"context"
	"fmt"

	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/google/uuid"
)

// AuditRepository gère la persistance des logs d'audit.
type AuditRepository struct{ db *DB }

// NewAuditRepository crée un nouveau repository d'audit.
func NewAuditRepository(db *DB) *AuditRepository { return &AuditRepository{db: db} }

// Create insère un nouveau log d'audit.
func (r *AuditRepository) Create(ctx context.Context, log *domain.AuditLog) error {
	if log.ID == uuid.Nil {
		log.ID = uuid.New()
	}
	query := `INSERT INTO audit_logs (id, actor_id, actor_name, action, target_type, target_id, details, ip_address)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		log.ID, log.ActorID, log.ActorName, log.Action,
		log.TargetType, log.TargetID, log.Details, log.IPAddress,
	).Scan(&log.CreatedAt)
}

// List retourne les logs d'audit paginés, triés par date décroissante.
func (r *AuditRepository) List(ctx context.Context, page, perPage int) ([]domain.AuditLog, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit logs: %w", err)
	}
	offset := (page - 1) * perPage
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, actor_id, actor_name, action, target_type, target_id, details, ip_address, created_at
		 FROM audit_logs ORDER BY created_at DESC LIMIT $1 OFFSET $2`, perPage, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()

	var logs []domain.AuditLog
	for rows.Next() {
		var l domain.AuditLog
		if err := rows.Scan(
			&l.ID, &l.ActorID, &l.ActorName, &l.Action,
			&l.TargetType, &l.TargetID, &l.Details, &l.IPAddress, &l.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan audit log: %w", err)
		}
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
}

// ListByAction filtre par type d'action.
func (r *AuditRepository) ListByAction(ctx context.Context, action string, page, perPage int) ([]domain.AuditLog, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs WHERE action=$1", action).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit logs by action: %w", err)
	}
	offset := (page - 1) * perPage
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, actor_id, actor_name, action, target_type, target_id, details, ip_address, created_at
		 FROM audit_logs WHERE action=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, action, perPage, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit logs by action: %w", err)
	}
	defer rows.Close()

	var logs []domain.AuditLog
	for rows.Next() {
		var l domain.AuditLog
		if err := rows.Scan(
			&l.ID, &l.ActorID, &l.ActorName, &l.Action,
			&l.TargetType, &l.TargetID, &l.Details, &l.IPAddress, &l.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan audit log: %w", err)
		}
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
}
