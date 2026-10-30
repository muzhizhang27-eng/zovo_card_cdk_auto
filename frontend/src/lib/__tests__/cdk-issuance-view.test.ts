import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, nextTick, ssrContextKey } from 'vue'
import CDKeyManagement from '../../views/admin/CDKeyManagement.vue'
import { PENDING_ISSUE_KEY } from '../cdk-issuance'

const mocks = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock('../api', () => ({ authFetch: mocks.fetch }))
vi.mock('../dialog', () => ({ dialog: { confirm: async () => true, toast() {} } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
function storage() {
  const data = new Map<string, string>()
  return { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => { data.set(k, v) }, removeItem: (k: string) => { data.delete(k) } }
}
const renderer = createRenderer({
  insert() {}, remove() {}, createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  setText() {}, setElementText() {}, parentNode: () => null, nextSibling: () => null, patchProp() {},
})
const apps: ReturnType<typeof renderer.createApp>[] = []
function mount() {
  let vm: any
  const component = CDKeyManagement as any
  const app = renderer.createApp({ ...component, setup(props: unknown, ctx: unknown) { vm = component.setup(props, ctx); return () => null } })
  app.provide(ssrContextKey, { modules: new Set() })
  app.mount({}); apps.push(app)
  return { vm, close: () => app.unmount() }
}
async function settle() { for (let i = 0; i < 12; i++) { await Promise.resolve(); await nextTick() } }
let scenario = 'success', resolveIssue: ((response: Response) => void) | undefined
const code = 'SYNTHETIC-ONLY-NOT-A-REAL-CDK-12345678'
const posts = () => mocks.fetch.mock.calls.filter(([path, init]) => path === '/api/v1/admin/cardplatform/cdks' && init?.method === 'POST')
beforeEach(() => {
  scenario = 'success'; resolveIssue = undefined
  vi.stubGlobal('localStorage', storage()); vi.stubGlobal('sessionStorage', storage())
  vi.stubGlobal('window', { location: { origin: 'https://admin.example.test' } })
  vi.stubGlobal('navigator', { locks: { request: async (_name: string, _opts: unknown, action: (lock: object) => unknown) => action({}) } })
  mocks.fetch.mockReset().mockImplementation(async (path: string, init: RequestInit = {}) => {
    if (path === '/api/v1/admin/cardplatform/cdks' && init.method === 'POST') {
      const pending = JSON.parse(localStorage.getItem(PENDING_ISSUE_KEY)!)
      expect(new Headers(init.headers).get('Idempotency-Key')).toBe(pending.key)
      expect(JSON.parse(String(init.body))).toEqual(pending.body)
      if (scenario === 'lost-response') throw new TypeError('accepted but response lost')
      if (scenario === 'server-error') return Response.json({ error: 'issue_outcome_unknown' }, { status: 502 })
      if (scenario === 'legacy-recovery') return Response.json({ recovered: true, issued: [{ code }] })
      if (scenario === 'pending') return new Promise<Response>(resolve => { resolveIssue = resolve })
      return Response.json({ issued: [{ code }], stored_count: 1 })
    }
    if (path.includes('/plans')) return Response.json({ plans: { plus: {} }, registry: [{ key: 'plus' }], payment_regions: [{ country: 'PH', currency: 'PHP' }] })
    if (path.endsWith('/settings')) return Response.json({ card_api_key_configured: true })
    if (path.includes('/stored?')) return Response.json({ list: [], total: 0, full_code_in_store: 0 })
    return Response.json({})
  })
})
afterEach(() => { apps.splice(0).forEach(app => { if (app._instance) app.unmount() }); vi.unstubAllGlobals() })
async function ready() {
  const page = mount(); await settle()
  page.vm.form.funding_confirmed = true
  return page
}
describe('actual paid issuance view', () => {
  it('persists before POST and never repeats an uncertain purchase on refresh', async () => {
    for (const failure of ['lost-response', 'server-error', 'legacy-recovery']) {
      localStorage.removeItem(PENDING_ISSUE_KEY); mocks.fetch.mockClear()
      let page = await ready(); scenario = failure
      await page.vm.issue()
      const pending = page.vm.pendingIssue.value
      expect(pending).not.toBeNull(); expect(posts()).toHaveLength(1)
      page.close(); page = await ready()
      await page.vm.issue()
      expect(posts()).toHaveLength(1)
      expect(page.vm.pendingIssue.value.key).toBe(pending.key)
      page.close()
    }
  })
  it('blocks duplicate clicks while a paid POST is in flight', async () => {
    const page = await ready(); scenario = 'pending'
    const first = page.vm.issue(); await settle(); await page.vm.issue()
    expect(posts()).toHaveLength(1)
    resolveIssue!(Response.json({ issued: [{ code }], stored_count: 1 }))
    await first
    expect(page.vm.pendingIssue.value).toBeNull()
    expect(localStorage.getItem(PENDING_ISSUE_KEY)).toBeNull()
    expect(page.vm.recentCodes.value).toEqual([code])
    expect(localStorage.getItem('cdk_full_code_cache_v1')).toBeNull()
    expect(sessionStorage.getItem('cdk_recent_issued_v1')).toBeNull()
  })
  it('does not send a purchase if it cannot save the request journal', async () => {
    const page = await ready()
    vi.spyOn(localStorage, 'setItem').mockImplementation(() => { throw new Error('storage unavailable') })
    await page.vm.issue()
    expect(posts()).toHaveLength(0)
    expect(page.vm.issueError.value).toBe('storage unavailable')
  })
})
