package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/config"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/credential"
	email "github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/email"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/identity"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/storage"
	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/openid"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const sessionCookie = "__Host-auth_session"
const csrfCookie = "__Host-auth_csrf"

type report struct{}

func (report) EmailFailure(_ context.Context, p identity.Purpose, err error) {
	kind := "internal"
	switch {
	case errors.Is(err, email.ErrTimeout):
		kind = "timeout"
	case errors.Is(err, email.ErrPermanent):
		kind = "permanent"
	case errors.Is(err, email.ErrTransient):
		kind = "transient"
	case errors.Is(err, email.ErrRateLimited):
		kind = "rate_limited"
	}
	slog.Warn("authentication email failed", "purpose", string(p), "class", kind)
}

type Hasher struct{}

func (Hasher) Hash(password string) (string, error) {
	return credential.Hash(password, credential.DefaultParameters)
}

type Server struct {
	Config    config.Config
	DB        *pgxpool.Pool
	Store     storage.IdentityStore
	Identity  identity.Service
	OAuth     fosite.OAuth2Provider
	Keys      Keys
	Secret    []byte
	dummyHash string
	slots     chan struct{}
	// ResponseFloor bounds obvious account timing differences beyond the email timeout.
	ResponseFloor time.Duration
}

func New(c config.Config, db *pgxpool.Pool, sender email.Sender, keys Keys, secret []byte) (*Server, error) {
	dummy, err := credential.Hash("dummy credential never used for login", credential.DefaultParameters)
	if err != nil {
		return nil, err
	}
	store := storage.IdentityStore{DB: db}
	s := &Server{Config: c, DB: db, Store: store, Keys: keys, Secret: secret, dummyHash: dummy, slots: make(chan struct{}, 4), ResponseFloor: 6 * time.Second}
	s.Identity = identity.Service{Store: store, Sender: sender, Hasher: Hasher{}, Reporter: report{}, PublicURL: c.PublicURL}
	s.OAuth = Provider(&storage.OAuthStore{DB: db}, c.Issuer, secret, keys)
	return s, nil
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func browserResult(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Flowdraw account</title><h1>` + template.HTMLEscapeString(message) + `</h1><p><a href="/login">Sign in</a> <a href="/resend-verification">Resend verification</a></p></html>`))
		return
	}
	jsonResponse(w, status, map[string]string{"status": message})
}
func failure(w http.ResponseWriter) {
	http.Error(w, "Unable to complete request", http.StatusBadRequest)
}
func cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}
func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	a, err := mail.ParseAddress(value)
	if err != nil || a.Address != value || len(value) > 254 || !strings.Contains(value, "@") {
		return "", errors.New("invalid email")
	}
	return value, nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var ready bool
		err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='003_login_handoffs.sql')`).Scan(&ready)
		if err != nil || !ready || s.Keys.Active.Key == nil {
			http.Error(w, "not ready", 503)
			return
		}
		jsonResponse(w, 200, map[string]string{"status": "ready"})
	})
	for _, path := range []string{"/register", "/login", "/forgot-password", "/resend-verification", "/verify-email", "/reset-password", "/logout", "/revoke-all"} {
		mux.HandleFunc("GET "+path, s.page)
		mux.HandleFunc("POST "+path, s.browserPost)
	}
	mux.HandleFunc("GET /oauth2/auth", s.authorize)
	mux.HandleFunc("POST /oauth2/token", s.token)
	mux.HandleFunc("POST /oauth2/revoke", s.revoke)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, s.Keys.Public) })
	mux.HandleFunc("GET /userinfo", s.userinfo)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		mux.ServeHTTP(w, r)
	})
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Flowdraw account</title>
<script nonce="{{.Nonce}}">const token=new URLSearchParams(location.hash.slice(1)).get('token')||'';history.replaceState(null,'',location.pathname+location.search);addEventListener('DOMContentLoaded',()=>{document.getElementById('token').value=token;});</script></head><body>
<h1>{{.Title}}</h1><form method="post" action="{{.Path}}"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" id="token" name="token"><input type="hidden" name="return_to" value="{{.Return}}">
{{if .Email}}<label>Email <input type="email" name="email" autocomplete="email" required maxlength="254"></label>{{end}}
{{if .Password}}<label>Password <input type="password" name="password" autocomplete="{{.Autocomplete}}" required minlength="12" maxlength="1024"></label>{{end}}
<button type="submit">Continue</button></form><nav><a href="/login">Sign in</a> <a href="/register">Register</a> <a href="/forgot-password">Recover password</a> <a href="/resend-verification">Resend verification</a></nav></body></html>`))

func safeReturn(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.IsAbs() || u.Host != "" || u.Path != "/oauth2/auth" || u.Fragment != "" {
		return "/login"
	}
	return u.String()
}
func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	csrf, _, err := identity.NewToken()
	if err != nil {
		http.Error(w, "Unavailable", 503)
		return
	}
	nonce, _, err := identity.NewToken()
	if err != nil {
		http.Error(w, "Unavailable", 503)
		return
	}
	cookie(w, csrfCookie, csrf, 3600)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	path := r.URL.Path
	auto := "new-password"
	if path == "/login" {
		auto = "current-password"
	}
	_ = pageTemplate.Execute(w, map[string]any{"Nonce": nonce, "CSRF": csrf, "Path": path, "Title": strings.ReplaceAll(strings.TrimPrefix(path, "/"), "-", " "), "Return": safeReturn(r.URL.Query().Get("return_to")), "Email": path == "/register" || path == "/login" || path == "/forgot-password" || path == "/resend-verification", "Password": path == "/register" || path == "/login" || path == "/reset-password", "Autocomplete": auto})
}
func (s *Server) csrfValid(r *http.Request) bool {
	if r.Header.Get("Origin") != s.Config.PublicURL {
		return false
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	a, b := cookieValue(r, csrfCookie), r.PostForm.Get("csrf")
	return len(a) == 43 && len(b) == 43 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func (s *Server) limited(r *http.Request, bucket, account string) bool {
	source, _, _ := net.SplitHostPort(r.RemoteAddr)
	// Caddy overwrites this header; auth-service has no public port.
	if value := r.Header.Get("X-Auth-Client-IP"); net.ParseIP(value) != nil {
		source = value
	}
	keys := []string{"source:" + source}
	if account != "" {
		keys = append(keys, "account:"+account)
	}
	for _, key := range keys {
		mac := hmac.New(sha256.New, s.Secret)
		mac.Write([]byte(bucket + ":" + key))
		ok, err := s.Store.Allow(r.Context(), hex.EncodeToString(mac.Sum(nil)), 10, 15*time.Minute)
		if err != nil || !ok {
			return true
		}
	}
	return false
}
func (s *Server) browserPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		failure(w)
		return
	}
	if !s.csrfValid(r) {
		http.Error(w, "Invalid request", 403)
		return
	}
	path := r.URL.Path
	if path == "/logout" || path == "/revoke-all" {
		if err := s.Store.Logout(r.Context(), cookieValue(r, sessionCookie), path == "/revoke-all"); err != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		cookie(w, sessionCookie, "", -1)
		cookie(w, csrfCookie, "", -1)
		browserResult(w, r, 200, "signed out")
		return
	}
	emailAddress, emailErr := normalizeEmail(r.PostForm.Get("email"))
	if emailErr != nil && path != "/verify-email" && path != "/reset-password" {
		failure(w)
		return
	}
	if s.limited(r, path, emailAddress) {
		http.Error(w, "Try again later", 429)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "Try again later", 429)
		return
	}
	start := time.Now()
	if path == "/register" || path == "/forgot-password" || path == "/resend-verification" {
		defer func() {
			if left := s.ResponseFloor - time.Since(start); left > 0 {
				timer := time.NewTimer(left)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
				}
			}
			browserResult(w, r, 200, "If eligible, an email will arrive. You can request another message later.")
		}()
		// Bound work below the public timing floor, including database and provider calls.
		ctx, cancel := context.WithTimeout(r.Context(), 5500*time.Millisecond)
		defer cancel()
		switch path {
		case "/register":
			hash, err := credential.Hash(r.PostForm.Get("password"), credential.DefaultParameters)
			if err != nil {
				return
			}
			u, err := s.Store.Register(ctx, emailAddress, hash)
			if err == nil && u != nil {
				if err = s.Identity.SendVerification(ctx, *u); err != nil {
					s.Identity.Reporter.EmailFailure(ctx, identity.VerifyEmail, err)
				}
			}
		case "/forgot-password":
			s.Identity.ForgotPassword(ctx, emailAddress)
		case "/resend-verification":
			u, err := s.Store.FindUserByEmail(ctx, emailAddress)
			if err == nil && u != nil && !u.EmailVerified {
				if err = s.Identity.SendVerification(ctx, *u); err != nil {
					s.Identity.Reporter.EmailFailure(ctx, identity.VerifyEmail, err)
				}
			}
		}
		return
	}
	switch path {
	case "/login":
		id, hash, version, err := s.Store.Credential(r.Context(), emailAddress)
		if err != nil {
			hash = s.dummyHash
		}
		ok, verifyErr := credential.Verify(hash, r.PostForm.Get("password"))
		if err != nil || verifyErr != nil || !ok {
			failure(w)
			return
		}
		value, _, err := identity.NewToken()
		if err != nil {
			failure(w)
			return
		}
		rehash := ""
		if credential.NeedsRehash(hash) {
			rehash, err = credential.Hash(r.PostForm.Get("password"), credential.DefaultParameters)
			if err != nil {
				failure(w)
				return
			}
		}
		if err = s.Store.LoginSession(r.Context(), id, version, cookieValue(r, sessionCookie), value, rehash, time.Now().Add(s.Config.SessionLifetime)); err != nil {
			failure(w)
			return
		}
		cookie(w, sessionCookie, value, int(s.Config.SessionLifetime.Seconds()))
		cookie(w, csrfCookie, "", -1)
		target := safeReturn(r.PostForm.Get("return_to"))
		if target == "/login" {
			browserResult(w, r, 200, "signed in")
			return
		}
		http.Redirect(w, r, target, 303)
	case "/verify-email":
		if err := s.Identity.Verify(r.Context(), r.PostForm.Get("token")); err != nil {
			failure(w)
			return
		}
		browserResult(w, r, 200, "verified")
	case "/reset-password":
		if err := s.Identity.Reset(r.Context(), r.PostForm.Get("token"), r.PostForm.Get("password")); err != nil {
			failure(w)
			return
		}
		cookie(w, sessionCookie, "", -1)
		browserResult(w, r, 200, "password changed")
	}
}
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ar, err := s.OAuth.NewAuthorizeRequest(ctx, r)
	if err != nil {
		s.OAuth.WriteAuthorizeError(ctx, w, ar, err)
		return
	}
	u, err := s.Store.SessionUser(ctx, cookieValue(r, sessionCookie))
	requestedAt := ar.GetRequestedAt()
	if value := cookieValue(r, "__Host-auth_flow"); value != "" {
		d := sha256.Sum256([]byte(value))
		requestHash := sha256.Sum256([]byte(r.URL.RequestURI()))
		var original time.Time
		if e := s.DB.QueryRow(ctx, `DELETE FROM auth_login_handoffs WHERE secret_hash=$1 AND request_hash=$2 AND expires_at>now() RETURNING requested_at`, d[:], requestHash[:]).Scan(&original); e == nil {
			requestedAt = original
		}
		cookie(w, "__Host-auth_flow", "", -1)
	}
	needsLogin := err != nil
	if u != nil {
		prompts := strings.Fields(ar.GetRequestForm().Get("prompt"))
		for _, p := range prompts {
			if p == "login" && u.AuthTime.Before(requestedAt) {
				needsLogin = true
			}
		}
		if raw := ar.GetRequestForm().Get("max_age"); raw != "" {
			if age, e := strconv.ParseInt(raw, 10, 32); e == nil && age >= 0 && u.AuthTime.Add(time.Duration(age)*time.Second).Before(requestedAt) {
				needsLogin = true
			}
		}
	}
	if needsLogin {
		if ar.GetRequestForm().Get("prompt") == "none" {
			s.OAuth.WriteAuthorizeError(ctx, w, ar, fosite.ErrLoginRequired)
			return
		}
		value, d, e := identity.NewToken()
		if e != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		requestHash := sha256.Sum256([]byte(r.URL.RequestURI()))
		if _, e = s.DB.Exec(ctx, `INSERT INTO auth_login_handoffs(secret_hash,request_hash,requested_at,expires_at) VALUES($1,$2,$3,now()+interval '5 minutes')`, d[:], requestHash[:], ar.GetRequestedAt()); e != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		cookie(w, "__Host-auth_flow", value, 300)
		http.Redirect(w, r, "/login?return_to="+url.QueryEscape(r.URL.RequestURI()), 303)
		return
	}
	session := openid.NewDefaultSession()
	session.Subject = u.ID
	session.Claims.Subject = u.ID
	session.Claims.AuthTime = u.AuthTime
	session.Claims.RequestedAt = requestedAt
	session.Claims.Extra = map[string]interface{}{}
	for _, scope := range ar.GetRequestedScopes() {
		if ar.GetClient().GetScopes().Has(scope) {
			ar.GrantScope(scope)
		}
	}
	if ar.GetGrantedScopes().Has("email") {
		session.Claims.Extra["email"] = u.Email
		session.Claims.Extra["email_verified"] = u.EmailVerified
	}
	response, err := s.OAuth.NewAuthorizeResponse(ctx, ar, session)
	if err != nil {
		s.OAuth.WriteAuthorizeError(ctx, w, ar, err)
		return
	}
	s.OAuth.WriteAuthorizeResponse(ctx, w, ar, response)
}
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ar, err := s.OAuth.NewAccessRequest(ctx, r, openid.NewDefaultSession())
	if err != nil {
		s.OAuth.WriteAccessError(ctx, w, ar, err)
		return
	}
	response, err := s.OAuth.NewAccessResponse(ctx, ar)
	if err != nil {
		s.OAuth.WriteAccessError(ctx, w, ar, err)
		return
	}
	s.OAuth.WriteAccessResponse(ctx, w, ar, response)
}
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	err := s.OAuth.NewRevocationRequest(r.Context(), r)
	s.OAuth.WriteRevocationResponse(r.Context(), w, err)
}
func (s *Server) discovery(w http.ResponseWriter, r *http.Request) {
	u := s.Config.Issuer
	jsonResponse(w, 200, map[string]any{"issuer": u, "authorization_endpoint": u + "/oauth2/auth", "token_endpoint": u + "/oauth2/token", "revocation_endpoint": u + "/oauth2/revoke", "jwks_uri": u + "/.well-known/jwks.json", "userinfo_endpoint": u + "/userinfo", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic"}, "revocation_endpoint_auth_methods_supported": []string{"client_secret_basic"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"openid", "profile", "email", "offline_access"}, "claims_supported": []string{"sub", "iss", "aud", "exp", "iat", "auth_time", "nonce", "email", "email_verified"}})
}
func (s *Server) userinfo(w http.ResponseWriter, r *http.Request) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "Unauthorized", 401)
		return
	}
	kind, ar, err := s.OAuth.IntrospectToken(r.Context(), strings.TrimPrefix(header, "Bearer "), fosite.AccessToken, openid.NewDefaultSession(), "openid")
	if err != nil || kind != fosite.AccessToken {
		w.Header().Set("WWW-Authenticate", "Bearer error=\"invalid_token\"")
		http.Error(w, "Unauthorized", 401)
		return
	}
	var address string
	var verified bool
	err = s.DB.QueryRow(r.Context(), `SELECT email,email_verified_at IS NOT NULL FROM users WHERE id=$1 AND status='active'`, ar.GetSession().GetSubject()).Scan(&address, &verified)
	if err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	result := map[string]any{"sub": ar.GetSession().GetSubject()}
	if ar.GetGrantedScopes().Has("email") {
		result["email"] = address
		result["email_verified"] = verified
	}
	jsonResponse(w, 200, result)
}
