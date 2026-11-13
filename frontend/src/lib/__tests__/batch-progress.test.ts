import { describe, expect, it } from 'vitest'
import { BATCH_PROGRESS_KEY, loadBatchProgress, saveBatchProgress, type BatchItem } from '../batch-progress'

function storage() {
  const data = new Map<string, string>()
  return { data, getItem: (k: string) => data.get(k) || null,
    setItem: (k: string, v: string) => { data.set(k, v) }, removeItem: (k: string) => { data.delete(k) } }
}
function item(patch: Partial<BatchItem> = {}): BatchItem {
  return { id: 'one', cardKey: 'SYNTHETIC-CDK', planLabel: 'plus', status: 'submitting', verifyMsg: '有效',
    redemptionToken: 'synthetic-token', previewExpiresAt: Date.now() + 60_000,
    clientRequestId: 'stable-request', submittedAt: Date.now(), progressMsg: '提交中',
    email: 'test@example.invalid', cardLastFour: '', accessToken: 'never-store-access-token',
    gptPassword: 'never-store-gpt-password', emailPassword: 'never-store-mail-password', error: '', ...patch }
}

describe('batch progress recovery', () => {
  it('restores an in-flight request for queries with its original identity and account mapping', () => {
    const s = storage(), original = item()
    expect(saveBatchProgress(s, 'session', [original])).toBe(true)
    const restored = loadBatchProgress(s)!
    expect(restored.items[0]).toMatchObject({ cardKey: original.cardKey, email: original.email,
      clientRequestId: 'stable-request', redemptionToken: 'synthetic-token', status: 'processing' })
  })
  it('never persists or restores imported credentials', () => {
    const s = storage()
    saveBatchProgress(s, 'mailbox', [item()])
    expect(s.getItem(BATCH_PROGRESS_KEY)).not.toContain('never-store')
    expect(loadBatchProgress(s)!.items[0]).toMatchObject({ accessToken: '', gptPassword: '', emailPassword: '' })
  })
  it('restores interrupted preflight to waiting for credentials without inventing a submission', () => {
    const s = storage()
    saveBatchProgress(s, 'session', [item({ submittedAt: 0 })])
    expect(loadBatchProgress(s)!.items[0]).toMatchObject({ submittedAt: 0, status: 'waiting_session' })
  })
  it('preserves final outcomes and order of remaining accounts', () => {
    const s = storage()
    saveBatchProgress(s, 'session', [item({ status: 'success' }), item({ id: 'two', cardKey: 'SECOND', status: 'waiting_session', submittedAt: 0 })])
    expect(loadBatchProgress(s)!.items.map(i => [i.id, i.status])).toEqual([['one', 'success'], ['two', 'waiting_session']])
  })
  it('drops expired or malformed snapshots and clears a reset batch', () => {
    const s = storage()
    s.setItem(BATCH_PROGRESS_KEY, JSON.stringify({ version: 1, savedAt: 1, items: [item()] }))
    expect(loadBatchProgress(s)).toBeNull()
    s.setItem(BATCH_PROGRESS_KEY, '{broken')
    expect(loadBatchProgress(s)).toBeNull()
    expect(saveBatchProgress(s, 'session', [])).toBe(true)
    expect(s.getItem(BATCH_PROGRESS_KEY)).toBeNull()
  })
  it('reports unavailable storage so callers can stop before submitting', () => {
    const s = storage()
    s.setItem = () => { throw new Error('quota exceeded') }
    expect(saveBatchProgress(s, 'session', [item()])).toBe(false)
  })
})
