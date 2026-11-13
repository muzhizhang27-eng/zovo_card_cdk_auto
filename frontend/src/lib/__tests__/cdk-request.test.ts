import { afterEach, describe, expect, it, vi } from 'vitest'
import { cdkRequest, isDefiniteRejection, newRedemptionRequestId } from '../cdk-request'

afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe('CDK request lifecycle', () => {
  it('keeps the device header and request ID and never retries an unknown submission', async () => {
    const fetch = vi.fn().mockRejectedValue(new TypeError('response lost'))
    vi.stubGlobal('fetch', fetch)
    const body = JSON.stringify({ client_request_id: 'original-id' })
    await expect(cdkRequest('/api/v1/public/cdk/redeem', 'device', { method: 'POST', body })).rejects.toThrow('response lost')
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch.mock.calls[0][1].body).toBe(body)
    expect(fetch.mock.calls[0][1].headers.get('X-Redemption-Device')).toBe('device')
  })
  it('bounds a hung request and supports cancellation on page exit', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn((_path, init) => new Promise((_resolve, reject) => {
      init.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    })))
    const timed = cdkRequest('/api/v1/public/cdk/redeem', 'device')
    const assertion = expect(timed).rejects.toThrow('aborted')
    await vi.advanceTimersByTimeAsync(90_000)
    await assertion
    const controller = new AbortController()
    const pending = cdkRequest('/result', 'device', { signal: controller.signal })
    const cancelled = expect(pending).rejects.toThrow('aborted')
    controller.abort()
    await cancelled
    expect(vi.getTimerCount()).toBe(0)
  })
  it('distinguishes a definite rejection from an uncertain order outcome', () => {
    expect(isDefiniteRejection(new Response('', { status: 400 }), { error: 'invalid token' })).toBe(true)
    for (const status of [200, 408, 409, 429, 500, 502]) {
      expect(isDefiniteRejection(new Response('', { status }), {})).toBe(false)
    }
    expect(isDefiniteRejection(new Response('', { status: 400 }), { data: { id: 123 } })).toBe(false)
  })
  it('creates IDs within the upstream 80-byte limit', () => {
    const first = newRedemptionRequestId('batch')
    expect(first.length).toBeLessThanOrEqual(80)
    expect(newRedemptionRequestId('batch')).not.toBe(first)
  })
})
