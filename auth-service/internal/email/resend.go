package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

var ErrRateLimited = errors.New("email provider rate limited")

type ProviderError struct {
	Status int
	Type   string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("email provider rejected request (status %d, type %q)", e.Status, e.Type)
}

type ResendSender struct {
	apiKey, from, endpoint string
	client                 *http.Client
}

func NewResendSender(apiKey, fromAddress, fromName string, client *http.Client) (*ResendSender, error) {
	if strings.TrimSpace(apiKey) == "" || strings.TrimSpace(fromAddress) == "" {
		return nil, errors.New("Resend API key and sender address are required")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	from := (&mail.Address{Name: strings.TrimSpace(fromName), Address: strings.TrimSpace(fromAddress)}).String()
	return &ResendSender{apiKey: apiKey, from: from, endpoint: "https://api.resend.com/emails", client: client}, nil
}

func (s *ResendSender) SendVerificationEmail(ctx context.Context, m VerificationMessage) error {
	return s.send(ctx, m.To, m.IdempotencyKey, verificationContent(m))
}
func (s *ResendSender) SendPasswordResetEmail(ctx context.Context, m PasswordResetMessage) error {
	return s.send(ctx, m.To, m.IdempotencyKey, resetContent(m))
}
func (s *ResendSender) SendSecurityAlert(ctx context.Context, m SecurityAlertMessage) error {
	return s.send(ctx, m.To, m.IdempotencyKey, alertContent(m))
}

func (s *ResendSender) send(ctx context.Context, to, idempotencyKey string, c content) error {
	body, err := json.Marshal(map[string]any{"from": s.from, "to": []string{to}, "subject": c.subject, "text": c.text, "html": c.html})
	if err != nil {
		return fmt.Errorf("encode email: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create email request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "flowdraw-auth/1.0")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	var p struct {
		Type string `json:"name"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&p)
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%w: %s", ErrRateLimited, p.Type)
	}
	return &ProviderError{Status: resp.StatusCode, Type: p.Type}
}
