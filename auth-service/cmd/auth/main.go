package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/config"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/email"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/httpserver"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/migrations"
	"golang.org/x/crypto/bcrypt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return errors.New("usage: auth serve|migrate|register-client|health-live|health-ready")
	}
	command := os.Args[1]
	if command == "health-live" || command == "health-ready" {
		path := "live"
		if command == "health-ready" {
			path = "ready"
		}
		client := http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/health/" + path)
		if err != nil {
			return errors.New("health check failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return errors.New("health check failed")
		}
		return nil
	}
	if command != "serve" && command != "migrate" && command != "register-client" {
		return errors.New("unknown command")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dbURL := os.Getenv("AUTH_DATABASE_URL")
	if dbURL == "" {
		return errors.New("AUTH_DATABASE_URL is required")
	}
	poolConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	poolConfig.MaxConns = 12
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	poolConfig.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	db, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("database initialization failed")
	}
	defer db.Close()
	if command == "migrate" {
		if err = migrations.Run(ctx, db); err != nil {
			return errors.New("database migration failed")
		}
		fmt.Println("migrations applied")
		return nil
	}
	if command == "register-client" {
		callback := os.Getenv("FLOWDRAW_CALLBACK_URL")
		u, err := url.Parse(callback)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Path != "/api/auth/callback" || strings.Contains(callback, "*") {
			return errors.New("FLOWDRAW_CALLBACK_URL must be an exact HTTPS /api/auth/callback URL")
		}
		secret := os.Getenv("FLOWDRAW_CLIENT_SECRET")
		if len(secret) < 32 || len(secret) > 72 {
			return errors.New("FLOWDRAW_CLIENT_SECRET must contain 32 to 72 bytes")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
		if err != nil {
			return errors.New("client secret hashing failed")
		}
		_, err = db.Exec(ctx, `INSERT INTO oauth_clients(id,secret_hash,redirect_uris,scopes) VALUES('flowdraw',$1,$2,$3) ON CONFLICT(id) DO UPDATE SET secret_hash=excluded.secret_hash,redirect_uris=excluded.redirect_uris,scopes=excluded.scopes,enabled=true`, hash, []string{callback}, []string{"openid", "profile", "email", "offline_access"})
		if err != nil {
			return errors.New("client registration failed")
		}
		fmt.Println("Flowdraw client registered")
		return nil
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	secret, err := base64.StdEncoding.DecodeString(os.Getenv("AUTH_GLOBAL_SECRET"))
	if err != nil || len(secret) < 32 {
		return errors.New("AUTH_GLOBAL_SECRET must be base64 encoding of at least 32 random bytes")
	}
	keys, err := httpserver.LoadKeys(c.SigningKeyFile, c.SigningKeyID, os.Getenv("AUTH_OVERLAP_JWKS_FILE"))
	if err != nil {
		return errors.New("invalid signing key configuration")
	}
	sender, err := email.NewResendSender(c.ResendAPIKey, c.EmailFrom, c.EmailFromName, nil)
	if err != nil {
		return err
	}
	handler, err := httpserver.New(c, db, sender, keys, secret)
	if err != nil {
		return errors.New("HTTP initialization failed")
	}
	server := &http.Server{Addr: c.Address, Handler: handler.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP server stopped")
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			return errors.New("HTTP shutdown failed")
		}
	}
	return nil
}
