// Package repository implémente la couche d'accès aux données PostgreSQL de PXL.
//
// Chaque repository est spécialisé pour une entité du domaine :
//   - UserRepository  : utilisateurs (CRUD, recherche par username/email)
//   - ImageRepository : images (CRUD, déduplication SHA-256, burn-after-view)
//   - AlbumRepository : albums (CRUD, association images)
//
// Le schéma SQL est embarqué dans db.go et appliqué automatiquement au démarrage.
package repository

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/lib/pq"
)

// migrationV1SQL is the initial schema embedded directly to avoid embed path issues.
const migrationV1SQL = `
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE IF NOT EXISTS users (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    username        VARCHAR(50) UNIQUE NOT NULL,
    email           VARCHAR(255) UNIQUE NOT NULL,
    password_hash   TEXT NOT NULL,
    role            VARCHAR(20) NOT NULL DEFAULT 'user',
    is_active       BOOLEAN NOT NULL DEFAULT true,
    api_key_hash    VARCHAR(64),
    api_key_prefix  VARCHAR(11),
    quota_bytes     BIGINT NOT NULL DEFAULT 1073741824,
    used_bytes      BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS images (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    short_id         VARCHAR(12) UNIQUE NOT NULL,
    user_id          UUID REFERENCES users(id) ON DELETE SET NULL,
    original_name    VARCHAR(255) NOT NULL,
    stored_name      VARCHAR(255) NOT NULL,
    storage_path     TEXT NOT NULL,
    mime_type        VARCHAR(100) NOT NULL,
    extension        VARCHAR(10) NOT NULL,
    size_bytes       BIGINT NOT NULL,
    width            INT NOT NULL DEFAULT 0,
    height           INT NOT NULL DEFAULT 0,
    sha256           VARCHAR(64) NOT NULL,
    delete_token     VARCHAR(71) NOT NULL,
    is_private       BOOLEAN NOT NULL DEFAULT false,
    view_count       BIGINT NOT NULL DEFAULT 0,
    burn_after_view  BOOLEAN NOT NULL DEFAULT false,
    expires_at       TIMESTAMPTZ,
    thumb_generated  BOOLEAN NOT NULL DEFAULT false,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at       TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_images_short_id  ON images(short_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_images_sha256    ON images(sha256)   WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_images_user_id   ON images(user_id)  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_images_expires   ON images(expires_at) WHERE expires_at IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_images_created   ON images(created_at DESC);

CREATE TABLE IF NOT EXISTS albums (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    short_id    VARCHAR(12) UNIQUE NOT NULL,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title       VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    is_private  BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_albums_user  ON albums(user_id);
CREATE INDEX IF NOT EXISTS idx_albums_short ON albums(short_id);

CREATE TABLE IF NOT EXISTS album_images (
    album_id  UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    image_id  UUID NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    position  INT  NOT NULL DEFAULT 0,
    added_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (album_id, image_id)
);
`

// migrationV2SQL ajoute : audit_logs, quota_images, refresh_tokens, et les colonnes manquantes.
const migrationV2SQL = `
-- Table d'audit : trace toutes les actions admin et utilisateur sensibles
CREATE TABLE IF NOT EXISTS audit_logs (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    actor_id    UUID REFERENCES users(id) ON DELETE SET NULL,
    actor_name  VARCHAR(50) NOT NULL DEFAULT '',
    action      VARCHAR(100) NOT NULL,
    target_type VARCHAR(50) NOT NULL DEFAULT '',
    target_id   VARCHAR(255) NOT NULL DEFAULT '',
    details     JSONB NOT NULL DEFAULT '{}',
    ip_address  VARCHAR(45) NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_actor   ON audit_logs(actor_id);
CREATE INDEX IF NOT EXISTS idx_audit_action  ON audit_logs(action);

-- Refresh tokens pour renouvellement sécurisé des JWT
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  VARCHAR(64) NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_refresh_user    ON refresh_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_refresh_hash    ON refresh_tokens(token_hash) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_refresh_expires ON refresh_tokens(expires_at);

-- Ajout quota nombre d'images
ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_images INT NOT NULL DEFAULT 5000;
ALTER TABLE users ADD COLUMN IF NOT EXISTS used_images  INT NOT NULL DEFAULT 0;

-- Comptage des images existantes pour initialiser used_images
UPDATE users SET used_images = (
    SELECT COUNT(*) FROM images WHERE images.user_id = users.id AND images.deleted_at IS NULL
) WHERE used_images = 0;
`

// migrationV3SQL converts delete capabilities from plaintext to a prefixed
// SHA-256 representation. The prefix prevents a stored digest from being
// accepted as a legacy plaintext token during the compatibility window.
const migrationV3SQL = `
CREATE EXTENSION IF NOT EXISTS pgcrypto;
ALTER TABLE images ALTER COLUMN delete_token TYPE VARCHAR(71);
UPDATE images
SET delete_token = 'sha256:' || encode(digest(delete_token, 'sha256'), 'hex')
WHERE delete_token NOT LIKE 'sha256:%';
`

// Existing sessions stay at version zero until an explicit revocation.
const migrationV4SQL = `
ALTER TABLE users ADD COLUMN IF NOT EXISTS auth_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS auth_version BIGINT NOT NULL DEFAULT 0;
`

// DB wraps *sql.DB with helper methods.
type DB struct {
	*sql.DB
}

func NewDB(dsn string, maxOpen, maxIdle int, maxLife time.Duration) (*DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(maxLife)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &DB{DB: db}, nil
}

// RunMigrations applies all embedded schemas sequentially.
func (db *DB) RunMigrations() error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("SELECT pg_advisory_xact_lock(706, 2)"); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INT PRIMARY KEY, applied_at TIMESTAMPTZ DEFAULT NOW()
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	migrations := []struct {
		version int
		sql     string
	}{
		{1, migrationV1SQL},
		{2, migrationV2SQL},
		{3, migrationV3SQL},
		{4, migrationV4SQL},
	}

	for _, m := range migrations {
		var exists bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", m.version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		slog.Info("applying migration", slog.Int("version", m.version))
		if _, err := tx.Exec(m.sql); err != nil {
			return fmt.Errorf("apply migration v%d: %w", m.version, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations(version) VALUES($1)", m.version); err != nil {
			return fmt.Errorf("record migration v%d: %w", m.version, err)
		}
	}
	return tx.Commit()
}

// HealthCheck pings the database.
func (db *DB) HealthCheck(ctx context.Context) error {
	return db.PingContext(ctx)
}
