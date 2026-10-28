// 2026-10-24 X 会员并入 XPASS:四档 plus/premium/super/ultra(替换旧 basic/premium/premium_plus)。
export function isXPremiumPlan(value: string): boolean {
  return /^(?:x_)?(?:plus|premium|super|ultra)_(?:monthly|yearly)$/.test(String(value || '').toLowerCase())
}
// 2026-10-24 凭据简化:只需 X 的 auth_token(裸串或 JSON{auth_token});ct0/billing_email 不再要。
export function xPremiumCredential(raw: string): string {
  if (!raw || raw.length > 32768 || /\0/.test(raw)) return ''
  const s = raw.trim()
  if (!s.startsWith('{')) {
    return /^[a-zA-Z0-9._%~-]{16,512}$/.test(s) ? s : '' // 裸 auth_token
  }
  try {
    const v = JSON.parse(s)
    if (/^[a-zA-Z0-9._%~-]{16,512}$/.test(String(v.auth_token || v.authToken || ''))) return s
    // 兼容旧 cookie JSON(赠送 gift 仍用含 auth_token= 的整段 cookie)
    if (typeof v.cookieHeader === 'string' && /auth_token=/.test(v.cookieHeader) && !/[\r\n\0]/.test(v.cookieHeader)) return s
    return ''
  } catch { return '' }
}
