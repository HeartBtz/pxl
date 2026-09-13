-- Store delete capabilities as one-way hashes. Existing raw client tokens
-- continue to work because the service hashes the presented value.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
ALTER TABLE images ALTER COLUMN delete_token TYPE VARCHAR(71);
UPDATE images
SET delete_token = 'sha256:' || encode(digest(delete_token, 'sha256'), 'hex')
WHERE delete_token NOT LIKE 'sha256:%';
