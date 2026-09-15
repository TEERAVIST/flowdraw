import { useEffect, useState } from 'react';

export function AccountMenu() {
  const [session, setSession] = useState(null);
  const [error, setError] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    fetch('/api/auth/session', { signal: controller.signal, credentials: 'same-origin' })
      .then(response => response.ok ? response.json() : null)
      .then(value => { if (!controller.signal.aborted) setSession(value); })
      .catch(() => {});
    return () => controller.abort();
  }, []);
  async function logout() {
    setError('');
    try {
      const response = await fetch('/api/auth/logout', { method: 'POST', credentials: 'same-origin', headers: { 'X-CSRF-Token': session.csrf } });
      if (!response.ok) throw new Error('Sign out failed');
      setSession({ user: null });
    } catch { setError('Could not sign out. Try again.'); }
  }
  if (!session) return null;
  return <>
    {session.user ? <><span>{session.user.email}</span><button type="button" onClick={logout}>Sign out</button></> : <a href="/api/auth/login">Sign in</a>}
    {error && <span role="alert">{error}</span>}
  </>;
}
