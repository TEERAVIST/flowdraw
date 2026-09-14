package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mail "github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/email"
)

type memoryStore struct {
	user              *User
	challenges        []Challenge
	verified, revoked bool
}

func (m *memoryStore) FindUserByEmail(_ context.Context, email string) (*User, error) {
	if m.user != nil && m.user.Email == email {
		u := *m.user
		return &u, nil
	}
	return nil, nil
}
func (m *memoryStore) CreateChallenge(_ context.Context, c Challenge) error {
	for i := range m.challenges {
		if m.challenges[i].UserID == c.UserID && m.challenges[i].Purpose == c.Purpose && m.challenges[i].ConsumedAt == nil {
			n := time.Now()
			m.challenges[i].ConsumedAt = &n
		}
	}
	m.challenges = append(m.challenges, c)
	return nil
}
func (m *memoryStore) consume(p Purpose, d [32]byte, now time.Time) error {
	for i := range m.challenges {
		c := &m.challenges[i]
		if c.Purpose == p && c.Digest == d {
			if c.ConsumedAt != nil {
				return ErrConsumedChallenge
			}
			if !now.Before(c.ExpiresAt) {
				return ErrExpiredChallenge
			}
			c.ConsumedAt = &now
			return nil
		}
	}
	return ErrInvalidChallenge
}
func (m *memoryStore) VerifyEmail(_ context.Context, d [32]byte, n time.Time) error {
	if e := m.consume(VerifyEmail, d, n); e != nil {
		return e
	}
	m.verified = true
	return nil
}
func (m *memoryStore) ResetPassword(_ context.Context, d [32]byte, _ string, n time.Time) error {
	if e := m.consume(ResetPassword, d, n); e != nil {
		return e
	}
	m.revoked = true
	return nil
}

type fakeSender struct {
	reset, verify int
	err           error
	resetURL      string
}

func (f *fakeSender) SendVerificationEmail(_ context.Context, m mail.VerificationMessage) error {
	f.verify++
	return f.err
}
func (f *fakeSender) SendPasswordResetEmail(_ context.Context, m mail.PasswordResetMessage) error {
	f.reset++
	f.resetURL = m.URL
	return f.err
}
func (f *fakeSender) SendSecurityAlert(context.Context, mail.SecurityAlertMessage) error { return nil }

type hasher struct{}

func (hasher) Hash(string) (string, error) { return "hash", nil }

type reporter struct{ called bool }

func (r *reporter) EmailFailure(context.Context, Purpose, error) { r.called = true }

func TestTokenGenerationAndHashing(t *testing.T) {
	a, d, e := NewToken()
	if e != nil || len(a) < 40 || !TokenMatches(a, d) || TokenMatches(a+"x", d) {
		t.Fatal("invalid token behavior")
	}
}
func TestChallengeExpirationInvalidAndConsumed(t *testing.T) {
	now := time.Now()
	user := &User{ID: "u", Email: "u@example.test"}
	m := &memoryStore{user: user}
	token, d, _ := NewToken()
	_ = token
	m.challenges = []Challenge{{UserID: "u", Purpose: VerifyEmail, Digest: d, ExpiresAt: now}}
	if e := m.consume(VerifyEmail, d, now); !errors.Is(e, ErrExpiredChallenge) {
		t.Fatal(e)
	}
	_, other, _ := NewToken()
	if e := m.consume(VerifyEmail, other, now); !errors.Is(e, ErrInvalidChallenge) {
		t.Fatal(e)
	}
	m.challenges[0].ExpiresAt = now.Add(time.Hour)
	if e := m.consume(VerifyEmail, d, now); e != nil {
		t.Fatal(e)
	}
	if e := m.consume(VerifyEmail, d, now); !errors.Is(e, ErrConsumedChallenge) {
		t.Fatal(e)
	}
}
func TestForgotPasswordIsGenericOnMissingAndProviderFailure(t *testing.T) {
	ctx := context.Background()
	fs := &fakeSender{}
	m := &memoryStore{}
	s := Service{Store: m, Sender: fs, PublicURL: "https://auth.example.test", Now: time.Now}
	s.ForgotPassword(ctx, "missing@example.test")
	if fs.reset != 0 {
		t.Fatal("sent for missing user")
	}
	m.user = &User{ID: "u", Email: "u@example.test"}
	fs.err = errors.New("provider down")
	r := &reporter{}
	s.Reporter = r
	s.ForgotPassword(ctx, "u@example.test")
	if fs.reset != 1 || !r.called {
		t.Fatal("failure not safely reported")
	}
	if strings.Contains(fs.resetURL, "?token=") {
		t.Fatal("token leaked into query")
	}
}
func TestVerificationAndResetTransitions(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	u := &User{ID: "u", Email: "u@example.test"}
	m := &memoryStore{user: u}
	s := Service{Store: m, Sender: &fakeSender{}, Hasher: hasher{}, PublicURL: "https://auth.example.test", Now: func() time.Time { return now }}
	verify, vd, _ := NewToken()
	reset, rd, _ := NewToken()
	m.challenges = []Challenge{{UserID: "u", Purpose: VerifyEmail, Digest: vd, ExpiresAt: now.Add(time.Hour)}, {UserID: "u", Purpose: ResetPassword, Digest: rd, ExpiresAt: now.Add(time.Hour)}}
	if e := s.Verify(ctx, verify); e != nil || !m.verified {
		t.Fatalf("verify transition failed: %v", e)
	}
	if e := s.Reset(ctx, reset, "new password"); e != nil || !m.revoked {
		t.Fatalf("reset did not revoke sessions: %v", e)
	}
}
