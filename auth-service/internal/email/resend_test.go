package email

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

func TestFailurePayloadNeverEscapes(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{{400, ErrPermanent}, {401, ErrPermanent}, {429, ErrRateLimited}, {500, ErrTransient}, {503, ErrTransient}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"name":"sensitive-token-payload"}`))
		}))
		s, _ := NewResendSender("secret", "auth@example.test", "", server.Client())
		s.endpoint = server.URL
		err := s.SendSecurityAlert(context.Background(), SecurityAlertMessage{To: "u@example.test"})
		server.Close()
		if !errors.Is(err, tc.want) {
			t.Fatalf("status %d: %v", tc.status, err)
		}
	}
}
func TestSenderValidationAndRedirect(t *testing.T) {
	for _, value := range []string{"invalid", "Name <auth@example.test>", "a@example.test\r\nBcc: x@example.test"} {
		if _, err := NewResendSender("secret", value, "", nil); err == nil {
			t.Fatal("accepted invalid sender")
		}
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed redirect") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	s, _ := NewResendSender("secret", "auth@example.test", "", server.Client())
	s.endpoint = server.URL
	if !errors.Is(s.SendSecurityAlert(context.Background(), SecurityAlertMessage{}), ErrPermanent) {
		t.Fatal("redirect not rejected")
	}
}

func TestProviderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 20 * time.Millisecond
	sender, err := NewResendSender("secret", "auth@example.test", "", client)
	if err != nil {
		t.Fatal(err)
	}
	sender.endpoint = server.URL
	if err = sender.SendSecurityAlert(context.Background(), SecurityAlertMessage{}); !errors.Is(err, ErrTimeout) {
		t.Fatal("timeout classification", err)
	}
}
