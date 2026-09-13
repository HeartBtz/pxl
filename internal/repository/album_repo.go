package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/google/uuid"
)

// AlbumRepository gère les opérations CRUD sur les albums en base de données.
type AlbumRepository struct{ db *DB }

// NewAlbumRepository crée un nouveau repository d'albums.
func NewAlbumRepository(db *DB) *AlbumRepository { return &AlbumRepository{db: db} }

// DB expose le *DB sous-jacent pour les requêtes admin ad hoc.
func (r *AlbumRepository) DB() *DB { return r.db }

func (r *AlbumRepository) Create(ctx context.Context, a *domain.Album, imageIDs ...uuid.UUID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	query := `INSERT INTO albums (id, short_id, user_id, title, description, is_private)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING created_at, updated_at`
	if err := tx.QueryRowContext(ctx, query,
		a.ID, a.ShortID, a.UserID, a.Title, a.Description, a.IsPrivate,
	).Scan(&a.CreatedAt, &a.UpdatedAt); err != nil {
		return err
	}
	for i, id := range imageIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO album_images (album_id, image_id, position)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, a.ID, id, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *AlbumRepository) GetByShortID(ctx context.Context, shortID string) (*domain.Album, error) {
	a := &domain.Album{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, short_id, user_id, title, description, is_private, created_at, updated_at
		 FROM albums WHERE short_id=$1`, shortID,
	).Scan(&a.ID, &a.ShortID, &a.UserID, &a.Title, &a.Description, &a.IsPrivate, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, domain.ErrAlbumNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan album: %w", err)
	}
	return a, nil
}

func (r *AlbumRepository) ListByUser(ctx context.Context, userID uuid.UUID) ([]*domain.Album, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, short_id, user_id, title, description, is_private, created_at, updated_at
		 FROM albums WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var albums []*domain.Album
	for rows.Next() {
		a := &domain.Album{}
		if err := rows.Scan(&a.ID, &a.ShortID, &a.UserID, &a.Title, &a.Description, &a.IsPrivate, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		albums = append(albums, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	return albums, nil
}

func (r *AlbumRepository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM albums WHERE id=$1", id)
	return err
}

func (r *AlbumRepository) AddImage(ctx context.Context, albumID, imageID uuid.UUID, position int) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO album_images (album_id, image_id, position) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
		albumID, imageID, position)
	return err
}

func (r *AlbumRepository) RemoveImage(ctx context.Context, albumID, imageID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM album_images WHERE album_id=$1 AND image_id=$2", albumID, imageID)
	return err
}

func (r *AlbumRepository) GetImages(ctx context.Context, albumID uuid.UUID) ([]*domain.Image, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+imageCols+` FROM images
		 JOIN album_images ai ON ai.image_id = images.id
		 WHERE ai.album_id=$1 AND images.deleted_at IS NULL
		 AND (images.expires_at IS NULL OR images.expires_at > NOW())
		 ORDER BY ai.position LIMIT 1000`, albumID)
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
	return imgs, nil
}
