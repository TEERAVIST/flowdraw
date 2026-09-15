import { createCipheriv, createDecipheriv, createHash, randomBytes, timingSafeEqual } from 'node:crypto';
import * as oidc from 'openid-client';

const sessionName = '__Host-flowdraw_session';
const loginName = '__Host-flowdraw_login';
const digest = (value) => createHash('sha256').update(value || '').digest();
const random = () => randomBytes(32).toString('base64url');
const getCookie = (request, name) => (request.headers.cookie || '').split(';').map(v => v.trim()).find(v => v.startsWith(`${name}=`))?.slice(name.length + 1) || '';
const cookie = (name, value, age) => `${name}=${value}; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=${age}`;
const csrfFor = (value) => createHash('sha256').update(`flowdraw-csrf:${value}`).digest('base64url');
const equal = (a, b) => typeof a === 'string' && typeof b === 'string' && a.length === b.length && timingSafeEqual(Buffer.from(a), Buffer.from(b));
const json = (res, status, value) => { res.writeHead(status, { 'content-type': 'application/json', 'cache-control': 'no-store' }); res.end(JSON.stringify(value)); };

export function authSettings(env) {
  if (!env.FLOWDRAW_AUTH_ISSUER) return null;
  const issuer = new URL(env.FLOWDRAW_AUTH_ISSUER);
  const origin = new URL(env.FLOWDRAW_PUBLIC_URL);
  for (const u of [issuer, origin]) {
    if (u.protocol !== 'https:' || u.username || u.password || u.pathname !== '/' || u.search || u.hash) throw new Error('Auth URLs must be HTTPS origins');
  }
  const key = Buffer.from(env.FLOWDRAW_SESSION_KEY || '', 'base64');
  if (key.length !== 32 || !env.FLOWDRAW_CLIENT_SECRET || env.FLOWDRAW_CLIENT_SECRET.length < 32) throw new Error('Auth session key and client secret are required');
  return { issuer, origin: origin.origin, callback: `${origin.origin}/api/auth/callback`, key, secret: env.FLOWDRAW_CLIENT_SECRET };
}

export function seal(key, data) {
  const iv = randomBytes(12);
  const cipher = createCipheriv('aes-256-gcm', key, iv);
  const ciphertext = Buffer.concat([cipher.update(JSON.stringify(data)), cipher.final()]);
  return Buffer.concat([iv, cipher.getAuthTag(), ciphertext]);
}
export function unseal(key, data) {
  const cipher = createDecipheriv('aes-256-gcm', key, data.subarray(0, 12));
  cipher.setAuthTag(data.subarray(12, 28));
  return JSON.parse(Buffer.concat([cipher.update(data.subarray(28)), cipher.final()]).toString());
}

// Product sessions live only in Flowdraw's database. OIDC tokens never leave this backend.
export function createAuth({ pool, env = process.env, protocol = oidc }) {
  const settings = authSettings(env);
  let configuration;
  async function config() {
    if (!configuration) {
      configuration = protocol.discovery(settings.issuer, 'flowdraw', { client_secret: settings.secret, id_token_signed_response_alg: 'RS256' }, protocol.ClientSecretBasic(settings.secret), { timeout: 5 })
        .then(value => { protocol.enableNonRepudiationChecks(value); return value; })
        .catch(() => { configuration = undefined; throw new Error('Authentication service unavailable'); });
    }
    return configuration;
  }
  async function initialize() {
    if (!settings) return;
    await pool.query(`
      CREATE TABLE IF NOT EXISTS app_auth_logins (
        secret_hash bytea PRIMARY KEY, payload bytea NOT NULL, expires_at timestamptz NOT NULL
      );
      CREATE TABLE IF NOT EXISTS app_auth_sessions (
        secret_hash bytea PRIMARY KEY, subject text NOT NULL, email text,
        expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
      );
    `);
  }
  async function handle(request, response, url) {
    if (!url.pathname.startsWith('/api/auth/')) return false;
    response.setHeader('Cache-Control', 'no-store');
    response.setHeader('Referrer-Policy', 'no-referrer');
    response.setHeader('X-Content-Type-Options', 'nosniff');
    if (!settings) { json(response, 503, { error: 'Authentication is not configured' }); return true; }
    try {
      if (request.method === 'GET' && url.pathname === '/api/auth/login') {
        const c = await config();
        const verifier = protocol.randomPKCECodeVerifier();
        const state = protocol.randomState();
        const nonce = protocol.randomNonce();
        const value = random();
        const target = protocol.buildAuthorizationUrl(c, { redirect_uri: settings.callback, scope: 'openid email', state, nonce, code_challenge: await protocol.calculatePKCECodeChallenge(verifier), code_challenge_method: 'S256' });
        await pool.query('DELETE FROM app_auth_logins WHERE expires_at<=now() OR secret_hash=$1', [digest(getCookie(request, loginName))]);
        await pool.query("INSERT INTO app_auth_logins(secret_hash,payload,expires_at) VALUES($1,$2,now()+interval '5 minutes')", [digest(value), seal(settings.key, { verifier, state, nonce })]);
        response.setHeader('Set-Cookie', cookie(loginName, value, 300));
        response.writeHead(303, { Location: target.href }); response.end(); return true;
      }
      if (request.method === 'GET' && url.pathname === '/api/auth/callback') {
        // Atomic DELETE RETURNING gives the browser-bound handoff one consumer.
        const pending = await pool.query('DELETE FROM app_auth_logins WHERE secret_hash=$1 AND expires_at>now() RETURNING payload', [digest(getCookie(request, loginName))]);
        response.setHeader('Set-Cookie', cookie(loginName, '', 0));
        if (!pending.rowCount) throw new Error('Invalid login');
        const flow = unseal(settings.key, pending.rows[0].payload);
        const callback = new URL(settings.callback); callback.search = url.search;
        const tokens = await protocol.authorizationCodeGrant(await config(), callback, { pkceCodeVerifier: flow.verifier, expectedState: flow.state, expectedNonce: flow.nonce, idTokenExpected: true });
        const claims = tokens.claims();
        if (!claims?.sub || claims.email_verified !== true) throw new Error('Invalid identity');
        const value = random();
        const transaction = await pool.connect();
        try {
          await transaction.query('BEGIN');
          await transaction.query('DELETE FROM app_auth_sessions WHERE secret_hash=$1', [digest(getCookie(request, sessionName))]);
          await transaction.query("INSERT INTO app_auth_sessions(secret_hash,subject,email,expires_at) VALUES($1,$2,$3,now()+interval '12 hours')", [digest(value), claims.sub, claims.email]);
          await transaction.query('COMMIT');
        } catch (error) { await transaction.query('ROLLBACK'); throw error; }
        finally { transaction.release(); }
        response.setHeader('Set-Cookie', [cookie(loginName, '', 0), cookie(sessionName, value, 43200)]);
        response.writeHead(303, { Location: '/' }); response.end(); return true;
      }
      if (request.method === 'GET' && url.pathname === '/api/auth/session') {
        const value = getCookie(request, sessionName);
        const result = await pool.query('SELECT subject,email FROM app_auth_sessions WHERE secret_hash=$1 AND expires_at>now()', [digest(value)]);
        json(response, 200, result.rowCount ? { user: result.rows[0], csrf: csrfFor(value) } : { user: null }); return true;
      }
      if (request.method === 'POST' && url.pathname === '/api/auth/logout') {
        const value = getCookie(request, sessionName);
        if (!value || request.headers.origin !== settings.origin || !equal(request.headers['x-csrf-token'], csrfFor(value))) { json(response, 403, { error: 'Invalid request' }); return true; }
        await pool.query('DELETE FROM app_auth_sessions WHERE secret_hash=$1', [digest(value)]);
        response.setHeader('Set-Cookie', cookie(sessionName, '', 0));
        json(response, 200, { status: 'signed out' }); return true;
      }
      json(response, 404, { error: 'Not found' }); return true;
    } catch {
      // Never log callback URLs, protocol errors, request bodies or token responses.
      json(response, 400, { error: 'Unable to complete authentication. Please sign in again.' }); return true;
    }
  }
  return { initialize, handle };
}
