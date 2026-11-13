export type ItemStatus = 'pending_verify' | 'verify_ok' | 'verify_fail' | 'waiting_session'
  | 'submitting' | 'processing' | 'success' | 'failed'

export interface BatchItem {
  id: string
  cardKey: string
  planLabel: string
  status: ItemStatus
  verifyMsg: string
  redemptionToken: string
  previewExpiresAt: number
  clientRequestId: string
  submittedAt: number
  progressMsg: string
  email: string
  cardLastFour: string
  accessToken: string
  gptPassword: string
  emailPassword: string
  error: string
}

export const BATCH_PROGRESS_KEY = 'cdk_batch_progress_v1'
const STATUSES = new Set<ItemStatus>(['pending_verify', 'verify_ok', 'verify_fail', 'waiting_session', 'submitting', 'processing', 'success', 'failed'])
type ProgressStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

export function saveBatchProgress(storage: ProgressStorage, credMode: 'session' | 'mailbox', items: BatchItem[]): boolean {
  try {
    if (!items.length) storage.removeItem(BATCH_PROGRESS_KEY)
    else storage.setItem(BATCH_PROGRESS_KEY, JSON.stringify({
      version: 1, savedAt: Date.now(), credMode,
      // Keep only what is needed to recover queries; never serialize imported credentials.
      items: items.map(({ id, cardKey, planLabel, status, verifyMsg, redemptionToken, previewExpiresAt,
        clientRequestId, submittedAt, progressMsg, email, cardLastFour, error }) => ({
        id, cardKey, planLabel, status, verifyMsg, redemptionToken, previewExpiresAt,
        clientRequestId, submittedAt, progressMsg, email, cardLastFour, error,
      })),
    }))
    return true
  } catch { return false }
}

export function loadBatchProgress(storage: ProgressStorage): { credMode: 'session' | 'mailbox'; items: BatchItem[] } | null {
  try {
    const raw = storage.getItem(BATCH_PROGRESS_KEY)
    if (!raw) return null
    const p = JSON.parse(raw)
    if (p.version !== 1 || !Array.isArray(p.items) || !Number.isFinite(p.savedAt)
      || Date.now() - p.savedAt > 7 * 24 * 3600_000) {
      storage.removeItem(BATCH_PROGRESS_KEY)
      return null
    }
    const seen = new Set<string>()
    const items: BatchItem[] = []
    for (const row of p.items.slice(0, 1000)) {
      if (!row || typeof row.id !== 'string' || typeof row.cardKey !== 'string'
        || !row.cardKey.trim() || seen.has(row.id) || !STATUSES.has(row.status)) continue
      seen.add(row.id)
      const submittedAt = Number(row.submittedAt) || 0
      const status: ItemStatus = row.status === 'submitting'
        ? submittedAt ? 'processing' : 'waiting_session' : row.status
      items.push({
        id: row.id, cardKey: row.cardKey, status, submittedAt,
        planLabel: String(row.planLabel || ''), verifyMsg: String(row.verifyMsg || ''),
        redemptionToken: String(row.redemptionToken || ''), clientRequestId: String(row.clientRequestId || ''),
        previewExpiresAt: Number(row.previewExpiresAt) || 0,
        progressMsg: String(row.progressMsg || ''), email: String(row.email || ''),
        cardLastFour: String(row.cardLastFour || ''), error: String(row.error || ''),
        accessToken: '', gptPassword: '', emailPassword: '',
      })
    }
    return items.length ? { credMode: p.credMode === 'mailbox' ? 'mailbox' : 'session', items } : null
  } catch { return null }
}
