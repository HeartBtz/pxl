-- PXL Schema v1
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

--------------------------------------------------------------------------
-- Users
--------------------------------------------------------------------------
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

--------------------------------------------------------------------------
-- Images
--------------------------------------------------------------------------
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

--------------------------------------------------------------------------
-- Albums
--------------------------------------------------------------------------
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

--------------------------------------------------------------------------
-- Album ↔ Image join table
--------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS album_images (
    album_id  UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    image_id  UUID NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    position  INT  NOT NULL DEFAULT 0,
    added_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (album_id, image_id)
);
