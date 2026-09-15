package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"github.com/go-jose/go-jose/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/config"
	email "github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/email"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/storage"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/migrations"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type captureSender struct {
	mu                  sync.Mutex
	verification, reset string
	alerts              int
	fail                bool
}

func (s *captureSender) SendVerificationEmail(_ context.Context, m email.VerificationMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verification = m.URL
	return nil
}
func (s *captureSender) SendPasswordResetEmail(_ context.Context, m email.PasswordResetMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset = m.URL
	return nil
}
func (s *captureSender) SendSecurityAlert(context.Context, email.SecurityAlertMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts++
	if s.fail {
		return email.ErrTransient
	}
	return nil
}
func setup(t *testing.T) (*Server, *captureSender) {
	t.Helper()
	dsn := os.Getenv("AUTH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AUTH_TEST_DATABASE_URL not set; PostgreSQL integration test skipped")
	}
	u, parseErr := url.Parse(dsn)
	if parseErr != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("test database name must end with _test")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	// Tests require a dedicated disposable database; never point this at deployment data.
	if _, err = db.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Run(ctx, db); err != nil {
		t.Fatal("migration rerun", err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keys := Keys{Active: jose.JSONWebKey{Key: key, KeyID: "test-stable-key", Algorithm: "RS256", Use: "sig"}}
	keys.Public.Keys = []jose.JSONWebKey{keys.Active.Public()}
	sender := &captureSender{}
	s, err := New(config.Config{Issuer: "https://auth.example.test", PublicURL: "https://auth.example.test", SessionLifetime: time.Hour, CookieSecure: true}, db, sender, keys, []byte("0123456789012345678901234567890123"))
	if err != nil {
		t.Fatal(err)
	}
	s.ResponseFloor = 0
	hash, _ := bcrypt.GenerateFromPassword([]byte("test-client-secret"), bcrypt.MinCost)
	if _, err = db.Exec(ctx, `INSERT INTO oauth_clients(id,secret_hash,redirect_uris,scopes) VALUES('flowdraw',$1,$2,$3)`, hash, []string{"https://flowdraw.example.test/api/auth/callback"}, []string{"openid", "email", "offline_access"}); err != nil {
		t.Fatal(err)
	}
	return s, sender
}
func request(t *testing.T, h http.Handler, method, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, "https://auth.example.test"+path, body)
	r.RemoteAddr = "192.0.2.1:1234"
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://auth.example.test")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func csrf(t *testing.T, h http.Handler, path string) *http.Cookie {
	t.Helper()
	w := request(t, h, "GET", path, nil)
	for _, c := range w.Result().Cookies() {
		if c.Name == csrfCookie {
			return c
		}
	}
	t.Fatal("missing csrf cookie")
	return nil
}
func post(t *testing.T, h http.Handler, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	c := csrf(t, h, path)
	form.Set("csrf", c.Value)
	return request(t, h, "POST", path, form, append(cookies, c)...)
}
func fragmentToken(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.RawQuery != "" {
		t.Fatal("token query")
	}
	v, _ := url.ParseQuery(u.Fragment)
	return v.Get("token")
}
func registerLogin(t *testing.T, s *Server, sender *captureSender) (*http.Cookie, string) {
	t.Helper()
	h := s.Handler()
	address := "u@example.test"
	password := "correct horse battery staple"
	w := post(t, h, "/register", url.Values{"email": {" U@EXAMPLE.TEST "}, "password": {password}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = post(t, h, "/verify-email", url.Values{"token": {fragmentToken(t, sender.verification)}})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = post(t, h, "/login", url.Values{"email": {address}, "password": {password}})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			if !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteLaxMode {
				t.Fatal("unsafe session cookie")
			}
			return c, address
		}
	}
	t.Fatal("missing session")
	return nil, ""
}
func authorizationValues() url.Values {
	d := sha256.Sum256([]byte(strings.Repeat("v", 43)))
	return url.Values{"client_id": {"flowdraw"}, "redirect_uri": {"https://flowdraw.example.test/api/auth/callback"}, "response_type": {"code"}, "scope": {"openid email offline_access"}, "state": {"state-long-enough"}, "nonce": {"nonce-long-enough"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(d[:])}}
}
func authorizeCode(t *testing.T, s *Server, c *http.Cookie) string {
	t.Helper()
	w := request(t, s.Handler(), "GET", "/oauth2/auth?"+authorizationValues().Encode(), nil, c)
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Query().Get("code") == "" {
		t.Fatalf("authorization: %d %s %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	return u.Query().Get("code")
}
func tokenRequest(t *testing.T, s *Server, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "https://auth.example.test/oauth2/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth("flowdraw", "test-client-secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func exchangeValues(code string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"https://flowdraw.example.test/api/auth/callback"}, "code_verifier": {strings.Repeat("v", 43)}}
}
func tokens(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("token: %d %s", w.Code, w.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestOIDCPostgres(t *testing.T) {
	s, sender := setup(t)
	c, _ := registerLogin(t, s, sender)
	code := authorizeCode(t, s, c)
	v := tokens(t, tokenRequest(t, s, exchangeValues(code)))
	for _, name := range []string{"access_token", "refresh_token", "id_token"} {
		if v[name] == nil {
			t.Fatalf("missing %s", name)
		}
	}
	jwt, err := jose.ParseSigned(v["id_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if jwt.Signatures[0].Header.KeyID != "test-stable-key" {
		t.Fatal("unstable kid")
	}
	payload, err := jwt.Verify(s.Keys.Public.Keys[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	_ = json.Unmarshal(payload, &claims)
	if claims["iss"] != s.Config.Issuer || claims["nonce"] != "nonce-long-enough" || claims["email"] != "u@example.test" {
		t.Fatal("invalid claims", claims)
	}
	r := httptest.NewRequest("GET", "https://auth.example.test/userinfo", nil)
	r.Header.Set("Authorization", "Bearer "+v["access_token"].(string))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("userinfo", w.Code, w.Body.String())
	}
	refreshed := tokens(t, tokenRequest(t, s, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {v["refresh_token"].(string)}}))
	if refreshed["refresh_token"] == v["refresh_token"] {
		t.Fatal("refresh not rotated")
	}
	if w = tokenRequest(t, s, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {v["refresh_token"].(string)}}); w.Code == 200 {
		t.Fatal("replayed refresh accepted")
	}
	if w = tokenRequest(t, s, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshed["refresh_token"].(string)}}); w.Code == 200 {
		t.Fatal("reuse did not revoke family")
	}
	if w = tokenRequest(t, s, exchangeValues(code)); w.Code == 200 {
		t.Fatal("replayed code accepted")
	}
	var leaked bool
	if err = s.DB.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM oauth_sessions WHERE payload::text LIKE '%test-client-secret%' OR payload::text LIKE '%code_verifier%' OR signature=$1)`, code).Scan(&leaked); err != nil || leaked {
		t.Fatal("sensitive storage", err)
	}
	// Fresh family for RFC 7009 revocation.
	v = tokens(t, tokenRequest(t, s, exchangeValues(authorizeCode(t, s, c))))
	r = httptest.NewRequest("POST", "https://auth.example.test/oauth2/revoke", strings.NewReader(url.Values{"token": {v["refresh_token"].(string)}, "token_type_hint": {"refresh_token"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth("flowdraw", "test-client-secret")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("revocation", w.Code)
	}
	if tokenRequest(t, s, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {v["refresh_token"].(string)}}).Code == 200 {
		t.Fatal("revoked refresh accepted")
	}
}
func TestRedirectAndPKCE(t *testing.T) {
	s, sender := setup(t)
	c, _ := registerLogin(t, s, sender)
	for _, redirect := range []string{"https://evil.test/api/auth/callback", "https://flowdraw.example.test/api/auth/callback/", "https://flowdraw.example.test/api/auth/callback?extra=x", "https://flowdraw.example.test.evil.test/api/auth/callback"} {
		q := authorizationValues()
		q.Set("redirect_uri", redirect)
		w := request(t, s.Handler(), "GET", "/oauth2/auth?"+q.Encode(), nil, c)
		if w.Code < 400 || w.Header().Get("Location") != "" {
			t.Fatal("unsafe redirect", redirect, w.Code, w.Header())
		}
	}
	for _, method := range []string{"plain", ""} {
		q := authorizationValues()
		q.Set("code_challenge_method", method)
		w := request(t, s.Handler(), "GET", "/oauth2/auth?"+q.Encode(), nil, c)
		u, _ := url.Parse(w.Header().Get("Location"))
		if u.Query().Get("code") != "" {
			t.Fatal("accepted PKCE", method)
		}
	}
	code := authorizeCode(t, s, c)
	q := exchangeValues(code)
	q.Set("code_verifier", strings.Repeat("x", 43))
	if tokenRequest(t, s, q).Code == 200 {
		t.Fatal("wrong verifier accepted")
	}
}
func TestBrowserSecurityAndReset(t *testing.T) {
	s, sender := setup(t)
	h := s.Handler()
	c, address := registerLogin(t, s, sender)
	for _, path := range []string{"/register", "/login", "/forgot-password", "/resend-verification", "/verify-email", "/reset-password", "/logout", "/revoke-all"} {
		if w := request(t, h, "POST", path, url.Values{"email": {address}}, c); w.Code != 403 {
			t.Fatal(path, w.Code)
		}
	}
	csrfCookieValue := csrf(t, h, "/logout")
	r := httptest.NewRequest("POST", "https://auth.example.test/logout", strings.NewReader(url.Values{"csrf": {csrfCookieValue.Value}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.test")
	r.AddCookie(csrfCookieValue)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin accepted")
	}
	w = request(t, h, "GET", "/reset-password", nil)
	body := w.Body.String()
	if !strings.Contains(body, "history.replaceState") || strings.Contains(body, "src=\"") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("unsafe fragment page")
	}
	existing := post(t, h, "/forgot-password", url.Values{"email": {address}})
	missing := post(t, h, "/forgot-password", url.Values{"email": {"missing@example.test"}})
	if existing.Code != missing.Code || existing.Body.String() != missing.Body.String() {
		t.Fatal("enumeration")
	}
	sender.fail = true
	w = post(t, h, "/reset-password", url.Values{"token": {fragmentToken(t, sender.reset)}, "password": {"replacement password long enough"}})
	if w.Code != 200 || sender.alerts != 1 {
		t.Fatal("reset notification", w.Code, sender.alerts)
	}
	if _, err := s.Store.SessionUser(context.Background(), c.Value); err == nil {
		t.Fatal("reset session survived")
	}
}
func TestChallengeConcurrency(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	u, err := s.Store.Register(ctx, "concurrent@example.test", "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Identity.SendVerification(ctx, *u)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM identity_challenges WHERE user_id=$1 AND consumed_at IS NULL`, u.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	// Obtain the active digest; exactly one transaction may consume it.
	var raw []byte
	_ = s.DB.QueryRow(ctx, `SELECT secret_hash FROM identity_challenges WHERE user_id=$1 AND consumed_at IS NULL`, u.ID).Scan(&raw)
	var d [32]byte
	copy(d[:], raw)
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.Store.VerifyEmail(ctx, d, time.Now()) }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("successful consumers", success)
	}
}

func TestConcurrentCodeExchangeAndSessionRotation(t *testing.T) {
	s, sender := setup(t)
	c, address := registerLogin(t, s, sender)
	code := authorizeCode(t, s, c)
	results := make(chan int, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- tokenRequest(t, s, exchangeValues(code)).Code }()
	}
	wg.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == 200 {
			success++
		}
	}
	if success != 1 {
		t.Fatal("concurrent code successes", success)
	}
	w := post(t, s.Handler(), "/login", url.Values{"email": {address}, "password": {"correct horse battery staple"}}, c)
	if w.Code != 200 {
		t.Fatal("rotation failed", w.Code)
	}
	var next *http.Cookie
	for _, candidate := range w.Result().Cookies() {
		if candidate.Name == sessionCookie {
			next = candidate
		}
	}
	if next == nil || next.Value == c.Value {
		t.Fatal("session not rotated")
	}
	if _, err := s.Store.SessionUser(context.Background(), c.Value); err == nil {
		t.Fatal("old session remains valid")
	}
	if w = post(t, s.Handler(), "/revoke-all", url.Values{}, next); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, err := s.Store.SessionUser(context.Background(), next.Value); err == nil {
		t.Fatal("revoke-all failed")
	}
}
func TestResetRollbackAndExpiry(t *testing.T) {
	s, sender := setup(t)
	c, address := registerLogin(t, s, sender)
	ctx := context.Background()
	_ = post(t, s.Handler(), "/forgot-password", url.Values{"email": {address}})
	token := fragmentToken(t, sender.reset)
	_, err := s.DB.Exec(ctx, `CREATE FUNCTION fail_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test failure'; END $$; CREATE TRIGGER fail_event BEFORE INSERT ON security_events FOR EACH ROW EXECUTE FUNCTION fail_event()`)
	if err != nil {
		t.Fatal(err)
	}
	w := post(t, s.Handler(), "/reset-password", url.Values{"token": {token}, "password": {"replacement password long enough"}})
	if w.Code == 200 {
		t.Fatal("reset committed through failure")
	}
	if _, err = s.Store.SessionUser(ctx, c.Value); err != nil {
		t.Fatal("failed reset revoked session", err)
	}
	_, err = s.DB.Exec(ctx, `DROP TRIGGER fail_event ON security_events`)
	if err != nil {
		t.Fatal(err)
	}
	w = post(t, s.Handler(), "/reset-password", url.Values{"token": {token}, "password": {"replacement password long enough"}})
	if w.Code != 200 {
		t.Fatal("rollback consumed challenge")
	}
	w = post(t, s.Handler(), "/login", url.Values{"email": {address}, "password": {"replacement password long enough"}})
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var next *http.Cookie
	for _, candidate := range w.Result().Cookies() {
		if candidate.Name == sessionCookie {
			next = candidate
		}
	}
	_, err = s.DB.Exec(ctx, `UPDATE auth_sessions SET expires_at=now()-interval '1 second'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.SessionUser(ctx, next.Value); err == nil {
		t.Fatal("expired session accepted")
	}
}

func TestExpiredAuthorizationCodeAndRecoveryChallenges(t *testing.T) {
	s, sender := setup(t)
	clientSession, address := registerLogin(t, s, sender)
	ctx := context.Background()

	code := authorizeCode(t, s, clientSession)
	if _, err := s.DB.Exec(ctx, `UPDATE oauth_sessions SET expires_at=now()-interval '1 second' WHERE kind='authorize_code'`); err != nil {
		t.Fatal(err)
	}
	if response := tokenRequest(t, s, exchangeValues(code)); response.Code == http.StatusOK {
		t.Fatal("expired authorization code accepted")
	}

	_ = post(t, s.Handler(), "/forgot-password", url.Values{"email": {address}})
	resetToken := fragmentToken(t, sender.reset)
	if _, err := s.DB.Exec(ctx, `UPDATE identity_challenges SET expires_at=now()-interval '1 second' WHERE purpose='reset_password' AND consumed_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	response := post(t, s.Handler(), "/reset-password", url.Values{
		"token":    {resetToken},
		"password": {"replacement password long enough"},
	})
	if response.Code == http.StatusOK {
		t.Fatal("expired recovery challenge accepted")
	}

	response = post(t, s.Handler(), "/reset-password", url.Values{
		"token":    {strings.Repeat("x", 43)},
		"password": {"replacement password long enough"},
	})
	if response.Code == http.StatusOK {
		t.Fatal("invalid recovery challenge accepted")
	}
}
func TestIndependentRateLimits(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		ok, err := s.Store.Allow(ctx, "login:source", 10, time.Minute)
		if err != nil || !ok {
			t.Fatal(i, err)
		}
	}
	if ok, err := s.Store.Allow(ctx, "login:source", 10, time.Minute); err != nil || ok {
		t.Fatal("login limit", err)
	}
	for _, bucket := range []string{"register", "recovery", "resend"} {
		if ok, err := s.Store.Allow(ctx, bucket+":source", 10, time.Minute); err != nil || !ok {
			t.Fatal("shared buckets", err)
		}
	}
}

func TestFlowdrawBFFEndToEnd(t *testing.T) {
	if os.Getenv("AUTH_TEST_BFF") != "1" {
		t.Skip("set AUTH_TEST_BFF=1 to run the Node BFF against the real Fosite provider")
	}
	s, sender := setup(t)
	c, _ := registerLogin(t, s, sender)
	hash, _ := bcrypt.GenerateFromPassword([]byte("test-client-secret"+strings.Repeat("!", 14)), bcrypt.MinCost)
	if _, err := s.DB.Exec(context.Background(), `UPDATE oauth_clients SET secret_hash=$1 WHERE id='flowdraw'`, hash); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(s.Handler())
	defer ts.Close()
	s.Config.Issuer = ts.URL
	s.Config.PublicURL = ts.URL
	s.OAuth = Provider(&storage.OAuthStore{DB: s.DB}, ts.URL, s.Secret, s.Keys)
	certPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "../../testdata/oidc-bff.mjs")
	cmd.Env = append(os.Environ(), "AUTH_TEST_ISSUER="+ts.URL, "AUTH_TEST_COOKIE="+c.Name+"="+c.Value, "NODE_EXTRA_CA_CERTS="+certPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("BFF integration failed: %v\n%s", err, out)
	}
}

func TestSigningKeyPersistsAcrossProviderRestart(t *testing.T) {
	s, sender := setup(t)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "active.pem")
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	if err = os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}

	firstKeys, err := LoadKeys(keyPath, "persistent-key", "")
	if err != nil {
		t.Fatal(err)
	}
	s.Keys = firstKeys
	s.OAuth = Provider(&storage.OAuthStore{DB: s.DB}, s.Config.Issuer, s.Secret, firstKeys)
	clientSession, _ := registerLogin(t, s, sender)
	issued := tokens(t, tokenRequest(t, s, exchangeValues(authorizeCode(t, s, clientSession))))
	idToken := issued["id_token"].(string)

	// Recreate the key loader and provider from the same mounted private-key file,
	// matching a normal container restart with the persistent mount unchanged.
	restartedKeys, err := LoadKeys(keyPath, "persistent-key", "")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(s.Config, s.DB, sender, restartedKeys, s.Secret)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jose.ParseSigned(idToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = parsed.Verify(restarted.Keys.Public.Keys[0].Key); err != nil {
		t.Fatal("pre-restart token failed verification after restart", err)
	}
	if parsed.Signatures[0].Header.KeyID != "persistent-key" {
		t.Fatal("signing kid changed across restart")
	}
}

func TestLoginHandoffAndPrompt(t *testing.T) {
	s, sender := setup(t)
	old, address := registerLogin(t, s, sender)
	h := s.Handler()
	q := authorizationValues()
	q.Set("prompt", "login")
	path := "/oauth2/auth?" + q.Encode()
	w := request(t, h, "GET", path, nil, old)
	if !strings.HasPrefix(w.Header().Get("Location"), "/login?") {
		t.Fatal("prompt login ignored", w.Header())
	}
	var handoff *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "__Host-auth_flow" {
			handoff = c
		}
	}
	if handoff == nil {
		t.Fatal("missing login handoff")
	}
	w = post(t, h, "/login", url.Values{"email": {address}, "password": {"correct horse battery staple"}, "return_to": {path}}, old, handoff)
	if w.Code != 303 || w.Header().Get("Location") != path {
		t.Fatal("lost authorization request")
	}
	var current *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			current = c
		}
	}
	w = request(t, h, "GET", path, nil, current, handoff)
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Query().Get("code") == "" {
		t.Fatal("reauthentication failed", w.Header(), w.Body.String())
	}
	w = request(t, h, "GET", path, nil, current, handoff)
	if !strings.HasPrefix(w.Header().Get("Location"), "/login?") {
		t.Fatal("replayed handoff accepted")
	}
	q.Set("prompt", "none")
	w = request(t, h, "GET", "/oauth2/auth?"+q.Encode(), nil)
	u, _ = url.Parse(w.Header().Get("Location"))
	if u.Query().Get("error") != "login_required" {
		t.Fatal("prompt none", w.Header())
	}
}
