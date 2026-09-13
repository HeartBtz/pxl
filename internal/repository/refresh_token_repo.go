// Package repository — RefreshTokenRepository gère les refresh tokens en base de données.
package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/google/uuid"
)

// RefreshTokenRepository gère la persistance des refresh tokens.
type RefreshTokenRepository struct{ db *DB }

// NewRefreshTokenRepository crée un nouveau repository de refresh tokens.
func NewRefreshTokenRepository(db *DB) *RefreshTokenRepository {
	return &RefreshTokenRepository{db: db}
}

// Create insère un nouveau refresh token.
func (r *RefreshTokenRepository) Create(ctx context.Context, rt *domain.RefreshToken) error {
	if rt.ID == uuid.Nil {
		rt.ID = uuid.New()
	}
	query := `INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, auth_version)
		SELECT $1, id, $3, $4, auth_version FROM users
		WHERE id=$2 AND is_active=true AND auth_version=$5 FOR UPDATE
		RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		rt.ID, rt.UserID, rt.TokenHash, rt.ExpiresAt, rt.AuthVersion,
	).Scan(&rt.CreatedAt)
}

// Rotate commits consumption and replacement together, holding the same user
// lock as password changes, suspension and logout. No descendant can escape revocation.
func (r *RefreshTokenRepository) Rotate(ctx context.Context, hash, newHash string, expires time.Time) (*domain.User, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	u := &domain.User{}
	err = tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users
		WHERE id=(SELECT user_id FROM refresh_tokens WHERE token_hash=$1) FOR UPDATE`, hash).Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.IsActive,
		&u.APIKeyHash, &u.APIKeyPrefix, &u.QuotaBytes, &u.UsedBytes,
		&u.QuotaImages, &u.UsedImages, &u.CreatedAt, &u.UpdatedAt, &u.AuthVersion)
	if err == sql.ErrNoRows {
		return nil, domain.ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	if !u.IsActive {
		return nil, domain.ErrUserInactive
	}
	res, err := tx.ExecContext(ctx, `UPDATE refresh_tokens SET revoked_at=NOW()
		WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>NOW() AND auth_version=$2`, hash, u.AuthVersion)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, domain.ErrInvalidToken
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO refresh_tokens (user_id, token_hash, expires_at, auth_version)
		VALUES ($1,$2,$3,$4)`, u.ID, newHash, expires, u.AuthVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return u, nil
}

// GetByHash retourne un refresh token valide (non révoqué, non expiré) par son hash.
func (r *RefreshTokenRepository) GetByHash(ctx context.Context, hash string) (*domain.RefreshToken, error) {
	rt := &domain.RefreshToken{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, token_hash, expires_at, created_at, revoked_at
		 FROM refresh_tokens WHERE token_hash=$1 AND revoked_at IS NULL`, hash,
	).Scan(&rt.ID, &rt.UserID, &rt.TokenHash, &rt.ExpiresAt, &rt.CreatedAt, &rt.RevokedAt)
	if err == sql.ErrNoRows {
		return nil, domain.ErrInvalidToken
	}
	if err != nil {
		return nil, fmt.Errorf("get refresh token: %w", err)
	}
	if rt.ExpiresAt.Before(time.Now()) {
		return nil, domain.ErrRefreshExpired
	}
	return rt, nil
}

// ConsumeByHash atomically validates and revokes a refresh token. Exactly one
// concurrent rotation can succeed, preventing replay from creating multiple
// valid token descendants.
func (r *RefreshTokenRepository) ConsumeByHash(ctx context.Context, hash string) (*domain.RefreshToken, error) {
	rt := &domain.RefreshToken{}
	err := r.db.QueryRowContext(ctx, `UPDATE refresh_tokens
		SET revoked_at=NOW()
		WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at > NOW()
		RETURNING id, user_id, token_hash, expires_at, created_at, revoked_at`, hash,
	).Scan(&rt.ID, &rt.UserID, &rt.TokenHash, &rt.ExpiresAt, &rt.CreatedAt, &rt.RevokedAt)
	if err == sql.ErrNoRows {
		return nil, domain.ErrInvalidToken
	}
	if err != nil {
		return nil, fmt.Errorf("consume refresh token: %w", err)
	}
	return rt, nil
}

// Revoke marque un refresh token comme révoqué.
func (r *RefreshTokenRepository) Revoke(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE refresh_tokens SET revoked_at=NOW() WHERE id=$1", id)
	return err
}

// RevokeAllForUser révoque tous les refresh tokens d'un utilisateur.
func (r *RefreshTokenRepository) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE refresh_tokens SET revoked_at=NOW() WHERE user_id=$1 AND revoked_at IS NULL", userID)
	return err
}

// CleanupExpired supprime les refresh tokens expirés depuis plus de 7 jours.
func (r *RefreshTokenRepository) CleanupExpired(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM refresh_tokens WHERE expires_at < NOW() - INTERVAL '7 days'")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
