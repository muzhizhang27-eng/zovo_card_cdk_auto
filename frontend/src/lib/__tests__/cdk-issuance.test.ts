import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { beginIssue, finishIssue, loadPendingIssue, PENDING_ISSUE_KEY, withIssueLock, type IssueBody } from '../cdk-issuance'
const body: IssueBody = { plan: 'plus', product: 'gpt', count: 1, funding_confirmed: true, payment_country: 'PH' }
function storage() {
  const data = new Map<string, string>()
  return { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => { data.set(k, v) }, removeItem: (k: string) => { data.delete(k) } }
}
beforeEach(() => { vi.stubGlobal('crypto', { randomUUID: () => 'synthetic-issue-id' }) })
afterEach(() => vi.unstubAllGlobals())
describe('paid CDK issuance journal', () => {
  it('preserves request identity across reload and refuses another batch until reconciled', () => {
    const s = storage(), payload = { ...body }, pending = beginIssue(s, payload)
    payload.count = 20
    expect(loadPendingIssue(s)).toEqual(pending)
    expect(pending.body.count).toBe(1)
    expect(() => beginIssue(s, body)).toThrow('尚未核对')
    finishIssue(s, 'different-request')
    expect(loadPendingIssue(s)).toEqual(pending)
    finishIssue(s, pending.key)
    expect(loadPendingIssue(s)).toBeNull()
  })
  it('fails closed when persistence is unavailable or malformed', () => {
    const s = storage(); s.setItem = () => { throw new Error('quota') }
    expect(() => beginIssue(s, body)).toThrow('quota')
    const broken = storage(); broken.setItem(PENDING_ISSUE_KEY, '{"key":"lost"}')
    expect(() => beginIssue(broken, body)).toThrow('无法读取')
  })
  it('blocks simultaneous purchases across browser tabs', async () => {
    const action = vi.fn()
    vi.stubGlobal('navigator', { locks: { request: async (_n: string, _opts: unknown, callback: (lock: unknown) => unknown) => callback(null) } })
    await expect(withIssueLock(action)).rejects.toThrow('另一个标签页')
    expect(action).not.toHaveBeenCalled()
  })
})
