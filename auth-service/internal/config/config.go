package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address         string
	DatabaseURL     string
	Issuer          string
	CookieSecure    bool
	SessionLifetime time.Duration
	SigningKeyFile  string
	SigningKeyID    string
	EmailProvider   string
	ResendAPIKey    string
	EmailFrom       string
	EmailFromName   string
	PublicURL       string
}

func Load() (Config, error) {
	c := Config{
		Address:         env("AUTH_ADDRESS", ":8080"),
		DatabaseURL:     os.Getenv("AUTH_DATABASE_URL"),
		Issuer:          os.Getenv("AUTH_ISSUER"),
		CookieSecure:    envBool("AUTH_COOKIE_SECURE", true),
		SessionLifetime: 12 * time.Hour,
		SigningKeyFile:  os.Getenv("AUTH_SIGNING_KEY_FILE"),
		SigningKeyID:    os.Getenv("AUTH_SIGNING_KEY_ID"),
		EmailProvider:   env("EMAIL_PROVIDER", "resend"),
		ResendAPIKey:    os.Getenv("RESEND_API_KEY"),
		EmailFrom:       os.Getenv("EMAIL_FROM"),
		EmailFromName:   os.Getenv("EMAIL_FROM_NAME"),
		PublicURL:       os.Getenv("AUTH_PUBLIC_URL"),
	}
	for name, value := range map[string]string{
		"AUTH_DATABASE_URL":     c.DatabaseURL,
		"AUTH_ISSUER":           c.Issuer,
		"AUTH_SIGNING_KEY_FILE": c.SigningKeyFile,
		"AUTH_SIGNING_KEY_ID":   c.SigningKeyID,
		"RESEND_API_KEY":        c.ResendAPIKey,
		"EMAIL_FROM":            c.EmailFrom,
		"AUTH_PUBLIC_URL":       c.PublicURL,
	} {
		if value == "" {
			return Config{}, fmt.Errorf("%s is required", name)
		}
	}
	if c.EmailProvider != "resend" {
		return Config{}, fmt.Errorf("unsupported EMAIL_PROVIDER %q", c.EmailProvider)
	}
	if err := ValidateHTTPSOrigin(c.PublicURL); err != nil {
		return Config{}, fmt.Errorf("AUTH_PUBLIC_URL: %w", err)
	}
	if err := ValidateHTTPSOrigin(c.Issuer); err != nil {
		return Config{}, fmt.Errorf("AUTH_ISSUER: %w", err)
	}
	if c.Issuer != c.PublicURL {
		return Config{}, fmt.Errorf("AUTH_ISSUER must equal AUTH_PUBLIC_URL")
	}
	if !c.CookieSecure || (os.Getenv("AUTH_COOKIE_SECURE") != "" && os.Getenv("AUTH_COOKIE_SECURE") != "true") {
		return Config{}, fmt.Errorf("AUTH_COOKIE_SECURE must be true")
	}
	return c, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// ValidateHTTPSOrigin rejects credentials, paths and fragments in the public origin.
func ValidateHTTPSOrigin(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(value, "\r\n\t ") {
		return fmt.Errorf("must be an absolute HTTPS origin without credentials, path, query or fragment")
	}
	return nil
}
