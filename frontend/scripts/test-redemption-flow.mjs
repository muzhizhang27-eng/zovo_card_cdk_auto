import assert from 'node:assert/strict'
import { readFileSync, mkdtempSync, rmSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { parse, compileScript } from '@vue/compiler-sfc'
import { build } from 'esbuild'
import { createRenderer, nextTick } from 'vue'

const root = fileURLToPath(new URL('..', import.meta.url))
const tmp = mkdtempSync(resolve(root, '.redemption-test-'))
const local = new Map(), session = new Map(), timers = new Map()
const originalTimers = { setTimeout, clearTimeout, setInterval, clearInterval }
const storage = map => ({ getItem: k => map.get(k) || null, setItem: (k, v) => map.set(k, v), removeItem: k => map.delete(k) })
globalThis.localStorage = storage(local)
globalThis.sessionStorage = storage(session)
let nextTimer = 1
globalThis.setTimeout = globalThis.setInterval = (fn, delay) => { const id = nextTimer++; timers.set(id, { fn, delay }); return id }
globalThis.clearTimeout = globalThis.clearInterval = id => timers.delete(id)
let calls = [], scenario = 'happy', resultStatus = 'completed', previewCount = 0
const credential = JSON.stringify({ sessionToken: 'synthetic.jwe.parts.for.tests', user: { email: 'test@example.invalid' } })
const reply = (data, status = 200) => ({ ok: status >= 200 && status < 300, status, text: async () => JSON.stringify(data) })
globalThis.fetch = async (path, init = {}) => {
  const body = init.body ? JSON.parse(init.body) : {}
  calls.push({ path, body, device: init.headers?.get('X-Redemption-Device') })
  if (path.endsWith('/preview')) {
    previewCount++
    if (scenario === 'rate-limit') return reply({ error: 'too many requests' }, 429)
    return reply({ code: 0, data: { redemption_token: 'synthetic-' + body.code, plan: 'plus', product: 'gpt', expires_at: '2099-01-01T00:00:00Z' } })
  }
  if (path.endsWith('/preflight')) {
    if (scenario === 'preflight-offline') throw new TypeError('offline before submission')
    return reply({ code: 0, data: { preflight_token: 'synthetic-preflight', email: 'test@example.invalid', currentPlan: 'free', subscription_has_active: false } })
  }
  if (path.endsWith('/redeem')) {
    // Check persistence at the actual send boundary, before any server response.
    const saved = session.get('cdk_batch_progress_v1') || session.get('cdk_redeem_progress_v1')
    assert.ok(saved?.includes(body.client_request_id), 'request identity must be saved BEFORE sending')
    if (scenario === 'lost-response') throw new TypeError('response lost after server accepted')
    if (scenario === 'hung-submit') return new Promise((_resolve, reject) => {
      init.signal.addEventListener('abort', () => reject(new DOMException('page closed', 'AbortError')), { once: true })
    })
    if (scenario === 'server-error') return reply({ error: 'gateway unavailable' }, 502)
    if (scenario === 'rejected') return reply({ error: 'invalid credential' }, 400)
    return reply({ code: 0, data: { id: 1001, status: 'awaiting_card', stage: 'cdk_accepted' } })
  }
  if (path.includes('/result')) return reply({ code: 0, data: { order: { id: 1001, status: resultStatus, stage: 'test', account_email: 'test@example.invalid' }, events: [] } })
  throw new Error('Unexpected endpoint: ' + path)
}

const renderer = createRenderer({
  insert() {}, remove() {}, createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  setText() {}, setElementText() {}, parentNode: () => null, nextSibling: () => null, patchProp() {},
})
const mocks = {
  'vue-router': 'export const useRoute=()=>({query:globalThis.__redemptionRouteQuery || {}})',
  'vue-i18n': 'export const useI18n=()=>({t:k=>k})',
  '../../lib/dialog': 'export const dialog={confirm:async()=>true,toast(){}}',
}
async function compile(name) {
  const { descriptor } = parse(readFileSync(resolve(root, 'src/views/user', name + '.vue'), 'utf8'))
  const script = compileScript(descriptor, { id: name + '-test' })
  const outfile = resolve(tmp, name + '.mjs')
  await build({ stdin: { contents: script.content, resolveDir: resolve(root, 'src/views/user'), loader: 'ts' },
    outfile, bundle: true, platform: 'node', format: 'esm', external: ['vue'],
    plugins: [{ name: 'test-mocks', setup(b) {
      b.onResolve({ filter: /\.vue$/ }, a => ({ path: a.path, namespace: 'vue-stub' }))
      b.onLoad({ filter: /.*/, namespace: 'vue-stub' }, () => ({ contents: 'export default {}', loader: 'js' }))
      b.onResolve({ filter: /.*/ }, a => a.path in mocks ? { path: a.path, namespace: 'mock' } : undefined)
      b.onLoad({ filter: /.*/, namespace: 'mock' }, a => ({ contents: mocks[a.path], loader: 'js' }))
    } }] })
  return (await import(pathToFileURL(outfile).href)).default
}
function fresh() {
  local.clear(); session.clear(); timers.clear(); calls = []; scenario = 'happy'; resultStatus = 'completed'; previewCount = 0
  delete globalThis.__redemptionRouteQuery
}
const apps = new Set()
function mount(component) {
  let vm
  const app = renderer.createApp({ ...component, setup(props, ctx) { vm = component.setup(props, ctx); return () => null } })
  app.mount({})
  apps.add(app)
  return { vm, close() { app.unmount(); apps.delete(app) } }
}
async function settle() { await nextTick(); await new Promise(setImmediate) }
async function pollTick() {
  for (const [id, timer] of [...timers]) {
    if (timer.delay === 3000) { timers.delete(id); await timer.fn() }
  }
  await settle()
}
async function readySingle(vm) {
  vm.code.value = 'SXC-SYNTHETIC-SINGLE'
  await vm.doPreview(); assert.equal(vm.step.value, 2)
  vm.sessionRaw.value = credential
  await vm.doPreflight(); assert.equal(vm.step.value, 3)
}
async function readyBatch(vm, count = 2) {
  vm.cdkText.value = Array.from({ length: count }, (_, i) => 'SXC-SYNTHETIC-BATCH-' + i).join('\n')
  await vm.handleVerifyBatch()
  assert.ok(vm.items.value.every(i => i.status === 'waiting_session'))
}
const redeemCalls = () => calls.filter(c => c.path.endsWith('/redeem'))

try {
  const single = await compile('RechargeView'), batch = await compile('BatchRedeemView')
  fresh()
  {
    let page = mount(single), vm = page.vm
    await readySingle(vm); scenario = 'hung-submit'; resultStatus = 'running'
    const pending = vm.doRedeem()
    await settle()
    const id = redeemCalls()[0].body.client_request_id
    page.close(); page = mount(single); vm = page.vm
    await pending; await settle()
    assert.equal(vm.clientRequestId.value, id)
    assert.equal(redeemCalls().length, 1)
    resultStatus = 'completed'; await pollTick()
    assert.equal(vm.resultStatus.value, 'completed')
    page.close()
    console.log('PASS reload while POST is in flight: cancellation preserves the original query state')
  }
  fresh()
  {
    const page = mount(single), vm = page.vm
    await readySingle(vm); await vm.doRedeem(); await settle()
    assert.equal(vm.resultStatus.value, 'completed')
    assert.equal(new Set(calls.map(c => c.device)).size, 1)
    assert.equal(vm.polling.value, false)
    page.close()
    console.log('PASS single happy path and device binding')
  }
  for (const failure of ['lost-response', 'server-error']) {
    fresh()
    let page = mount(single), vm = page.vm
    await readySingle(vm); scenario = failure; resultStatus = 'running'
    await vm.doRedeem(); await settle()
    const originalId = redeemCalls()[0].body.client_request_id
    assert.equal(vm.step.value, 4); assert.equal(vm.polling.value, true)
    await vm.doRedeem(); assert.equal(redeemCalls().length, 1)
    page.close()
    globalThis.__redemptionRouteQuery = { cdk: 'SXC-SYNTHETIC-OTHER' }
    page = mount(single); vm = page.vm
    await settle()
    assert.equal(vm.clientRequestId.value, originalId)
    assert.equal(vm.code.value, 'SXC-SYNTHETIC-SINGLE', 'new URL must not mix another CDK with an unresolved token')
    assert.equal(vm.step.value, 4); assert.equal(redeemCalls().length, 1)
    resultStatus = 'completed'; await pollTick()
    assert.equal(vm.resultStatus.value, 'completed')
    assert.equal(vm.polling.value, false)
    page.close()
    console.log(`PASS single ${failure}: saved before send, automatic query, reload, no resubmission`)
  }
  fresh()
  {
    const page = mount(single), vm = page.vm
    await readySingle(vm); scenario = 'rejected'
    await vm.doRedeem(); await settle()
    assert.equal(vm.resultStatus.value, 'rejected')
    assert.equal(calls.filter(c => c.path.includes('/result')).length, 0)
    page.close()
    console.log('PASS definite rejection does not pretend an order was accepted')
  }
  fresh()
  {
    const page = mount(single), vm = page.vm
    await readySingle(vm)
    const set = sessionStorage.setItem
    sessionStorage.setItem = () => { throw new Error('quota exceeded') }
    await vm.doRedeem(); assert.equal(redeemCalls().length, 0)
    sessionStorage.setItem = set
    page.close()
    console.log('PASS storage failure stops submission before sending')
  }
  fresh()
  {
    let page = mount(batch), vm = page.vm
    await readyBatch(vm); scenario = 'lost-response'; resultStatus = 'running'
    await vm.submitOne(vm.items.value[0], { mode: 'session', session: credential })
    await settle()
    assert.equal(vm.items.value[0].status, 'processing')
    assert.ok(vm.items.value[0].submittedAt)
    const id = vm.items.value[0].clientRequestId
    await vm.submitOne(vm.items.value[0], { mode: 'session', session: credential })
    assert.equal(redeemCalls().length, 1)
    const saved = session.get('cdk_batch_progress_v1')
    assert.ok(!saved.includes('sessionToken')); assert.ok(!saved.includes('synthetic.jwe'))
    page.close(); page = mount(batch); vm = page.vm; await settle()
    assert.equal(vm.restoredProgress.value, true)
    assert.equal(vm.items.value[0].clientRequestId, id)
    assert.equal(vm.items.value[0].email, 'test@example.invalid')
    assert.equal(vm.items.value[1].status, 'waiting_session')
    assert.equal(redeemCalls().length, 1)
    resultStatus = 'completed'; await pollTick()
    assert.equal(vm.items.value[0].status, 'success')
    scenario = 'happy'
    await vm.submitOne(vm.items.value[1], { mode: 'session', session: credential })
    await settle(); await pollTick()
    assert.ok(vm.items.value.every(i => i.status === 'success'))
    page.close()
    console.log('PASS batch lost response, reload, original account pairing and remaining queue')
  }
  fresh()
  {
    const page = mount(batch), vm = page.vm
    await readyBatch(vm, 1); scenario = 'preflight-offline'
    await vm.submitOne(vm.items.value[0], { mode: 'session', session: credential })
    assert.equal(vm.items.value[0].status, 'waiting_session')
    assert.equal(vm.items.value[0].submittedAt, 0); assert.equal(redeemCalls().length, 0)
    page.close()
    console.log('PASS preflight failure remains safe to retry without creating an order')
  }
  fresh()
  {
    const page = mount(batch), vm = page.vm
    scenario = 'rate-limit'; vm.cdkText.value = 'SXC-SYNTHETIC-RATE'
    await vm.handleVerifyBatch()
    assert.equal(vm.items.value[0].status, 'pending_verify')
    scenario = 'happy'; await vm.resumeVerification()
    assert.equal(vm.items.value[0].status, 'waiting_session')
    vm.items.value[0].previewExpiresAt = 0
    const previewsBefore = previewCount
    await vm.submitOne(vm.items.value[0], { mode: 'session', session: credential })
    await settle()
    assert.equal(previewCount, previewsBefore + 1)
    assert.equal(vm.items.value[0].status, 'success')
    page.close()
    console.log('PASS rate-limit exhaustion is recoverable and stale previews refresh before submission')
  }
  console.log('CDK redemption recovery regression suite PASS')
} finally {
  for (const app of apps) app.unmount()
  Object.assign(globalThis, originalTimers)
  delete globalThis.fetch; delete globalThis.localStorage; delete globalThis.sessionStorage
  delete globalThis.__redemptionRouteQuery
  rmSync(tmp, { recursive: true, force: true })
}
