package email

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResendSender(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing auth")
		}
		if r.Header.Get("Idempotency-Key") != "verify-1" {
			t.Error("missing idempotency key")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["from"] != `"Flowdraw Auth" <auth@example.test>` {
			t.Errorf("unexpected from: %v", body["from"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s, _ := NewResendSender("secret", "auth@example.test", "Flowdraw Auth", server.Client())
	s.endpoint = server.URL
	if err := s.SendVerificationEmail(context.Background(), VerificationMessage{To: "u@example.test", URL: "https://auth.example.test/verify#token=x", IdempotencyKey: "verify-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestResendFailureClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"name":"rate_limit_exceeded"}`))
	}))
	defer server.Close()
	s, _ := NewResendSender("secret", "auth@example.test", "", server.Client())
	s.endpoint = server.URL
	err := s.SendPasswordResetEmail(context.Background(), PasswordResetMessage{To: "u@example.test", URL: "https://example.test/#token=x"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limit, got %v", err)
	}
}
