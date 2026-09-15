import test from 'node:test';
import assert from 'node:assert/strict';
import { Buffer } from 'node:buffer';
import { authSettings, seal, unseal, createAuth } from '../server/auth.js';

const env = { FLOWDRAW_AUTH_ISSUER: 'https://auth.example.test', FLOWDRAW_PUBLIC_URL: 'https://flowdraw.example.test', FLOWDRAW_CLIENT_SECRET: 'a'.repeat(32), FLOWDRAW_SESSION_KEY: Buffer.alloc(32, 7).toString('base64') };

test('BFF requires HTTPS origins and complete secrets', () => {
  assert.equal(authSettings({}), null);
  assert.equal(authSettings(env).callback, 'https://flowdraw.example.test/api/auth/callback');
  for (const value of ['http://auth.example.test', 'https://user:password@auth.example.test', 'https://auth.example.test/extra']) assert.throws(() => authSettings({ ...env, FLOWDRAW_AUTH_ISSUER: value }));
  assert.throws(() => authSettings({ ...env, FLOWDRAW_SESSION_KEY: '' }));
});

test('pending OIDC verifier is encrypted and tamper authenticated', () => {
  const key = Buffer.alloc(32, 7);
  const payload = { verifier: 'secret-pkce-verifier', state: 'secret-state', nonce: 'nonce' };
  const ciphertext = seal(key, payload);
  assert.equal(ciphertext.includes(Buffer.from(payload.verifier)), false);
  assert.deepEqual(unseal(key, ciphertext), payload);
  ciphertext[ciphertext.length - 1] ^= 1;
  assert.throws(() => unseal(key, ciphertext));
});

function response() {
  return { status: 0, headers: {}, body: '', setHeader(name, value) { this.headers[name] = value; }, writeHead(status, headers = {}) { this.status = status; Object.assign(this.headers, headers); }, end(body = '') { this.body = body; } };
}

test('callback without browser-bound handoff fails before code exchange', async () => {
  let exchanged = false;
  const auth = createAuth({ env, pool: { query: async () => ({ rowCount: 0 }) }, protocol: { authorizationCodeGrant: async () => { exchanged = true; } } });
  const res = response();
  await auth.handle({ method: 'GET', headers: {} }, res, new URL('https://flowdraw.example.test/api/auth/callback?code=secret-code&state=secret-state'));
  assert.equal(res.status, 400); assert.equal(exchanged, false);
  assert.equal(res.body.includes('secret-code'), false);
  assert.match(res.headers['Set-Cookie'], /__Host-flowdraw_login=; Path=\/; Secure; HttpOnly; SameSite=Lax; Max-Age=0/);
});

test('BFF logout rejects cross-origin requests before database writes', async () => {
  const auth = createAuth({ env, pool: { query: async () => assert.fail('database write') } });
  const res = response();
  await auth.handle({ method: 'POST', headers: { cookie: '__Host-flowdraw_session=opaque', origin: 'https://evil.test', 'x-csrf-token': 'wrong' } }, res, new URL('https://flowdraw.example.test/api/auth/logout'));
  assert.equal(res.status, 403);
});
