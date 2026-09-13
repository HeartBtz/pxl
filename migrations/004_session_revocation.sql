-- Additive: existing sessions start at version zero and keep working until revoked.
ALTER TABLE users ADD COLUMN IF NOT EXISTS auth_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS auth_version BIGINT NOT NULL DEFAULT 0;
