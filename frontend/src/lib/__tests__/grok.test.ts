import { describe, expect, it } from 'vitest'
import { grokCredential, grokPlanLabel, isGrokCredential, isGrokPlan } from '../grok'
import { isXPremiumPlan } from '../x-premium'
import { planLabel, planSatisfied } from '../plan'
import { extractCdkSession } from '../batch-session'

describe('Grok plans', () => {
  it('recognises bare and prefixed Grok keys without touching GPT / X keys', () => {
    for (const p of ['plus_monthly', 'super_yearly', 'ultra_monthly', 'grok_premium_monthly',
      'monthly', 'yearly', 'lite_monthly', 'heavy_monthly', 'grok_heavy_yearly']) { // 后五个是改名前的旧键
      expect(isGrokPlan(p)).toBe(true)
    }
    for (const p of ['plus', 'pro_20x', 'pro_20x_renew', 'credit250', 'go', 'premium_monthly', 'basic_yearly', 'premium_plus_monthly']) {
      expect(isGrokPlan(p)).toBe(false)
    }
    // 2026-10-24:X 并入 XPASS 后与 Grok 同用 plus/premium/super/ultra 四档,裸 tier_月年 两者都认,
    // 靠显式 product 区分(issueProduct);但 GPT 的裸 'plus'(无 _月年)不属于 X。
    expect(isXPremiumPlan('plus')).toBe(false)
    expect(isXPremiumPlan('pro_20x')).toBe(false)
  })
  it('labels Grok plans with xAI tier names and compares them exactly (tiers are parallel product lines)', () => {
    expect(grokPlanLabel('premium_monthly')).toBe('Grok Premium / 月')
    expect(grokPlanLabel('plus_yearly')).toBe('Grok Plus / 年')
    expect(planLabel('grok_super_monthly')).toBe('Grok Super / 月')
    expect(planLabel('grok_ultra_yearly')).toBe('Grok Ultra / 年')
    // 改名前的旧键显示为新名
    expect(grokPlanLabel('monthly')).toBe('Grok Premium / 月')
    expect(planLabel('grok_heavy_yearly')).toBe('Grok Ultra / 年')
    expect(planLabel('lite_monthly')).toBe('Grok Plus / 月')
    // 裸 premium_monthly 归 X；带 grok_ 前缀才是 Grok
    expect(planLabel('premium_monthly')).toBe('X Premium / month')
    expect(planLabel('grok_premium_monthly')).toBe('Grok Premium / 月')
    expect(planSatisfied('premium_monthly', 'grok_premium_monthly')).toBe(true)
    expect(planSatisfied('monthly', 'grok_premium_monthly')).toBe(true)
    expect(planSatisfied('ultra_monthly', 'grok_premium_monthly')).toBe(false)
    expect(planSatisfied('plus_monthly', 'grok_super_monthly')).toBe(false)
    expect(planSatisfied('premium_monthly', 'grok_premium_yearly')).toBe(false)
  })
})

describe('Grok credential', () => {
  const value = 'eyJhbGciOiJIUzI1NiJ9.abcdefghijklmnop.signature_value'
  it('accepts a cookie header or a bare sso value', () => {
    expect(grokCredential('sso=' + value)).toBe('sso=' + value)
    expect(grokCredential('sso-rw=' + value + '; sso=' + value)).toBe('sso-rw=' + value + '; sso=' + value)
    expect(grokCredential('  ' + value + '  ')).toBe('sso=' + value)
  })
  it('rejects empty, multi-line, JSON and too-short input', () => {
    expect(grokCredential('')).toBe('')
    expect(grokCredential('sso=a\nb')).toBe('')
    expect(grokCredential('{"sessionToken":"x"}')).toBe('')
    expect(grokCredential('short')).toBe('')
  })
  it('batch import keeps cookie-form Grok credentials but not GPT JSON', () => {
    expect(isGrokCredential('sso=' + value)).toBe(true)
    expect(isGrokCredential(value)).toBe(false)
    expect(extractCdkSession('sso=' + value)).toBe('sso=' + value)
  })
})
