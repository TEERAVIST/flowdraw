CREATE TABLE auth_login_handoffs (
    secret_hash bytea PRIMARY KEY CHECK (octet_length(secret_hash)=32),
    request_hash bytea NOT NULL,
    requested_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);
