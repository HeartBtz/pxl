package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/google/uuid"
)

// UserRepository gère les opérations CRUD sur les utilisateurs en base de données.
type UserRepository struct{ db *DB }

// NewUserRepository crée un nouveau repository d'utilisateurs.
func NewUserRepository(db *DB) *UserRepository { return &UserRepository{db: db} }

func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	query := `INSERT INTO users (id, username, email, password_hash, role, is_active, quota_bytes, used_bytes, quota_images, used_images)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING created_at, updated_at`
	return r.db.QueryRowContext(ctx, query,
		u.ID, u.Username, u.Email, u.PasswordHash, u.Role, u.IsActive, u.QuotaBytes, u.UsedBytes,
		u.QuotaImages, u.UsedImages,
	).Scan(&u.CreatedAt, &u.UpdatedAt)
}

func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return r.scanUser(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id)
}

func (r *UserRepository) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	return r.scanUser(ctx, `SELECT `+userCols+` FROM users WHERE username=$1`, username)
}

func (r *UserRepository) GetByAPIKeyHash(ctx context.Context, hash string) (*domain.User, error) {
	return r.scanUser(ctx, `SELECT `+userCols+` FROM users WHERE api_key_hash=$1 AND is_active=true`, hash)
}

func (r *UserRepository) Exists(ctx context.Context, username, email string) (bool, error) {
	var c int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE username=$1 OR email=$2", username, email).Scan(&c)
	return c > 0, err
}

func (r *UserRepository) UpdateUsedBytes(ctx context.Context, userID uuid.UUID, delta int64) error {
	_, err := r.db.ExecContext(ctx, "UPDATE users SET used_bytes=used_bytes+$1 WHERE id=$2 AND used_bytes+$1>=0", delta, userID)
	return err
}

// UpdateUsedImages atomiquement met à jour le compteur d'images.
func (r *UserRepository) UpdateUsedImages(ctx context.Context, userID uuid.UUID, delta int) error {
	_, err := r.db.ExecContext(ctx, "UPDATE users SET used_images=used_images+$1 WHERE id=$2 AND used_images+$1>=0", delta, userID)
	return err
}

func (r *UserRepository) SetAPIKey(ctx context.Context, userID uuid.UUID, hash, prefix string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE users SET api_key_hash=$1, api_key_prefix=$2 WHERE id=$3", hash, prefix, userID)
	return err
}

// ListAll retourne tous les utilisateurs paginés avec recherche optionnelle.
func (r *UserRepository) ListAll(ctx context.Context, page, perPage int, search string) ([]*domain.User, int64, error) {
	var total int64
	var rows *sql.Rows
	var err error
	offset := (page - 1) * perPage

	if search != "" {
		like := "%" + search + "%"
		if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE username ILIKE $1 OR email ILIKE $1", like).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count users: %w", err)
		}
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+userCols+` FROM users WHERE username ILIKE $1 OR email ILIKE $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, like, perPage, offset)
	} else {
		if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count users: %w", err)
		}
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+userCols+` FROM users ORDER BY created_at DESC LIMIT $1 OFFSET $2`, perPage, offset)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var users []*domain.User
	for rows.Next() {
		u := &domain.User{}
		if err := rows.Scan(
			&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.IsActive,
			&u.APIKeyHash, &u.APIKeyPrefix, &u.QuotaBytes, &u.UsedBytes,
			&u.QuotaImages, &u.UsedImages, &u.CreatedAt, &u.UpdatedAt, &u.AuthVersion,
		); err != nil {
			return nil, 0, fmt.Errorf("scan user row: %w", err)
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// Count retourne le nombre total d'utilisateurs.
func (r *UserRepository) Count(ctx context.Context) (int, error) {
	var c int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&c)
	return c, err
}

// CountActive retourne le nombre d'utilisateurs actifs.
func (r *UserRepository) CountActive(ctx context.Context) (int, error) {
	var c int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE is_active=true").Scan(&c)
	return c, err
}

// UpdateRole change le rôle d'un utilisateur.
func (r *UserRepository) UpdateRole(ctx context.Context, userID uuid.UUID, role string) error {
	res, err := r.db.ExecContext(ctx, "UPDATE users SET role=$1, updated_at=NOW() WHERE id=$2", role, userID)
	if err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

// UpdateIsActive active ou désactive un utilisateur.
func (r *UserRepository) UpdateIsActive(ctx context.Context, userID uuid.UUID, active bool) error {
	res, err := r.db.ExecContext(ctx, "UPDATE users SET is_active=$1, updated_at=NOW() WHERE id=$2", active, userID)
	if err != nil {
		return fmt.Errorf("update is_active: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

// UpdatePassword met à jour le hash du mot de passe.
func (r *UserRepository) UpdatePassword(ctx context.Context, userID uuid.UUID, oldHash, hash string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=$1, auth_version=auth_version+1,
		api_key_hash=NULL, api_key_prefix=NULL, updated_at=NOW() WHERE id=$2 AND password_hash=$3 AND is_active=true`, hash, userID, oldHash)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrInvalidCreds
	}
	if _, err := tx.ExecContext(ctx, "UPDATE refresh_tokens SET revoked_at=NOW() WHERE user_id=$1 AND revoked_at IS NULL", userID); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeSessions serializes with refresh issuance on the user row.
func (r *UserRepository) RevokeSessions(ctx context.Context, userID uuid.UUID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "UPDATE users SET auth_version=auth_version+1 WHERE id=$1", userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE refresh_tokens SET revoked_at=NOW() WHERE user_id=$1 AND revoked_at IS NULL", userID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateQuotas met à jour les quotas d'un utilisateur.
func (r *UserRepository) UpdateQuotas(ctx context.Context, userID uuid.UUID, quotaBytes *int64, quotaImages *int) error {
	if quotaBytes != nil {
		if _, err := r.db.ExecContext(ctx, "UPDATE users SET quota_bytes=$1, updated_at=NOW() WHERE id=$2", *quotaBytes, userID); err != nil {
			return fmt.Errorf("update quota_bytes: %w", err)
		}
	}
	if quotaImages != nil {
		if _, err := r.db.ExecContext(ctx, "UPDATE users SET quota_images=$1, updated_at=NOW() WHERE id=$2", *quotaImages, userID); err != nil {
			return fmt.Errorf("update quota_images: %w", err)
		}
	}
	return nil
}

// AdminUpdate applies all account mutations atomically and revokes refresh
// tokens in the same transaction when an account is suspended.
func (r *UserRepository) AdminUpdate(ctx context.Context, userID uuid.UUID, req *domain.AdminUpdateUserRequest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin admin user update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := protectLastAdmin(ctx, tx, userID, (req.Role != nil && *req.Role != "admin") || (req.IsActive != nil && !*req.IsActive)); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET
		role=COALESCE($1::text, role),
		is_active=COALESCE($2::boolean, is_active),
		quota_bytes=COALESCE($3::bigint, quota_bytes),
		quota_images=COALESCE($4::integer, quota_images),
		auth_version=auth_version + CASE WHEN $2::boolean=false THEN 1 ELSE 0 END,
		updated_at=NOW()
		WHERE id=$5`, req.Role, req.IsActive, req.QuotaBytes, req.QuotaImages, userID)
	if err != nil {
		return fmt.Errorf("admin user update: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrUserNotFound
	}
	if req.IsActive != nil && !*req.IsActive {
		if _, err := tx.ExecContext(ctx, `UPDATE refresh_tokens SET revoked_at=NOW()
			WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
			return fmt.Errorf("revoke suspended user sessions: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit admin user update: %w", err)
	}
	return nil
}

func (r *UserRepository) CountActiveAdmins(ctx context.Context) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role='admin' AND is_active=true").Scan(&count); err != nil {
		return 0, fmt.Errorf("count active administrators: %w", err)
	}
	return count, nil
}

// Delete supprime un utilisateur (hard delete).
func (r *UserRepository) Delete(ctx context.Context, userID uuid.UUID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := protectLastAdmin(ctx, tx, userID, true); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id=$1", userID)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrUserNotFound
	}
	return tx.Commit()
}

func protectLastAdmin(ctx context.Context, tx *sql.Tx, userID uuid.UUID, removing bool) error {
	// Serialize account administration before counting, including concurrent
	// cross-demotions/deletions which can each see two admins outside a transaction.
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(706, 1)"); err != nil {
		return err
	}
	if !removing {
		return nil
	}
	var last bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='admin' AND is_active=true)
		AND (SELECT COUNT(*) FROM users WHERE role='admin' AND is_active=true)<=1`, userID).Scan(&last); err != nil {
		return err
	}
	if last {
		return domain.ErrForbidden
	}
	return nil
}

// ── internals ────────────────────────────────────────

const userCols = `id, username, email, password_hash, role, is_active, api_key_hash, api_key_prefix, quota_bytes, used_bytes, quota_images, used_images, created_at, updated_at, auth_version`

func (r *UserRepository) scanUser(ctx context.Context, query string, args ...any) (*domain.User, error) {
	u := &domain.User{}
	err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.IsActive,
		&u.APIKeyHash, &u.APIKeyPrefix, &u.QuotaBytes, &u.UsedBytes,
		&u.QuotaImages, &u.UsedImages, &u.CreatedAt, &u.UpdatedAt, &u.AuthVersion,
	)
	if err == sql.ErrNoRows {
		return nil, domain.ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	return u, nil
}
