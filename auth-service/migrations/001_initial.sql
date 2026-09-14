CREATE TABLE IF NOT EXISTS users (
    id text PRIMARY KEY,
    email text NOT NULL UNIQUE,
    email_verified_at timestamptz,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS password_credentials (
    user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    password_hash text NOT NULL,
    password_version integer NOT NULL,
    changed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS auth_sessions (
    id uuid PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    secret_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS auth_sessions_user_active_idx ON auth_sessions(user_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS identity_challenges (
    id uuid PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose text NOT NULL CHECK (purpose IN ('verify_email', 'reset_password')),
    secret_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS oauth_clients (
    id text PRIMARY KEY,
    secret_hash bytea NOT NULL,
    redirect_uris text[] NOT NULL,
    grant_types text[] NOT NULL DEFAULT ARRAY['authorization_code', 'refresh_token'],
    response_types text[] NOT NULL DEFAULT ARRAY['code'],
    scopes text[] NOT NULL DEFAULT ARRAY['openid', 'profile', 'email'],
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS oauth_sessions (
    kind text NOT NULL,
    signature text NOT NULL,
    request_id text NOT NULL,
    payload jsonb NOT NULL,
    active boolean NOT NULL DEFAULT true,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, signature)
);
CREATE INDEX IF NOT EXISTS oauth_sessions_request_idx ON oauth_sessions(kind, request_id);

CREATE TABLE IF NOT EXISTS oauth_client_jtis (
    jti text PRIMARY KEY,
    expires_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS signing_keys (
    kid text PRIMARY KEY,
    algorithm text NOT NULL,
    public_jwk jsonb NOT NULL,
    not_before timestamptz NOT NULL,
    not_after timestamptz NOT NULL,
    retired_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS security_events (
    id bigserial PRIMARY KEY,
    event_type text NOT NULL,
    user_id text REFERENCES users(id) ON DELETE SET NULL,
    client_id text,
    source_hash bytea,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS security_events_user_time_idx ON security_events(user_id, occurred_at DESC);

CREATE TABLE IF NOT EXISTS rate_limits (
    bucket_key bytea PRIMARY KEY,
    window_started_at timestamptz NOT NULL,
    attempts integer NOT NULL
);
