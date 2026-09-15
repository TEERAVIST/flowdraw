# Auth V1 readiness report

Validated on 2026-09-15 from commit `cafc54c` plus the focused race/expiry validation fixes in the current working tree.

## Readiness decision

The repository implementation is complete for the scoped Auth V1 and its security-critical automated validation passes. It is **not production-ready yet** because deployment-specific controls remain unverified: real secrets and persistent signing-key mounts, the final exact Flowdraw hostname/callback, Resend domain verification and SPF/DKIM/DMARC delivery results, trusted Cloudflare client-IP handling, backups/retention, and a smoke test through the live shared ingress.

No Flowdraw product authorization or RBAC is included in this phase.

## Implemented architecture

The public path is Cloudflare -> shared Caddy -> either the Go auth service or the existing Flowdraw frontend/API. Caddy is the only component with published web ports. The Go service joins the external `proxy` network and internal `auth-private` network. `auth-postgres` joins only `auth-private`. Flowdraw's API joins `proxy` and its separate internal `backend` network; Flowdraw PostgreSQL and MinIO join only `backend`.

Auth owns identity, credentials, browser login sessions, recovery challenges, OAuth clients, Fosite protocol state, signing metadata, rate limits, and security events in `auth_db`. Flowdraw owns rooms, objects, product sessions, and future product authorization in `flowdraw_db`. Flowdraw never queries `auth_db`.

## OAuth/OIDC capabilities

ORY Fosite provides Authorization Code, mandatory PKCE S256, OpenID Connect, refresh tokens, introspection used by userinfo, and RFC 7009 revocation. Enabled public endpoints are:

- `GET /oauth2/auth`
- `POST /oauth2/token`
- `POST /oauth2/revoke`
- `GET /.well-known/openid-configuration`
- `GET /.well-known/jwks.json`
- `GET /userinfo`

Implicit, password, client credentials, dynamic client registration, wildcard callbacks, SAML, SCIM, social login, MFA, and passkeys are outside V1. Browser logout uses a CSRF-protected auth endpoint; RP-initiated/front-channel/back-channel OIDC logout is not advertised.

## Session model

The auth service issues a 256-bit opaque `__Host-auth_session` cookie. PostgreSQL stores only SHA-256 digests. The cookie is Secure, HttpOnly, host-only, Path=/, and SameSite=Lax. Login rotates a presented session, sessions expire after 12 hours, and logout/revoke-all update auth-owned session state. Password reset revokes all auth browser sessions atomically.

Flowdraw separately issues `__Host-flowdraw_session` after its backend completes code exchange and validates the ID token. Its 256-bit secret is also digest-only in `flowdraw_db`, Secure, HttpOnly, host-only, Path=/, SameSite=Lax, and expires after 12 hours. Flowdraw logout requires exact Origin plus a session-derived CSRF header. Auth and Flowdraw sessions have separate revocation domains.

## Token model

Authorization codes expire after five minutes and are one-use. Access tokens are opaque and expire after 15 minutes. ID tokens are RS256 JWTs with a stable `kid` and expire after 15 minutes. Refresh tokens expire after 30 days, require `offline_access`, rotate on use, and reject/revoke the family on replay. RFC 7009 revocation is supported.

Flowdraw's backend requests `openid email`, validates state, nonce, PKCE and the signed ID token, then discards the OAuth token response after creating its application session. React receives no authorization code, access token, refresh token, ID token, client secret, or PKCE verifier and stores none in localStorage/sessionStorage.

## Signing keys

The auth container mounts persistent RSA private signing material from `AUTH_KEYS_DIRECTORY`; it does not generate keys at startup. `AUTH_SIGNING_KEY_FILE` selects the active PEM and `AUTH_SIGNING_KEY_ID` supplies its stable kid. JWKS contains only public keys. `AUTH_OVERLAP_JWKS_FILE` supports multiple previous public keys during rotation, while the schema can retain multiple signing-key metadata rows.

The restart test loads the same mounted PEM into a newly constructed provider and confirms that an ID token issued before restart still verifies and retains the same kid. Manual rotation is documented in [auth-architecture.md](auth-architecture.md): mount a new private key under a new kid, retain old public JWKs through token lifetime plus skew/cache allowance, verify issuance/JWKS, then retire old public keys.

## Password and recovery security

Passwords use bounded Argon2id parameters, per-password random salt, constant-time verification, encoded policy versioning, and rehash on successful login when policy changes. Oversized passwords/hashes and unsafe cost parameters are rejected before expensive allocation. Missing-account login performs a dummy Argon2id check.

Verification and reset challenges contain 256 random bits, use URL fragments, and are stored only as SHA-256 digests. Browser pages remove the fragment immediately, load no third-party resources, and POST under CSRF protection. A partial unique index and parent-user locking enforce one active challenge per user/purpose. Consumption and verification/reset transitions are transactional and one-use. Tests cover invalid, expired, consumed, concurrent, and transaction-rollback behavior.

Forgot-password, registration, and verification-resend responses are generic across account existence and email outcomes. They use separate HMAC-derived database rate-limit buckets and bounded timing/work controls. Reset atomically changes the credential, increments its version, revokes auth sessions, and records a security event. Password-change notification is attempted after commit; delivery failure cannot undo the reset.

The Resend adapter uses standard HTTPS with a five-second maximum timeout, no redirect following and no inline retry. It classifies timeout, permanent 4xx, 429, and transient network/5xx failures. Provider-controlled payloads do not enter logs/errors. Automated tests use fake/local senders and send no real email.

## Database ownership and migration behavior

`auth_db` and `flowdraw_db` use separate services, credentials, volumes, and internal networks. Migrations run only through the explicit `auth migrate` command; server startup does not initialize or drop schema. Embedded migration files run in sorted order under a PostgreSQL advisory lock and are recorded transactionally.

Container validation ran all migrations against an empty database, inserted a sentinel user, reran migrations, and confirmed both the sentinel and exactly `001_initial.sql,002_invariants.sql,003_login_handoffs.sql` remained. The production Compose definition uses a persistent named PostgreSQL volume. Integration tests recreate only a database whose name must end in `_test`.

## Network exposure

- Public: shared Caddy publishes TCP 80, TCP 443, and UDP 443.
- Proxy-only exposure: Flowdraw frontend 80, Flowdraw API 3000, auth service 8080.
- Private only: `auth-postgres` 5432 on `auth-private`; Flowdraw PostgreSQL 5432 and MinIO 9000 on `backend`.
- No auth service, database, or MinIO host ports are published by application Compose files.

Caddy validation resolves `auth.k1n3ticnerdcore.tech` to `auth-service:8080`. Flowdraw client registration rejects non-HTTPS, wildcard, query-bearing, fragment-bearing, or non-`/api/auth/callback` values and stores exactly one operator-supplied callback.

## Validation results

- `gofmt`: clean (`gofmt -l .` produced no files).
- `go test ./...`: passed all packages with PostgreSQL and `AUTH_TEST_BFF=1`; 25 named tests.
- `go test -race ./...`: passed all packages and all 25 named tests after fixing a real Fosite lazy-hasher initialization race. The race report was not suppressed.
- `go vet ./...`: passed.
- `go build ./cmd/auth`: passed.
- Node `pnpm test`: 10/10 passed.
- `pnpm lint`: passed.
- `pnpm build`: passed; Vite reported only its existing large-chunk size warning.
- Auth PostgreSQL integration: passed, including concurrent challenge creation/consumption, atomic reset rollback, session rotation/revoke-all/expiry, exact redirects, PKCE failure, authorization-code expiry/replay, refresh rotation/replay, RFC 7009 revocation, invalid/expired recovery challenges, rate-limit isolation, and signing restart.
- Real Node <-> Go OIDC over local TLS: passed login redirect, authorization, S256 exchange, ID-token signature/claim validation, Flowdraw application-session creation, authenticated session lookup, callback replay rejection, and logout.
- Compose: Flowdraw, auth, and gateway configurations parsed successfully; service/network/port inspection matched the boundaries above.
- Caddy: `caddy adapt --validate` passed and the generated route targets `auth-service:8080`. Caddy emitted only a formatting warning for the example file.
- Container builds: `flowdraw-auth` passed from a no-cache build. Flowdraw API and frontend results are recorded after their no-cache dependency fetch completes.

No raw passwords, session secrets, authorization codes, OAuth tokens, reset/verification tokens, client secrets, PKCE verifiers, request bodies, Authorization headers, or collaboration edit secrets are logged by the added auth paths. Auth operational logs contain email purpose and sanitized failure class. Existing generic Flowdraw backend error logging was reviewed; auth callback/protocol failures are consumed and sanitized inside the auth module before reaching it, and collaboration credentials remain in URL fragments rather than requests.

## Remaining risks

### Blockers before production

- Provision strong, stable runtime/database/client/HMAC/session secrets and a persistent readable RSA key mount outside images/repository.
- Replace example Flowdraw hostname and register the exact production callback on both sides.
- Add and verify the Resend transactional domain; confirm real received-message SPF, DKIM and DMARC results. API acceptance alone is insufficient.
- Configure and validate trusted Cloudflare proxy ranges/client-IP propagation at shared Caddy. The current peer-IP header may group users by Cloudflare edge until this is done.
- Run the complete smoke flow through deployed Cloudflare/Caddy/TLS and confirm neither ingress nor platform logging captures sensitive headers, bodies, or callback query strings.
- Establish auth database backup/restore, monitoring/alerting, and retention cleanup for expired sessions/challenges/handoffs/rate buckets.
- Tune and load-test rate/admission/timing controls on production-sized infrastructure.

### Acceptable V1 limitations

- Email has no queue/outbox or durable retry; users request a replacement message after delivery failure.
- First-party scopes are preapproved; there is no general consent UI.
- Flowdraw product sessions are not synchronously revoked by auth logout/password reset; returning through OIDC will re-evaluate auth state.
- Profile scope adds no claims beyond stable subject; Flowdraw currently uses subject and email.
- Signing-key activation and retirement are operator-driven.

### Future improvements

- Add a durable email outbox if delivery availability requires it.
- Add OIDC back-channel logout/session coordination if product-session revocation requirements change.
- Automate expired-record retention and signing-key operational checks.
- Add KMS/HSM-backed signing through the existing key boundary.

These are future options, not authorization to add infrastructure in Auth V1.

## Compatibility changes

Existing room/canvas/WebSocket/object behavior and capability links remain unchanged. Auth integration is opt-in through Flowdraw environment variables; without them, the application continues and `/api/auth/*` reports unavailable. The UI adds sign-in/session/sign-out controls. Enabling auth creates product login/session tables in `flowdraw_db`.

Migration 002 deletes foundation OAuth sessions because storage signatures and serialized request forms were hardened; users with such pre-V1 OAuth state must reauthenticate. It also consumes older duplicate active identity challenges before enforcing uniqueness. Existing room edit capabilities are not converted into account ownership.

## Next phase contract

After successful OIDC validation, Flowdraw receives these identity claims from Auth V1:

- `sub`: stable opaque auth user ID; use this as the product-local principal key.
- `email`: normalized current email, released only under the `email` scope.
- `email_verified`: must be `true` at callback time.
- Standard validation claims: `iss`, `aud`, `exp`, `iat`, `auth_time`, and request-bound `nonce`.

Flowdraw persists only `subject` (`sub`) and `email` in its application session row and returns `{ user: { subject, email }, csrf }` from `/api/auth/session`. The next authorization phase should create product-local user/membership/role rows keyed by `subject`, make authorization decisions entirely in `flowdraw_db`, and never query `auth_db` or use email as the immutable principal identifier.
