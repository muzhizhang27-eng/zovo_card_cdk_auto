import { describe, expect, it } from 'vitest'
import {
  activationSessionForRetry,
  checkSessionForCdk,
  extractCdkSession,
  parseSessionJsonLenient,
} from '../batch-session'

const JWE = 'eyJ0.a.b.c.d' // 五段 JWE 占位(sessionToken 形态)

describe('格式化多行 Session JSON 容错', () => {
  it('标准 pretty 多行(含 sessionToken)照常通过', () => {
    const raw = `{
  "user": { "name": "张三", "email": "a@b.com" },
  "accessToken": "eyJa.b.c",
  "sessionToken": "${JWE}"
}`
    const s = extractCdkSession(raw)
    expect(s).not.toBe('')
    expect(() => JSON.parse(s)).not.toThrow() // 回传给后端的必须是严格合法 JSON
    expect(s).toBe(JSON.stringify(JSON.parse(raw)))
  })

  it('带尾逗号的多行 JSON 现在能解析(原来 JSON.parse 直接抛→误报)', () => {
    const raw = `{
  "accessToken": "eyJa.b.c",
  "sessionToken": "${JWE}",
}`
    const s = extractCdkSession(raw)
    expect(s).not.toBe('')
    // 回传的必须已清洗成严格合法 JSON(后端 Go 的 json.Unmarshal 才能过)
    const obj = JSON.parse(s)
    expect(obj.sessionToken).toBe(JWE)
    expect(checkSessionForCdk(raw).ok).toBe(true)
    expect(s).not.toMatch(/[\r\n]/)
  })

  it('数组/嵌套对象的尾逗号也清理', () => {
    const raw = `{
  "sessionToken": "${JWE}",
  "arr": [1, 2, 3,],
  "nested": { "a": 1, },
}`
    const parsed = parseSessionJsonLenient(raw)
    expect(parsed).not.toBeNull()
    expect(() => JSON.parse(parsed!.canonical)).not.toThrow()
  })

  it('字符串内部的逗号/花括号绝不被动', () => {
    const raw = `{
  "sessionToken": "${JWE}",
  "note": "a,b,}] not json",
}`
    const parsed = parseSessionJsonLenient(raw)
    expect(parsed).not.toBeNull()
    expect((parsed!.obj as any).note).toBe('a,b,}] not json')
  })

  it('无 sessionToken 仍拒(规则不放松)', () => {
    const raw = `{
  "user": { "email": "a@b.com" },
  "accessToken": "eyJa.b.c",
}`
    expect(extractCdkSession(raw)).toBe('')
  })

  it('彻底非法 JSON 返回 null / 空', () => {
    expect(parseSessionJsonLenient('{ not json at all ')).toBeNull()
    expect(extractCdkSession('{ nope')).toBe('')
  })

  it('客服重试拒绝纯 Access Token、Grok 和裸 X 凭据', () => {
    expect(activationSessionForRetry(`{"sessionToken":"${JWE}"}`)).toContain(JWE)
    expect(activationSessionForRetry('eyJ.fixture.payload')).toBe('')
    expect(activationSessionForRetry('sso=' + 'a'.repeat(40))).toBe('')
    expect(activationSessionForRetry('ab'.repeat(21))).toBe('')
  })
})

describe('提交前压缩 Session JSON', () => {
  it('完整保留大型格式化导出中的凭证及附加字段', () => {
    const value = {
      sessionToken: `eyJ0..iv.${'a'.repeat(4002)}.tag`,
      accessToken: `eyJ0.${'b'.repeat(1900)}.sig`,
      user: { email: 'fixture@example.invalid', name: '测试账号' },
      marketingEligibility: {
        events: Array.from({ length: 800 }, (_, i) => ({ name: `synthetic-event-${i}`, eligible: true })),
      },
    }
    const raw = JSON.stringify(value, null, 4)
    expect(new TextEncoder().encode(raw).length).toBeGreaterThan(65536)
    const compact = extractCdkSession(raw)
    expect(compact).toBe(JSON.stringify(value))
    expect(JSON.parse(compact)).toEqual(value)
    expect(new TextEncoder().encode(compact).length).toBeLessThan(65536)
    expect(checkSessionForCdk(raw).ok).toBe(true)
  })

  it('保留字符串里的空格、换行转义、引号与反斜杠', () => {
    const value = { sessionToken: JWE, note: '  引号 " 和 \\ 路径\t换行\n  ', punctuation: ', }] {' }
    const raw = JSON.stringify(value, null, '\t')
    const compact = parseSessionJsonLenient(raw)!.canonical
    expect(compact).toBe(JSON.stringify(value))
    expect(JSON.parse(compact)).toEqual(value)
  })

  it('压缩时不改写大整数、负零或指数数字字面量', () => {
    const raw = `{
      "sessionToken": "${JWE}",
      "largeId": 9007199254740993,
      "negativeZero": -0,
      "exponent": 1.2300e+04
    }`
    expect(parseSessionJsonLenient(raw)!.canonical).toBe(
      `{"sessionToken":"${JWE}","largeId":9007199254740993,"negativeZero":-0,"exponent":1.2300e+04}`,
    )
  })

  it('紧凑 JSON 重复处理保持不变', () => {
    const compact = `{"sessionToken":"${JWE}","note":"a b"}`
    expect(parseSessionJsonLenient(compact)!.canonical).toBe(compact)
    expect(extractCdkSession(compact)).toBe(compact)
  })
})
