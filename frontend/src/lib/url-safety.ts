/** Keep credentials and CSRF headers on the site's own API, including Request inputs. */
export function sameOriginApiUrl(input: RequestInfo | URL, origin = window.location.origin): URL {
  const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
  const url = new URL(raw, origin)
  if (url.origin !== origin || url.username || url.password || !url.pathname.startsWith('/api/v1/')) {
    throw new TypeError('Authenticated requests must use this site’s API')
  }
  return url
}

export function safeAdminRedirect(value: unknown): string {
  if (typeof value !== 'string' || !/^\/ops(?:\/|\?|#|$)/.test(value) || /[\\\u0000-\u001f\u007f]/.test(value)) return '/ops'
  const base = 'https://admin.example.invalid'
  const url = new URL(value, base)
  if (url.origin !== base || !(url.pathname === '/ops' || url.pathname.startsWith('/ops/'))
    || ['/ops/login', '/ops/setup'].includes(url.pathname)) return '/ops'
  return url.pathname + url.search + url.hash
}

/** External links supplied by an API may never execute a script/data URL. */
export function safeExternalUrl(value: unknown): string {
  if (typeof value !== 'string') return ''
  try {
    const url = new URL(value)
    return url.protocol === 'https:' && !url.username && !url.password ? url.href : ''
  } catch { return '' }
}

export function publicSiteUrl(path: string, configured = import.meta.env.VITE_PUBLIC_SITE_URL || '', origin = window.location.origin): string {
  const base = new URL(configured || origin)
  if ((base.protocol !== 'https:' && !(base.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(base.hostname)))
    || base.username || base.password || base.pathname !== '/' || base.search || base.hash) {
    throw new TypeError('VITE_PUBLIC_SITE_URL must be an HTTPS origin (localhost HTTP is allowed)')
  }
  const url = new URL(path, base)
  if (url.origin !== base.origin) throw new TypeError('Public site links must stay on the configured public site')
  return url.href
}
