package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/identity"
	"time"
)

// Register keeps user and credential creation atomic; duplicates are generic upstream.
func (s IdentityStore) Register(ctx context.Context, email, hash string) (*identity.User, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	u := &identity.User{ID: uuid.NewString(), Email: email}
	result, err := tx.Exec(ctx, `INSERT INTO users(id,email) VALUES($1,$2) ON CONFLICT(email) DO NOTHING`, u.ID, email)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() == 0 {
		return nil, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO password_credentials(user_id,password_hash,password_version) VALUES($1,$2,1)`, u.ID, hash); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return u, nil
}
func (s IdentityStore) Credential(ctx context.Context, email string) (string, string, int, error) {
	var id, hash string
	var version int
	err := s.DB.QueryRow(ctx, `SELECT u.id,p.password_hash,p.password_version FROM users u JOIN password_credentials p ON p.user_id=u.id WHERE u.email=$1 AND u.status='active'`, email).Scan(&id, &hash, &version)
	return id, hash, version, err
}
func (s IdentityStore) LoginSession(ctx context.Context, id string, version int, old, token, rehash string, expires time.Time) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var valid bool
	// Parent lock also serializes password reset and session issuance.
	if err = tx.QueryRow(ctx, `SELECT status='active' AND email_verified_at IS NOT NULL FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&valid); err != nil {
		return err
	}
	var current int
	if err = tx.QueryRow(ctx, `SELECT password_version FROM password_credentials WHERE user_id=$1`, id).Scan(&current); err != nil {
		return err
	}
	if !valid || current != version {
		return errors.New("invalid login")
	}
	if rehash != "" {
		if _, err = tx.Exec(ctx, `UPDATE password_credentials SET password_hash=$2 WHERE user_id=$1`, id, rehash); err != nil {
			return err
		}
	}
	if old != "" {
		d := sha256.Sum256([]byte(old))
		if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=now() WHERE secret_hash=$1 AND revoked_at IS NULL`, d[:]); err != nil {
			return err
		}
	}
	d := sha256.Sum256([]byte(token))
	if _, err = tx.Exec(ctx, `INSERT INTO auth_sessions(id,user_id,secret_hash,expires_at) VALUES($1,$2,$3,$4)`, uuid.NewString(), id, d[:], expires); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s IdentityStore) SessionUser(ctx context.Context, token string) (*identity.User, error) {
	d := sha256.Sum256([]byte(token))
	var u identity.User
	err := s.DB.QueryRow(ctx, `SELECT u.id,u.email,u.email_verified_at IS NOT NULL,s.created_at FROM auth_sessions s JOIN users u ON u.id=s.user_id WHERE s.secret_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND u.status='active' AND u.email_verified_at IS NOT NULL`, d[:]).Scan(&u.ID, &u.Email, &u.EmailVerified, &u.AuthTime)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
func (s IdentityStore) Logout(ctx context.Context, token string, all bool) error {
	d := sha256.Sum256([]byte(token))
	if !all {
		_, err := s.DB.Exec(ctx, `UPDATE auth_sessions SET revoked_at=now() WHERE secret_hash=$1 AND revoked_at IS NULL`, d[:])
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `SELECT u.id FROM users u JOIN auth_sessions s ON s.user_id=u.id WHERE s.secret_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() FOR UPDATE OF u`, d[:]).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Allow uses independent fixed-window buckets, with atomic increment under contention.
func (s IdentityStore) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	d := sha256.Sum256([]byte(key))
	var attempts int
	err := s.DB.QueryRow(ctx, `INSERT INTO rate_limits(bucket_key,window_started_at,attempts) VALUES($1,now(),1) ON CONFLICT(bucket_key) DO UPDATE SET attempts=CASE WHEN rate_limits.window_started_at <= now()-$2::interval THEN 1 ELSE rate_limits.attempts+1 END, window_started_at=CASE WHEN rate_limits.window_started_at <= now()-$2::interval THEN now() ELSE rate_limits.window_started_at END RETURNING attempts`, d[:], window.String()).Scan(&attempts)
	return attempts <= limit, err
}
