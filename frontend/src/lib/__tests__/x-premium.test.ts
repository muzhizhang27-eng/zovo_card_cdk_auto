import { describe, expect, it } from 'vitest'
import { isXPremiumPlan, xPremiumCredential } from '../x-premium'
import { planLabel, planSatisfied } from '../plan'
import { extractCdkSession } from '../batch-session'
describe('X subscriptions (XPASS)', () => {
  // 2026-10-24 凭据简化:只需 auth_token(裸串或 JSON{auth_token});ct0/billing_email 不再要。
  it('accepts a bare auth_token and JSON {auth_token}, still rejects GPT sessions', () => {
    const bare = 'a'.repeat(40)
    expect(xPremiumCredential(bare)).toBe(bare)
    const json = JSON.stringify({ auth_token: 'a'.repeat(40) })
    expect(xPremiumCredential(json)).toBe(json)
    expect(extractCdkSession(json)).toBe(json)
    expect(xPremiumCredential('{"sessionToken":"fixture"}')).toBe('')
  })
  it('recognizes the new XPASS tiers, not the retired basic/premium_plus', () => {
    expect(isXPremiumPlan('plus_monthly')).toBe(true)
    expect(isXPremiumPlan('super_yearly')).toBe(true)
    expect(isXPremiumPlan('ultra_monthly')).toBe(true)
    expect(isXPremiumPlan('premium_plus_yearly')).toBe(false)
    expect(isXPremiumPlan('basic_monthly')).toBe(false)
    expect(planLabel('super_yearly')).toContain('X Super')
    expect(planLabel('ultra_monthly')).toContain('X Ultra')
    expect(planSatisfied('pro', 'premium_monthly')).toBe(false) // GPT 订阅不满足 X
  })
})
