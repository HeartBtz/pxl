package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// ImageRepository gère les opérations CRUD sur les images en base de données.
// Supporte la déduplication par hash SHA-256, le burn-after-view,
// l'expiration, le listage paginé et le compteur de vues.
type ImageRepository struct{ db *DB }

var ErrCommitUncertain = errors.New("image commit outcome uncertain")

// NewImageRepository crée un nouveau repository d'images.
func NewImageRepository(db *DB) *ImageRepository { return &ImageRepository{db: db} }

func (r *ImageRepository) Create(ctx context.Context, img *domain.Image) error {
	if img.ID == uuid.Nil {
		img.ID = uuid.New()
	}
	query := `INSERT INTO images (id, short_id, user_id, original_name, stored_name, storage_path,
		mime_type, extension, size_bytes, width, height, sha256, delete_token, is_private,
		burn_after_view, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		img.ID, img.ShortID, img.UserID, img.OriginalName, img.StoredName, img.StoragePath,
		img.MIMEType, img.Extension, img.SizeBytes, img.Width, img.Height, img.SHA256,
		img.DeleteToken, img.IsPrivate, img.BurnAfterView, img.ExpiresAt,
	).Scan(&img.CreatedAt)
}

// CreateWithQuota creates an image and reserves the owner's byte and image
// quotas in one transaction. The FOR UPDATE lock prevents concurrent uploads
// from oversubscribing an account.
func (r *ImageRepository) CreateWithQuota(ctx context.Context, img *domain.Image) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin image create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if img.UserID != nil {
		var quotaBytes, usedBytes int64
		var quotaImages, usedImages int
		err = tx.QueryRowContext(ctx,
			`SELECT quota_bytes, used_bytes, quota_images, used_images FROM users WHERE id=$1 FOR UPDATE`,
			*img.UserID,
		).Scan(&quotaBytes, &usedBytes, &quotaImages, &usedImages)
		if err == sql.ErrNoRows {
			return domain.ErrUserNotFound
		}
		if err != nil {
			return fmt.Errorf("lock image owner: %w", err)
		}
		if img.SizeBytes > quotaBytes-usedBytes {
			return domain.ErrQuotaExceeded
		}
		if usedImages >= quotaImages {
			return domain.ErrQuotaImagesExceeded
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET used_bytes=used_bytes+$1, used_images=used_images+1, updated_at=NOW() WHERE id=$2`,
			img.SizeBytes, *img.UserID,
		); err != nil {
			return fmt.Errorf("reserve owner quota: %w", err)
		}
	}
	if img.ID == uuid.Nil {
		img.ID = uuid.New()
	}
	query := `INSERT INTO images (id, short_id, user_id, original_name, stored_name, storage_path,
		mime_type, extension, size_bytes, width, height, sha256, delete_token, is_private,
		burn_after_view, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		RETURNING created_at`
	if err := tx.QueryRowContext(ctx, query,
		img.ID, img.ShortID, img.UserID, img.OriginalName, img.StoredName, img.StoragePath,
		img.MIMEType, img.Extension, img.SizeBytes, img.Width, img.Height, img.SHA256,
		img.DeleteToken, img.IsPrivate, img.BurnAfterView, img.ExpiresAt,
	).Scan(&img.CreatedAt); err != nil {
		return fmt.Errorf("insert image: %w", err)
	}
	if err := tx.Commit(); err != nil {
		var sqlErr *pq.Error
		// Only explicit transaction/constraint rollbacks (including a deferred
		// trigger exception) establish failure. Connection/FATAL errors do not.
		if !errors.As(err, &sqlErr) || (sqlErr.Code.Class() != "23" && sqlErr.Code.Class() != "40" && sqlErr.Code != "P0001") {
			return fmt.Errorf("commit image create: %w", errors.Join(ErrCommitUncertain, err))
		}
		return fmt.Errorf("commit image create: %w", err)
	}
	return nil
}

func (r *ImageRepository) GetByShortID(ctx context.Context, shortID string) (*domain.Image, error) {
	return r.scanOne(ctx, `SELECT `+imageCols+` FROM images WHERE short_id=$1 AND deleted_at IS NULL`, shortID)
}

func (r *ImageRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Image, error) {
	return r.scanOne(ctx, `SELECT `+imageCols+` FROM images WHERE id=$1 AND deleted_at IS NULL`, id)
}

func (r *ImageRepository) GetBySHA256(ctx context.Context, hash string, userID *uuid.UUID) (*domain.Image, error) {
	return r.scanOne(ctx, `SELECT `+imageCols+` FROM images
		WHERE sha256=$1 AND user_id=$2 AND deleted_at IS NULL
		AND (expires_at IS NULL OR expires_at>NOW()) AND burn_after_view=false LIMIT 1`, hash, userID)
}

func (r *ImageRepository) ListByUser(ctx context.Context, userID uuid.UUID, page, perPage int) ([]*domain.Image, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM images WHERE user_id=$1 AND deleted_at IS NULL", userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * perPage
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+imageCols+` FROM images WHERE user_id=$1 AND deleted_at IS NULL ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		userID, perPage, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var imgs []*domain.Image
	for rows.Next() {
		img, e := scanImage(rows)
		if e != nil {
			return nil, 0, e
		}
		imgs = append(imgs, img)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("rows iteration: %w", err)
	}
	return imgs, total, nil
}

func (r *ImageRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	now := time.Now()
	_, err := r.db.ExecContext(ctx, "UPDATE images SET deleted_at=$1 WHERE id=$2", now, id)
	return err
}

// SoftDeleteWithQuota marks an image deleted and releases its owner's quota
// atomically. The conditional update makes repeated concurrent deletes safe.
func (r *ImageRepository) SoftDeleteWithQuota(ctx context.Context, img *domain.Image) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin image delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE images SET deleted_at=NOW() WHERE id=$1 AND deleted_at IS NULL`, img.ID)
	if err != nil {
		return fmt.Errorf("soft delete image: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrImageNotFound
	}
	if img.UserID != nil {
		_, err = tx.ExecContext(ctx, `UPDATE users
			SET used_bytes=GREATEST(0, used_bytes-$1), used_images=GREATEST(0, used_images-1), updated_at=NOW()
			WHERE id=$2`, img.SizeBytes, *img.UserID)
		if err != nil {
			return fmt.Errorf("release owner quota: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit image delete: %w", err)
	}
	return nil
}

func (r *ImageRepository) IncrementViews(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, "UPDATE images SET view_count=view_count+1 WHERE id=$1", id)
	return err
}

func (r *ImageRepository) IncrementViewsBatch(ctx context.Context, counts map[uuid.UUID]int64) error {
	if len(counts) == 0 {
		return nil
	}
	ids := make([]string, 0, len(counts))
	deltas := make([]int64, 0, len(counts))
	for id, delta := range counts {
		ids = append(ids, id.String())
		deltas = append(deltas, delta)
	}
	_, err := r.db.ExecContext(ctx, `UPDATE images AS i
		SET view_count = i.view_count + v.delta
		FROM unnest($1::uuid[], $2::bigint[]) AS v(id, delta)
		WHERE i.id = v.id`, pq.Array(ids), pq.Array(deltas))
	return err
}

// BurnAfterViewDelete atomically soft-deletes an image if it has burn_after_view=true
// and view_count >= 1. Returns true if the image was deleted.
func (r *ImageRepository) BurnAfterViewDelete(ctx context.Context, id uuid.UUID) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE images SET deleted_at=NOW()
		 WHERE id=$1 AND burn_after_view=true AND view_count >= 1 AND deleted_at IS NULL`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ClaimBurnView atomically claims a burn-after-view image and releases the
// owner's quota. Exactly one concurrent request can succeed.
func (r *ImageRepository) ClaimBurnView(ctx context.Context, img *domain.Image) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE images
		SET view_count=view_count+1, deleted_at=NOW()
		WHERE id=$1 AND burn_after_view=true AND deleted_at IS NULL`, img.ID)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return false, nil
	}
	if img.UserID != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE users
			SET used_bytes=GREATEST(0, used_bytes-$1), used_images=GREATEST(0, used_images-1), updated_at=NOW()
			WHERE id=$2`, img.SizeBytes, *img.UserID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *ImageRepository) SetThumbGenerated(ctx context.Context, id uuid.UUID, v bool) error {
	res, err := r.db.ExecContext(ctx, "UPDATE images SET thumb_generated=$1 WHERE id=$2 AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at>NOW())", v, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrImageNotFound
	}
	return nil
}

func (r *ImageRepository) CountBySHA256(ctx context.Context, sha256 string) (int, error) {
	var c int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM images WHERE sha256=$1 AND deleted_at IS NULL", sha256).Scan(&c)
	return c, err
}

func (r *ImageRepository) CleanupExpired(ctx context.Context, limit int) ([]*domain.Image, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx,
		`UPDATE images SET deleted_at=NOW()
		 WHERE id IN (
			SELECT id FROM images
			WHERE expires_at IS NOT NULL AND expires_at < NOW() AND deleted_at IS NULL
			LIMIT $1
		 ) AND deleted_at IS NULL RETURNING `+imageCols, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var imgs []*domain.Image
	for rows.Next() {
		img, e := scanImage(rows)
		if e != nil {
			return nil, e
		}
		imgs = append(imgs, img)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close expired rows: %w", err)
	}
	for _, img := range imgs {
		if img.UserID == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users
			SET used_bytes=GREATEST(0, used_bytes-$1), used_images=GREATEST(0, used_images-1), updated_at=NOW()
			WHERE id=$2`, img.SizeBytes, *img.UserID); err != nil {
			return nil, fmt.Errorf("release expired image quota: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit expired cleanup: %w", err)
	}
	return imgs, nil
}

// ListMissingThumbnails returns active images whose asynchronous thumbnail
// job has not completed yet.
func (r *ImageRepository) ListMissingThumbnails(ctx context.Context, limit int) ([]*domain.Image, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+imageCols+` FROM images
		WHERE thumb_generated=false AND deleted_at IS NULL ORDER BY created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var imgs []*domain.Image
	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, err
		}
		imgs = append(imgs, img)
	}
	return imgs, rows.Err()
}

// ── internals ────────────────────────────────────────

const imageCols = `id, short_id, user_id, original_name, stored_name, storage_path,
	mime_type, extension, size_bytes, width, height, sha256, delete_token, is_private,
	view_count, burn_after_view, expires_at, thumb_generated, created_at, deleted_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanImage(s scanner) (*domain.Image, error) {
	img := &domain.Image{}
	err := s.Scan(
		&img.ID, &img.ShortID, &img.UserID, &img.OriginalName, &img.StoredName, &img.StoragePath,
		&img.MIMEType, &img.Extension, &img.SizeBytes, &img.Width, &img.Height, &img.SHA256,
		&img.DeleteToken, &img.IsPrivate, &img.ViewCount, &img.BurnAfterView,
		&img.ExpiresAt, &img.ThumbGenerated, &img.CreatedAt, &img.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	return img, nil
}

func (r *ImageRepository) scanOne(ctx context.Context, query string, args ...any) (*domain.Image, error) {
	row := r.db.QueryRowContext(ctx, query, args...)
	img, err := scanImage(row)
	if err == sql.ErrNoRows {
		return nil, domain.ErrImageNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan image: %w", err)
	}
	return img, nil
}

// CountAll retourne le nombre total d'images actives.
func (r *ImageRepository) CountAll(ctx context.Context) (int64, error) {
	var c int64
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM images WHERE deleted_at IS NULL").Scan(&c)
	return c, err
}

// TotalBytes retourne la taille totale de toutes les images actives.
func (r *ImageRepository) TotalBytes(ctx context.Context) (int64, error) {
	var t sql.NullInt64
	err := r.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(size_bytes),0) FROM images WHERE deleted_at IS NULL").Scan(&t)
	if err != nil {
		return 0, err
	}
	return t.Int64, nil
}

// RecentUploads retourne le nombre d'uploads des dernières 24h.
func (r *ImageRepository) RecentUploads(ctx context.Context) (int64, error) {
	var c int64
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM images WHERE created_at > NOW() - INTERVAL '24 hours' AND deleted_at IS NULL").Scan(&c)
	return c, err
}

// ListAll retourne toutes les images paginées (admin).
func (r *ImageRepository) ListAll(ctx context.Context, page, perPage int) ([]*domain.Image, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM images WHERE deleted_at IS NULL").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count images: %w", err)
	}
	offset := (page - 1) * perPage
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+imageCols+` FROM images WHERE deleted_at IS NULL ORDER BY created_at DESC LIMIT $1 OFFSET $2`, perPage, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list images: %w", err)
	}
	defer rows.Close()
	var imgs []*domain.Image
	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan image: %w", err)
		}
		imgs = append(imgs, img)
	}
	return imgs, total, rows.Err()
}

// ForceDelete supprime définitivement une image (admin).
func (r *ImageRepository) ForceDelete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM images WHERE id=$1", id)
	return err
}
