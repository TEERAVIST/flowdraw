package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/identity"
)

type IdentityStore struct{ DB *pgxpool.Pool }

func (s IdentityStore) FindUserByEmail(ctx context.Context, email string) (*identity.User, error) {
	var u identity.User
	var verified *time.Time
	err := s.DB.QueryRow(ctx, `SELECT id,email,email_verified_at FROM users WHERE email=$1 AND status='active'`, email).Scan(&u.ID, &u.Email, &verified)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.EmailVerified = verified != nil
	return &u, nil
}

func (s IdentityStore) CreateChallenge(ctx context.Context, c identity.Challenge) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE identity_challenges SET consumed_at=$3 WHERE user_id=$1 AND purpose=$2 AND consumed_at IS NULL`, c.UserID, string(c.Purpose), time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO identity_challenges(id,user_id,purpose,secret_hash,expires_at) VALUES($1,$2,$3,$4,$5)`, c.ID, c.UserID, string(c.Purpose), c.Digest[:], c.ExpiresAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func consume(ctx context.Context, tx pgx.Tx, p identity.Purpose, d [32]byte, now time.Time) (string, error) {
	var userID string
	var expires time.Time
	var consumed *time.Time
	err := tx.QueryRow(ctx, `SELECT user_id,expires_at,consumed_at FROM identity_challenges WHERE purpose=$1 AND secret_hash=$2 FOR UPDATE`, string(p), d[:]).Scan(&userID, &expires, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", identity.ErrInvalidChallenge
	}
	if err != nil {
		return "", err
	}
	if consumed != nil {
		return "", identity.ErrConsumedChallenge
	}
	if !now.Before(expires) {
		return "", identity.ErrExpiredChallenge
	}
	if _, err = tx.Exec(ctx, `UPDATE identity_challenges SET consumed_at=$2 WHERE purpose=$1 AND secret_hash=$3`, string(p), now, d[:]); err != nil {
		return "", err
	}
	return userID, nil
}

func (s IdentityStore) VerifyEmail(ctx context.Context, d [32]byte, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	userID, err := consume(ctx, tx, identity.VerifyEmail, d, now)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET email_verified_at=COALESCE(email_verified_at,$2),updated_at=$2 WHERE id=$1`, userID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s IdentityStore) ResetPassword(ctx context.Context, d [32]byte, passwordHash string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	userID, err := consume(ctx, tx, identity.ResetPassword, d, now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO password_credentials(user_id,password_hash,password_version,changed_at) VALUES($1,$2,1,$3) ON CONFLICT(user_id) DO UPDATE SET password_hash=excluded.password_hash,password_version=password_credentials.password_version+1,changed_at=excluded.changed_at`, userID, passwordHash, now)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, userID, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO security_events(event_type,user_id) VALUES('password_reset', $1)`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var _ identity.Store = IdentityStore{}
