-- Invalidate duplicate foundation challenges before enforcing the invariant.
WITH ranked AS (
 SELECT id, row_number() OVER (PARTITION BY user_id,purpose ORDER BY created_at DESC,id DESC) AS position
 FROM identity_challenges WHERE consumed_at IS NULL
)
UPDATE identity_challenges SET consumed_at=now() WHERE id IN (SELECT id FROM ranked WHERE position>1);
CREATE UNIQUE INDEX identity_challenges_one_active ON identity_challenges(user_id,purpose) WHERE consumed_at IS NULL;
ALTER TABLE identity_challenges ADD CHECK (octet_length(secret_hash)=32);
ALTER TABLE auth_sessions ADD CHECK (octet_length(secret_hash)=32);
-- Foundation records contain unhashed lookup keys and potentially sensitive forms.
-- Explicitly revoke those sessions when migrating to the hardened serialization.
DELETE FROM oauth_sessions;
