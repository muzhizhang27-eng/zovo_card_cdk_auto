/** A timed-out submission must be reconciled through result, never resent automatically. */
export async function cdkRequest(path: string, device: string, init: RequestInit = {}) {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), path.endsWith('/recover-subscription') ? 240_000 : 90_000)
  const abort = () => controller.abort()
  if (init.signal?.aborted) controller.abort()
  else init.signal?.addEventListener('abort', abort, { once: true })
  const headers = new Headers(init.headers || {})
  headers.set('X-Redemption-Device', device)
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  try {
    const r = await fetch(path, { ...init, headers, signal: controller.signal, credentials: 'include' })
    const text = await r.text()
    let data: any = null
    try { data = text ? JSON.parse(text) : null } catch { data = { raw: text } }
    return { r, data }
  } finally {
    clearTimeout(timeout)
    init.signal?.removeEventListener('abort', abort)
  }
}

export function newRedemptionRequestId(prefix: string): string {
  const random = globalThis.crypto?.randomUUID?.() || Math.random().toString(36).slice(2)
  return `${prefix}-${Date.now()}-${random}`.slice(0, 80)
}

export function isDefiniteRejection(response: Response, data: any): boolean {
  // Conflicts, timeouts, rate limits and server errors require reconciliation.
  return response.status >= 400 && response.status < 500
    && ![408, 409, 429].includes(response.status)
    && !(data?.order || data?.data?.order || data?.data?.id)
}
