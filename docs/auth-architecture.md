# Central authentication platform design

## Decision

The platform authentication provider is one independently deployable Go application backed by its own PostgreSQL database. It is exposed only through the shared Caddy ingress at `auth.k1n3ticnerdcore.tech`. Product applications integrate through OAuth 2.0/OpenID Connect over HTTPS and never read `auth_db`.

```text
Cloudflare -> shared Caddy -> auth-service -> auth-private -> auth-postgres/auth_db
                         \-> Flowdraw     -> flowdraw-private -> flowdraw-postgres + MinIO
```

The shared external `proxy` network contains Caddy, `auth-service`, Flowdraw frontend, and `flowdraw-api`. Each product owns a separate internal network and separate database credentials.

## Fosite evaluation

ORY Fosite is suitable as the OAuth/OIDC protocol engine. Its current composed handlers cover authorization code, PKCE, OIDC authorization-code behavior, refresh grants, and token revocation. Fosite validates protocol requests and produces protocol responses and errors. It deliberately does not provide production storage or an identity/login product.

Fosite owns:

- OAuth authorization and token request validation
- exact registered redirect matching
- authorization-code binding and one-time use
- PKCE verification
- scope and audience processing
- refresh-token grant mechanics and rotation/revocation behavior
- OAuth/OIDC protocol errors
- ID/access/refresh token generation through configured strategies
- RFC 7009 revocation request semantics

The auth application owns:

- users, normalized email addresses, status, and verification state
- Argon2id credentials and versioned hashing parameters
- browser login sessions, CSRF, consent policy, and logout
- recovery and verification challenges plus email delivery integration
- registered clients and hashed confidential-client secrets
- PostgreSQL implementations of Fosite storage interfaces
- discovery, JWKS, userinfo HTTP presentation, key activation/rotation policy
- rate limiting, security events, and audit retention

Fosite examples and memory storage are not production persistence and must not be copied as such.

## V1 flows

- Authorization Code with mandatory PKCE (`S256`)
- OpenID Connect scopes `openid`, `profile`, and `email`
- confidential first-party BFF clients
- refresh tokens and RFC 7009 revocation
- no implicit flow, password grant, dynamic public registration, or wildcard redirect URI

Flowdraw is registered as a confidential client with one exact callback URI. Flowdraw's backend performs the code exchange and owns a host-only application session cookie. Access and refresh tokens never enter React local storage.

## Sessions and cookies

The auth browser session is an opaque random value. PostgreSQL stores only its SHA-256 digest, user, creation/expiry timestamps, last-use time, and revocation state. The cookie is `__Host-auth_session`, `Secure`, `HttpOnly`, `Path=/`, and `SameSite=Lax`. Flowdraw later creates its own unrelated `__Host-` session cookie after the callback.

Session revocation and revoke-all are database updates. SSO is achieved by redirecting to auth; no wildcard-domain cookie is used.

## Passwords and challenges

Passwords use `golang.org/x/crypto/argon2` Argon2id with a per-password random salt and a versioned encoded parameter string. Verification applies configured resource limits before allocating memory. Successful login rehashes when the stored parameters are older than current policy.

Email verification and password recovery use 256-bit random, base64url one-time challenges. Only SHA-256 digests are stored. Creating a challenge invalidates older active challenges for the same user and purpose. Challenge consumption and its state transition are one PostgreSQL transaction: verification marks the email verified; reset changes the password, revokes every active browser session, and records a security event. Public forgot-password responses are identical for existing and absent users.

The auth domain depends on `email.Sender`, not Resend. The V1 adapter sends both plain-text and escaped HTML content to Resend's HTTPS API with a five-second timeout and per-challenge idempotency key. Templates are kept in Go and cover address verification, password reset, and password/security notification. Raw challenge tokens appear only in the URL fragment (`#token=...`), which browsers do not send in HTTP requests. The first-party verification/reset page must read the fragment, immediately remove it with `history.replaceState`, load no third-party resources beforehand, and submit the token in a redacted request body. Request bodies and authorization headers must never be logged.

### Email delivery failure semantics

- Configuration is rejected at startup when the provider, API key, sender address, or public URL is invalid/missing.
- Network failure and timeout return a classified internal delivery failure; there is no inline retry.
- Resend 4xx responses are permanent request/configuration failures and require operator correction.
- Resend 429 responses are classified separately. The service does not sleep/retry in an interactive request; Resend publishes `Retry-After`, but durable deferred delivery needs a future queue/outbox decision.
- Resend 5xx responses are transient provider failures, but are not blindly retried during the request.
- Registration/resend can report delivery failure without changing PostgreSQL truth. Forgot-password always returns its generic public response and reports a sanitized operational error out-of-band.

Until an outbox/worker exists, a stored challenge whose send failed can be superseded safely by requesting another message. This is a known availability limitation, not a reason to add a queue in V1.

Email-generating endpoints require independent database/local limiter buckets for registration, verification resend, and password recovery, keyed by privacy-preserving hashes of normalized account and source identifiers. The HTTP endpoints are not implemented yet, so this enforcement remains a release blocker.

### Resend sender-domain onboarding

Prefer a transactional subdomain (for example `auth.k1n3ticnerdcore.tech`) to isolate sending reputation. Add that domain in Resend and copy the exact SPF and DKIM records Resend supplies into Cloudflare DNS; do not invent or duplicate SPF records. Publish DMARC at the appropriate `_dmarc` name, initially with monitoring policy (`p=none`) and a controlled aggregate-report mailbox, validate all legitimate senders, then move gradually to `quarantine` or `reject`. Sending is not operationally ready until Resend reports the domain verified and real delivery/header checks show SPF, DKIM, and DMARC passing.

Runtime configuration is `EMAIL_PROVIDER=resend`, `RESEND_API_KEY`, `EMAIL_FROM`, `EMAIL_FROM_NAME`, and `AUTH_PUBLIC_URL`. Keep the API key in Docker secrets/environment management, never in the repository or image.

## Tokens and keys

OIDC signing uses asymmetric keys. Private keys remain in auth-service secret storage; public keys are returned by JWKS. Every key has a stable `kid`, algorithm, creation time, activation interval, and retirement interval. One key signs at a time; previous public keys remain published until every token they signed has expired plus clock skew.

V1 supports file-mounted PEM keys and metadata configured through secrets. Rotation adds a new key, activates it, retains the previous verification key, then removes retired material only after the overlap window. A managed KMS/HSM can later replace the key loader without changing clients.

## Database boundary

`auth_db` contains users, credentials, sessions, challenges, OAuth clients, Fosite request/token sessions, signing-key metadata, rate-limit buckets, and audit/security events. It contains no Flowdraw room, canvas, or permission records. `flowdraw_db` stores product authorization such as `usr_123` being an editor of a room.

## Security-sensitive custom code

The following requires focused tests and review:

1. Fosite PostgreSQL serialization and transactional one-time code consumption.
2. Login-to-authorization handoff and CSRF/state preservation.
3. Argon2id parsing, resource caps, comparison, and rehash policy.
4. Recovery/verification challenge generation and consumption.
5. Cookie and forwarded-origin handling behind trusted Caddy only.
6. Signing-key selection, JWKS overlap, and rotation.
7. Client-secret hashing and exact redirect registration.

No OAuth grant, PKCE verifier, JWT signature, or protocol error logic is implemented independently when Fosite provides it.

## Operational model

- `/health/live` checks only the Go process.
- `/health/ready` checks `auth_db` and active signing-key availability.
- Database migrations run as an explicit one-shot deployment command before the service update.
- JSON structured logs exclude credentials, session values, codes, and tokens.
- A local bounded login/registration/recovery limiter is acceptable for the initial single instance. Its interface can later use a distributed implementation without changing OAuth semantics.
- Only Caddy publishes host ports. Neither auth-service nor auth-postgres publishes a host port.

## Deliberately excluded from V1

Redis, queues, Kafka, Kubernetes, SAML, SCIM, social providers, passkeys, MFA, public dynamic client registration, impersonation, and centralized product authorization.

## Production-readiness status

This auth service is **not production-ready**. Password hashing, the Fosite PostgreSQL storage foundation, the Resend boundary, and transactional challenge storage exist. Still required are the runnable server and migrations command, registration/login/reset/verification HTTP handlers and safe fragment-consuming pages, endpoint rate limiting, browser sessions and CSRF, complete Fosite provider composition, authorization/token/revocation endpoints, discovery, JWKS, userinfo, signing-key loading and rotation, Flowdraw client/session integration, Docker/Caddy definitions, and end-to-end OIDC/security tests.
