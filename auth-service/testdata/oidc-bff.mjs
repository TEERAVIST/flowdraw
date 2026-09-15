// Invoked by the Go PostgreSQL/TLS integration test. No real providers or email.
import assert from 'node:assert/strict';
import pg from 'pg';
import { createAuth } from '../../server/auth.js';

const pool = new pg.Pool({ connectionString: process.env.AUTH_TEST_DATABASE_URL });
const env = {
  FLOWDRAW_AUTH_ISSUER: process.env.AUTH_TEST_ISSUER,
  FLOWDRAW_PUBLIC_URL: 'https://flowdraw.example.test',
  FLOWDRAW_CLIENT_SECRET: 'test-client-secret'.padEnd(32, '!'),
  FLOWDRAW_SESSION_KEY: Buffer.alloc(32, 9).toString('base64'),
};
const auth = createAuth({ pool, env });
function response() {
  return { status: 0, headers: {}, body: '', setHeader(name, value) { this.headers[name] = value; }, writeHead(status, headers = {}) { this.status = status; Object.assign(this.headers, headers); }, end(body = '') { this.body = body; } };
}
const request = async (method, path, headers = {}) => {
  const res = response();
  await auth.handle({ method, headers }, res, new URL(path, env.FLOWDRAW_PUBLIC_URL));
  return res;
};
try {
  await auth.initialize();
  const login = await request('GET', '/api/auth/login');
  assert.equal(login.status, 303, login.body);
  const flowCookie = login.headers['Set-Cookie'].split(';')[0];
  const authorize = await fetch(login.headers.Location, { redirect: 'manual', headers: { Cookie: process.env.AUTH_TEST_COOKIE } });
  assert.equal(authorize.status, 303);
  const callback = new URL(authorize.headers.get('location'));
  assert.ok(callback.searchParams.has('code'), callback.searchParams.get('error'));
  const completed = await request('GET', callback.pathname + callback.search, { cookie: flowCookie });
  assert.equal(completed.status, 303, completed.body);
  assert.equal(completed.headers.Location, '/');
  const sessionCookie = completed.headers['Set-Cookie'].find(v => v.startsWith('__Host-flowdraw_session=')).split(';')[0];
  const session = await request('GET', '/api/auth/session', { cookie: sessionCookie });
  const data = JSON.parse(session.body);
  assert.equal(data.user.email, 'u@example.test');
  assert.ok(data.csrf);
  assert.equal(session.body.includes('access_token'), false);
  assert.equal(session.body.includes('refresh_token'), false);
  const replay = await request('GET', callback.pathname + callback.search, { cookie: flowCookie });
  assert.equal(replay.status, 400);
  const logout = await request('POST', '/api/auth/logout', { cookie: sessionCookie, origin: env.FLOWDRAW_PUBLIC_URL, 'x-csrf-token': data.csrf });
  assert.equal(logout.status, 200);
  const expired = await request('GET', '/api/auth/session', { cookie: sessionCookie });
  assert.equal(JSON.parse(expired.body).user, null);
  console.log('BFF OIDC TLS/PostgreSQL integration passed');
} finally { await pool.end(); }
