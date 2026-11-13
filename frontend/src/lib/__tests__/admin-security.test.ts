import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAuthStore } from '../../stores/auth'
import { authFetch, serverLogout } from '../api'
import { publicSiteUrl, safeAdminRedirect, safeExternalUrl } from '../url-safety'

function storage() {
  const data = new Map<string, string>()
  return { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => data.set(k, v), removeItem: (k: string) => data.delete(k) }
}
const metadata = { username: 'admin', name: 'Admin', expiresAt: '2099-01-01T00:00:00Z' }
beforeEach(() => {
  setActivePinia(createPinia())
  vi.stubGlobal('localStorage', storage()); vi.stubGlobal('sessionStorage', storage())
  vi.stubGlobal('window', { location: { origin: 'https://cdk-admin.example.test', pathname: '/ops/cdkeys', href: '' } })
  vi.stubGlobal('document', { cookie: '__Host-cdk_admin_csrf=production-csrf; csrf_token=legacy-csrf' })
})
afterEach(() => vi.unstubAllGlobals())

describe('administrator cookie authentication', () => {
  it('migrates legacy state without retaining a readable bearer token', () => {
    localStorage.setItem('cdk-admin-auth', JSON.stringify({ ...metadata, token: 'synthetic-legacy-bearer' }))
    const store = useAuthStore(); store.restore()
    expect(store.isLoggedIn).toBe(true)
    expect(JSON.parse(localStorage.getItem('cdk-admin-auth')!)).toEqual(metadata)
    store.save({ ...metadata, token: 'another-token' } as typeof metadata)
    expect(localStorage.getItem('cdk-admin-auth')).not.toContain('token')
  })
  it('does not trust forged local metadata when the server rejects the session', async () => {
    const store = useAuthStore(); store.save(metadata)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{}', { status: 401 })))
    expect(await store.verifySession()).toBe(false)
    expect(store.isLoggedIn).toBe(false)
  })
  it('restores a server-confirmed HttpOnly session without a readable token', async () => {
    const store = useAuthStore()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ username: 'admin', name: 'Admin', expires_at: metadata.expiresAt, is_admin: true })))
    expect(await store.verifySession()).toBe(true)
    expect(store.username).toBe('admin')
  })
  it('rejects external targets before fetch can send credentials or CSRF', async () => {
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch)
    for (const input of ['https://evil.example/api/v1/steal', '//evil.example/api/v1/steal', new URL('https://evil.example/api/v1/steal'), new Request('https://evil.example/api/v1/steal')]) {
      await expect(authFetch(input, { method: 'POST' })).rejects.toThrow('this site')
    }
    expect(fetch).not.toHaveBeenCalled()
  })
  it('uses the production CSRF cookie, omits bearer headers and blocks redirects', async () => {
    const fetch = vi.fn().mockResolvedValue(Response.json({ ok: true })); vi.stubGlobal('fetch', fetch)
    await authFetch(new Request('https://cdk-admin.example.test/api/v1/admin/settings', { method: 'PUT', headers: { Authorization: 'Bearer old', 'X-CSRF-Token': 'untrusted' }, body: '{}' }))
    const init = fetch.mock.calls[0][1]
    expect(init.headers.get('Authorization')).toBeNull()
    expect(init.headers.get('X-CSRF-Token')).toBe('production-csrf')
    expect(init.credentials).toBe('same-origin')
    expect(init.redirect).toBe('error')
  })
  it('clears a revoked session even when rejection comes from a cardplatform route', async () => {
    const store = useAuthStore(); store.save(metadata)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ error: 'Invalid or expired token' }, { status: 401 })))
    await authFetch('/api/v1/admin/cardplatform/cdks')
    expect(store.isLoggedIn).toBe(false)
    expect(window.location.href).toBe('/ops/login')
  })
  it('does not confuse upstream API key errors with local session expiry', async () => {
    const store = useAuthStore(); store.save(metadata)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ error: 'invalid API key' }, { status: 401 })))
    await authFetch('/api/v1/admin/cardplatform/cdks')
    expect(store.isLoggedIn).toBe(true)
  })
  it('does not report successful logout while the cookie remains active', async () => {
    const store = useAuthStore(); store.save(metadata)
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
    await expect(serverLogout()).rejects.toThrow('未能注销')
    expect(store.isLoggedIn).toBe(true)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ ok: true })))
    localStorage.setItem('cdk_full_code_cache_v1', 'synthetic-code')
    sessionStorage.setItem('cdk_recent_issued_v1', 'synthetic-code')
    await serverLogout()
    expect(store.isLoggedIn).toBe(false)
    expect(localStorage.getItem('cdk_full_code_cache_v1')).toBeNull()
    expect(sessionStorage.getItem('cdk_recent_issued_v1')).toBeNull()
  })
})

describe('administrator and public links', () => {
  it('restricts post-login navigation to admin routes', () => {
    expect(safeAdminRedirect('/ops/cdkeys?page=2')).toBe('/ops/cdkeys?page=2')
    for (const value of ['https://evil.example', '//evil.example', '/ops/../../recharge', '/ops\\evil', '/ops/login', '/ops/setup', '/opspoof', ['//evil.example']]) expect(safeAdminRedirect(value)).toBe('/ops')
  })
  it('renders only HTTPS external API links', () => {
    expect(safeExternalUrl('https://example.test/release')).toBe('https://example.test/release')
    for (const value of ['javascript:alert(1)', 'data:text/html,attack', 'http://example.test', '//example.test', 'https://user:password@example.test']) expect(safeExternalUrl(value)).toBe('')
  })
  it('uses the public origin for links copied from the separate admin domain', () => {
    expect(publicSiteUrl('/partner/swap', 'https://cdk.lanting.live')).toBe('https://cdk.lanting.live/partner/swap')
    expect(publicSiteUrl('/recharge', '', 'http://127.0.0.1:5173')).toBe('http://127.0.0.1:5173/recharge')
    expect(() => publicSiteUrl('//evil.example', 'https://cdk.lanting.live')).toThrow()
    expect(() => publicSiteUrl('/recharge', 'javascript:alert(1)')).toThrow()
    expect(() => publicSiteUrl('/recharge', 'http://public.example')).toThrow()
  })
})
