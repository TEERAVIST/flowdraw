# Flowdraw centralized Auth V1

## Status

The repository implements the V1 server, browser flows, Fosite provider, Flowdraw backend integration and deployment definitions. **This is not a production-readiness declaration.** Production credentials, signing keys, the exact deployed Flowdraw hostname, Resend domain verification, delivery/header checks and an ingress smoke test must be provisioned and verified before rollout. See [auth-validation.md](auth-validation.md) for the exact local verification results and limitations.

## Architecture and boundaries

```text
Cloudflare -> shared Caddy -> auth-service -> auth-private -> auth-postgres/auth_db
                         \-> Flowdraw SPA
                         \-> flowdraw-api -> backend -> Flowdraw PostgreSQL + MinIO
```

One Go auth service uses its own PostgreSQL database. Existing Flowdraw frontend, Node backend, room/canvas APIs, WebSocket collaboration and object storage keep their architecture. Caddy is the only public ingress. Auth and product databases are never shared. No Redis, worker, queue, Kubernetes or extra microservice is introduced.

`deploy/auth.compose.yml` is an independent Compose project using the shared external `proxy` network. Only auth-service joins `proxy` and the internal `auth-private` network. Auth PostgreSQL joins only `auth-private`, has a persistent named volume and publishes no host ports. Neither does auth-service. The shared gateway example routes `auth.k1n3ticnerdcore.tech` to auth-service:8080. Existing product routes are preserved.

## Browser and identity endpoints

| Endpoint | Behavior |
|---|---|
| GET/POST `/register` | Trim/lowercase email; create unverified user and Argon2id credential atomically; persist verification challenge; attempt email |
| GET/POST `/resend-verification` | Replace outstanding verification challenge for an eligible unverified account |
| GET/POST `/forgot-password` | Generic result for existing/missing accounts and delivery failures |
| GET/POST `/verify-email` | First-party fragment-consuming page and atomic one-time verification |
| GET/POST `/reset-password` | First-party page; atomic password replacement, browser-session revocation and security event; post-commit notification |
| GET/POST `/login` | Verify credential, require active/verified account, rotate presented browser session |
| GET/POST `/logout`, `/revoke-all` | GET displays form; CSRF-protected POST revokes current/all auth browser sessions |
| GET `/health/live`, `/health/ready` | Process liveness; database migration state and loaded signing-key readiness |

All browser POSTs require an exact public Origin and a matching CSRF value from a secure, HttpOnly, host-only `__Host-auth_csrf` cookie and the submitted form. State changes never occur on logout GET. Forms use bounded request bodies; no request/body/access logger is installed.

`__Host-auth_session` has Secure, HttpOnly, Path=/, SameSite=Lax, no Domain, and a 12-hour absolute lifetime. It contains 256 random bits; PostgreSQL stores only its SHA-256 digest. Successful login replaces the presented session. Login checks credential version while holding the user lock, preventing a password reset racing with credential verification from minting a session from an obsolete password. Password rehashing upgrades older accepted Argon2id parameters on successful login. Invalid Argon2id parameters and oversized hashes/passwords are rejected before expensive allocation.

Auth login handoffs store digests of a random browser-cookie value and the authorization request URI, with the original request time and five-minute expiry. Atomic consumption preserves `prompt=login`, `prompt=none` and `max_age` semantics across reauthentication. Only `/oauth2/auth` is an accepted local post-login destination.

### Challenge storage and concurrency

Verification/reset tokens contain 256 random bits encoded with base64url. Only SHA-256 token digests are stored. Links place tokens in `#token=...`, never query strings. The page's first inline nonce-authorized script reads the fragment, immediately calls `history.replaceState`, and places the token in a POST form. Pages load no external resources and use `default-src 'none'`, no-referrer, no-store and frame restrictions.

Every challenge writer/consumer first locks the parent user, then its challenge rows. A partial unique index enforces at most one unconsumed challenge per user/purpose. Concurrent resends supersede previous challenges. Consumption locks and checks purpose, expiration and consumed state. Verification and reset changes occur in the same transaction as consumption. Reset also revokes all auth browser sessions and records the security event; a failed transaction leaves the token usable and all previous state intact.

Registration commits the account/credential before sending. If challenge creation or Resend fails, the account remains unverified and cannot sign in; requesting another verification message is the recovery path. A failed send never rolls back an already committed identity transition. Password-changed notification failure is reported internally and never undoes a successful reset.

### Enumeration, rate limits and availability

Registration, verification resend and password recovery use identical generic public results across account existence and delivery outcomes. They have a six-second response floor and a 5.5-second work deadline; the Resend client is capped at five seconds. Missing-user login still computes a real Argon2id verification using a dummy hash.

Each identity POST route has an independent PostgreSQL fixed-window limiter (10 attempts per 15 minutes per source and, when provided, account). Keys are HMAC-derived with the server secret before hashing for storage. Limits fail closed on database errors. Four concurrent identity operations bound password-hashing and inline-email work. Admission/rate-limit errors depend on load/attempts, not account existence. This is timing mitigation, not a claim of constant-time network behavior; tune limits and response budgets under real deployment load.

Caddy overwrites `X-Auth-Client-IP`; auth accepts it only as a valid IP. The example uses the actual peer address. Behind Cloudflare this may group users by Cloudflare edge. Before production, configure and validate trusted Cloudflare proxy ranges/client-IP handling at the shared gateway, then use its validated client IP. Never trust arbitrary inbound forwarding headers. The shared proxy network is a trust boundary.

## Resend integration

Dependency direction remains auth domain -> `email.Sender` -> Resend adapter. Provider-independent escaped HTML/plaintext templates cover verification, recovery and security notifications. Standard `net/http` calls `https://api.resend.com/emails` with a bounded timeout, per-message idempotency key, no inline retry loop, and redirects disabled.

Internal error categories distinguish timeouts, permanent 4xx/redirect failures, 429 rate limiting and transient 5xx/network failures. Provider-controlled response payloads never become error text. Logs contain only purpose/category, never API keys, Authorization headers, provider bodies, request bodies, raw tokens, callback URLs or session cookies. Automated tests use local HTTP fixtures or fake senders and never send email.

There is no durable email retry/outbox. Users must request a replacement message after delivery failure. API acceptance does not prove inbox delivery.

### DNS and delivery deployment

Use a transactional subdomain, for example `auth.k1n3ticnerdcore.tech`, for reputation isolation. Add the domain in Resend. Copy **the exact SPF and DKIM records Resend provides** into Cloudflare; do not invent records or publish duplicate SPF policies. Add DMARC at the appropriate `_dmarc` name with `p=none` and a controlled reporting mailbox. Validate all legitimate senders before tightening to quarantine/reject. Readiness requires Resend's verified domain status and real received messages with SPF, DKIM and DMARC passing. No DNS or real sends are performed by repository tests.

## Fosite composition and endpoints

The pinned ORY Fosite provider composes:

- `OAuth2AuthorizeExplicitFactory`
- `OAuth2RefreshTokenGrantFactory`
- `OpenIDConnectExplicitFactory`
- `OpenIDConnectRefreshFactory`
- `OAuth2TokenIntrospectionFactory` (internal userinfo validation)
- `OAuth2TokenRevocationFactory`
- `OAuth2PKCEFactory`

Only authorization code, mandatory PKCE S256, OIDC and refresh grants are enabled. No implicit/password/client-credentials grant, dynamic client registration or wildcard redirects. Access tokens are opaque; ID tokens use RS256. Code lifetime is five minutes, access/ID tokens 15 minutes, refresh tokens 30 days. Refresh requires `offline_access`. Each refresh rotates its token, invalidates prior access tokens in the request family, and replay triggers family revocation through Fosite.

| Endpoint | Protocol |
|---|---|
| GET `/oauth2/auth` | Authorization; verified browser login; preapproved registered first-party scopes |
| POST `/oauth2/token` | Confidential client code exchange/refresh |
| POST `/oauth2/revoke` | RFC 7009 revocation |
| GET `/.well-known/openid-configuration` | Discovery |
| GET `/.well-known/jwks.json` | Active and overlapping RSA public keys |
| GET `/userinfo` | Fosite bearer-token validation and scope-filtered current account claims |

`openid`, `email`, `profile` and `offline_access` are accepted client scopes; profile currently adds no claims beyond the stable subject. Logout is the browser POST endpoint, not an advertised RP-initiated/front-channel/back-channel logout protocol.

Fosite implements OAuth/PKCE/JWT validation and token generation. PostgreSQL implements its transaction interface, atomic code invalidation and refresh rotation. OAuth lookup keys are SHA-256 digests, including the OIDC lookup that otherwise receives a raw code. Serialized request forms use a small allowlist, excluding client secrets, codes, refresh/access tokens and PKCE verifiers. Inactive records remain available for replay detection until expiry.

Sources: [pinned Fosite transaction contract](https://github.com/ory/fosite/blob/v0.49.0/storage/transactional.go), [pinned refresh implementation](https://github.com/ory/fosite/blob/v0.49.0/handler/oauth2/flow_refresh.go).

## Signing keys and rotation

Load one RSA PEM private key (PKCS#1 or PKCS#8, at least 2048 bits) using `AUTH_SIGNING_KEY_FILE`. `AUTH_SIGNING_KEY_ID` explicitly selects its stable `kid`; it is never generated on restart. The optional `AUTH_OVERLAP_JWKS_FILE` supplies previous RSA **public** keys. Empty/duplicate kids, private overlap keys and wrong algorithms are rejected. Only public material appears in JWKS. Activation is deployment-controlled; there is no automatic calendar/key-database scheduler.

Rotation procedure:

1. Generate a new private key outside the repository. Use a new stable kid; do not reuse a kid for a different key.
2. Export the old public JWK into the overlap JWKS (retain `kid`, `alg=RS256`, `use=sig`). Keep existing still-needed overlap keys.
3. Mount the new key and overlap file, change active kid, then replace the service. Ensure UID 10001 can read files; private keys should be owned accordingly and mode 0600.
4. Verify discovery/JWKS and a fresh signed-token exchange. Retain old public keys for at least the 15-minute ID-token lifetime plus client cache/clock-skew allowance after the last old-key issue (use at least 30 minutes operationally).
5. Remove retired public keys only after that window. Refresh grants issue fresh ID tokens with the current key.

`AUTH_GLOBAL_SECRET` is a separate stable random HMAC secret for Fosite opaque tokens and rate-limit privacy. Changing it invalidates outstanding OAuth credentials; plan that as a forced reauthentication event. It is not an RSA signing key.

## Flowdraw integration and compatibility

`auth register-client` provisions confidential client `flowdraw`, bcrypt-hashing its client secret, with exactly one HTTPS `/api/auth/callback` URI. The operator supplies the actual hostname; the example hostname must be replaced identically on both sides.

The Node backend uses `openid-client` for discovery, S256, code exchange, state/nonce checks and ID-token validation, including signature verification via `enableNonRepudiationChecks`. Five-minute browser-bound login handoffs are AES-256-GCM encrypted in `flowdraw_db`. The callback consumes them atomically. Tokens are used only in the backend during exchange and discarded; the product does not request offline access.

The backend creates its own 12-hour `__Host-flowdraw_session` with a SHA-256 secret digest in its own database. `/api/auth/session` returns minimal user data and a session-derived CSRF value. `/api/auth/logout` requires exact Origin and `X-CSRF-Token`. React stores only this non-token session view in component memory and displays sign-in/sign-out controls. No access/refresh tokens enter React, localStorage or sessionStorage.

OIDC integration is opt-in via `FLOWDRAW_AUTH_ISSUER`; without it, the existing application continues and auth routes report unavailable. Existing capability-based room authorization remains unchanged. Auth login does **not** convert existing rooms into account-owned resources. Auth password reset/revoke-all and Flowdraw logout operate on their respective browser sessions; existing product sessions and independently issued OAuth credentials are not globally synchronized. Back-channel logout and account-based room authorization are outside this change.

Migration 002 deletes foundation OAuth sessions because their lookup format/serialized payloads changed; it also consumes duplicate active challenges before adding the unique index. Existing foundation OAuth clients must be provisioned with the explicit command and exact deployed callback. The internal reset storage interface now returns the committed recipient for its security notification.

## Configuration and deployment

See `auth-service/.env.example`, `deploy/auth.env.example` and `deploy/.env.example`. No real secrets are committed.

Auth runtime: `AUTH_DATABASE_URL`, `AUTH_ISSUER`, `AUTH_PUBLIC_URL` (identical absolute HTTPS origins), `AUTH_ADDRESS`, `AUTH_COOKIE_SECURE=true`, `AUTH_GLOBAL_SECRET` (base64, at least 32 random bytes), `AUTH_SIGNING_KEY_FILE`, `AUTH_SIGNING_KEY_ID`, optional `AUTH_OVERLAP_JWKS_FILE`, `EMAIL_PROVIDER=resend`, `RESEND_API_KEY`, bare `EMAIL_FROM`, optional `EMAIL_FROM_NAME`.

Provisioning: `FLOWDRAW_CALLBACK_URL`, `FLOWDRAW_CLIENT_SECRET` (32–72 bytes); Compose additionally uses `GHCR_OWNER`, `AUTH_IMAGE_TAG`, `AUTH_POSTGRES_PASSWORD`, `AUTH_KEYS_DIRECTORY`. URL-encode reserved password characters in database URLs. Private-network PostgreSQL uses its own credentials; external database deployments require appropriate TLS.

Flowdraw backend: `FLOWDRAW_AUTH_ISSUER`, `FLOWDRAW_PUBLIC_URL`, matching `FLOWDRAW_CLIENT_SECRET`, `FLOWDRAW_SESSION_KEY` (base64, exactly 32 random bytes). No secret belongs in `VITE_*` variables.

Run locally with Go 1.25+ and environment loaded:

```sh
cd auth-service
go run ./cmd/auth migrate
go run ./cmd/auth register-client
go run ./cmd/auth serve
```

Migrations are explicit, embedded, transactionally recorded and serialized with a PostgreSQL advisory lock. Serving does not migrate. Liveness and readiness are distinct; readiness requires the latest schema and loaded active key. The server uses bounded HTTP/database timeouts and graceful shutdown.

On the deployment host, after provisioning `auth.env`, key mounts and the shared proxy network:

```sh
docker compose --env-file auth.env -f auth.compose.yml pull
docker compose --env-file auth.env -f auth.compose.yml up -d auth-postgres
docker compose --env-file auth.env -f auth.compose.yml run --rm auth-service migrate
docker compose --env-file auth.env -f auth.compose.yml run --rm auth-service register-client
docker compose --env-file auth.env -f auth.compose.yml up -d auth-service
```

Merge the auth virtual host into the **existing shared gateway** Caddyfile and validate/reload it. Configure Flowdraw's four auth variables and replace its API/frontend images. CI tests auth against disposable PostgreSQL and builds an auth image; auth migrations and first deployment remain explicit operations.

Before rollout, establish backup/restore, retention cleanup for expired challenges/sessions/handoffs/rate buckets, monitoring for sanitized email failures/readiness, and load testing for rate/admission limits. Do not enable access logging on callback/auth URLs or configure infrastructure to log request bodies/Authorization headers.
