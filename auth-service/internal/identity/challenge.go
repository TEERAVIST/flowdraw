package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/config"
	"net/url"
	"time"

	mail "github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/email"
)

type Purpose string

const (
	VerifyEmail   Purpose = "verify_email"
	ResetPassword Purpose = "reset_password"
)

var (
	ErrInvalidChallenge  = errors.New("invalid challenge")
	ErrExpiredChallenge  = errors.New("expired challenge")
	ErrConsumedChallenge = errors.New("challenge already consumed")
)

type Challenge struct {
	ID, UserID string
	Purpose    Purpose
	Digest     [32]byte
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}

func NewToken() (string, [32]byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", [32]byte{}, fmt.Errorf("generate challenge: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, sha256.Sum256([]byte(token)), nil
}

func TokenMatches(token string, digest [32]byte) bool {
	actual := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(actual[:], digest[:]) == 1
}

func fragmentURL(publicURL, path, token string) (string, error) {
	u, err := url.Parse(publicURL)
	if err != nil || config.ValidateHTTPSOrigin(publicURL) != nil {
		return "", errors.New("AUTH_PUBLIC_URL must be an absolute HTTPS URL")
	}
	u.Path = path
	u.RawQuery = ""
	u.Fragment = "token=" + url.QueryEscape(token)
	return u.String(), nil
}

type User struct {
	AuthTime               time.Time
	ID, Email, DisplayName string
	EmailVerified          bool
}
type Store interface {
	FindUserByEmail(context.Context, string) (*User, error)
	CreateChallenge(context.Context, Challenge) error // must invalidate older active challenges for user/purpose atomically
	VerifyEmail(context.Context, [32]byte, time.Time) error
	ResetPassword(context.Context, [32]byte, string, time.Time) (*User, error)
}
type PasswordHasher interface{ Hash(string) (string, error) }
type FailureReporter interface {
	EmailFailure(context.Context, Purpose, error)
}

type Service struct {
	Store     Store
	Sender    mail.Sender
	Hasher    PasswordHasher
	Reporter  FailureReporter
	PublicURL string
	Now       func() time.Time
	TTL       time.Duration
}

func (s Service) create(ctx context.Context, user User, purpose Purpose) (string, string, error) {
	token, digest, err := NewToken()
	if err != nil {
		return "", "", err
	}
	idRaw := make([]byte, 16)
	if _, err = rand.Read(idRaw); err != nil {
		return "", "", err
	}
	idRaw[6] = (idRaw[6] & 0x0f) | 0x40
	idRaw[8] = (idRaw[8] & 0x3f) | 0x80
	now := s.now()
	ttl := s.TTL
	if ttl == 0 {
		ttl = 30 * time.Minute
	}
	id := fmt.Sprintf("%x-%x-%x-%x-%x", idRaw[0:4], idRaw[4:6], idRaw[6:8], idRaw[8:10], idRaw[10:16])
	if err = s.Store.CreateChallenge(ctx, Challenge{ID: id, UserID: user.ID, Purpose: purpose, Digest: digest, ExpiresAt: now.Add(ttl)}); err != nil {
		return "", "", err
	}
	path := "/verify-email"
	if purpose == ResetPassword {
		path = "/reset-password"
	}
	link, err := fragmentURL(s.PublicURL, path, token)
	return id, link, err
}

func (s Service) SendVerification(ctx context.Context, user User) error {
	id, link, err := s.create(ctx, user, VerifyEmail)
	if err != nil {
		return err
	}
	return s.Sender.SendVerificationEmail(ctx, mail.VerificationMessage{To: user.Email, DisplayName: user.DisplayName, URL: link, IdempotencyKey: "verify-" + id})
}

// ForgotPassword always has the same public result. Delivery failures are reported
// out-of-band and never change the response in a way that reveals account existence.
func (s Service) ForgotPassword(ctx context.Context, normalizedEmail string) {
	user, err := s.Store.FindUserByEmail(ctx, normalizedEmail)
	if err != nil || user == nil {
		if err != nil && s.Reporter != nil {
			s.Reporter.EmailFailure(ctx, ResetPassword, err)
		}
		return
	}
	id, link, err := s.create(ctx, *user, ResetPassword)
	if err == nil {
		err = s.Sender.SendPasswordResetEmail(ctx, mail.PasswordResetMessage{To: user.Email, DisplayName: user.DisplayName, URL: link, IdempotencyKey: "reset-" + id})
	}
	if err != nil && s.Reporter != nil {
		s.Reporter.EmailFailure(ctx, ResetPassword, err)
	}
}

func (s Service) Verify(ctx context.Context, token string) error {
	return s.Store.VerifyEmail(ctx, sha256.Sum256([]byte(token)), s.now())
}

func (s Service) Reset(ctx context.Context, token, newPassword string) error {
	hash, err := s.Hasher.Hash(newPassword)
	if err != nil {
		return err
	}
	user, err := s.Store.ResetPassword(ctx, sha256.Sum256([]byte(token)), hash, s.now())
	if err != nil {
		return err
	}
	// The database commit is final even when the notification cannot be delivered.
	digest := sha256.Sum256([]byte(token))
	if err = s.Sender.SendSecurityAlert(ctx, mail.SecurityAlertMessage{To: user.Email, Event: "Your password was changed. All auth browser sessions were revoked.", IdempotencyKey: fmt.Sprintf("security-%x", digest[:16])}); err != nil && s.Reporter != nil {
		s.Reporter.EmailFailure(ctx, ResetPassword, err)
	}
	return nil
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
