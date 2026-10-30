export const PENDING_ISSUE_KEY = 'cdk_pending_issue_v1'

export interface IssueBody {
  plan: string
  product: string
  count: number
  funding_confirmed: true
  payment_country: string
}
export interface PendingIssue { key: string; body: IssueBody; createdAt: string }
type JournalStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

export function loadPendingIssue(storage: JournalStorage): PendingIssue | null {
  const raw = storage.getItem(PENDING_ISSUE_KEY)
  if (!raw) return null
  const value = JSON.parse(raw) as PendingIssue
  if (!value || typeof value.key !== 'string' || !value.key.startsWith('cdk-issue-')
    || !value.body || !Number.isInteger(value.body.count) || value.body.count < 1 || value.body.count > 200
    || typeof value.body.plan !== 'string' || typeof value.body.product !== 'string'
    || typeof value.body.payment_country !== 'string' || value.body.funding_confirmed !== true
    || !Number.isFinite(Date.parse(value.createdAt))) throw new Error('发码记录无法读取，请先核对库存')
  return value
}

export function beginIssue(storage: JournalStorage, body: IssueBody): PendingIssue {
  if (loadPendingIssue(storage)) throw new Error('上次发码结果尚未核对，不能开始新购买')
  const pending = { key: `cdk-issue-${crypto.randomUUID()}`, body: { ...body }, createdAt: new Date().toISOString() }
  storage.setItem(PENDING_ISSUE_KEY, JSON.stringify(pending))
  return pending
}

export function finishIssue(storage: JournalStorage, key: string): void {
  if (loadPendingIssue(storage)?.key === key) storage.removeItem(PENDING_ISSUE_KEY)
}

export async function withIssueLock(action: () => Promise<void>): Promise<void> {
  if (!navigator.locks) throw new Error('当前浏览器不支持安全发码锁，请使用新版浏览器和 HTTPS')
  await navigator.locks.request('cdk-admin-issue', { ifAvailable: true }, async lock => {
    if (!lock) throw new Error('另一个标签页正在购买，请等待并核对库存')
    await action()
  })
}
