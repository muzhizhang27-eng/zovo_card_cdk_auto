import { useAuthStore } from '../stores/auth'
import { sameOriginApiUrl } from './url-safety'

const UNSAFE_METHODS = ['POST', 'PUT', 'PATCH', 'DELETE']

function getCookie(name: string): string {
  const match = document.cookie.match(new RegExp('(?:^|; )' + name.replace(/([.$?*|{}()[\]\\/+^])/g, '\\$1') + '=([^;]*)'))
  try { return match ? decodeURIComponent(match[1]) : '' } catch { return '' }
}

function csrfToken(): string {
  return getCookie('__Host-cdk_admin_csrf') || getCookie('csrf_token')
}

/**
 * 是否应把 401/403 当成「本站登录失效」。
 * 卡台上游 Key 无效/未配置时后端可能透传 401/403，绝不能踢出 admin 会话。
 */
function isLocalSessionAuthFailure(url: string, status: number, body: any): boolean {
  if (status !== 401 && status !== 403) return false

  const err = String(body?.error || body?.msg || body?.message || '').toLowerCase()
  const code = String(body?.error_code || body?.code || '').toLowerCase()

  // 明确卡台/上游错误码
  if (
    code.includes('cardplatform')
    || code.includes('api_key')
    || err.includes('api key')
    || err.includes('openapi')
    || err.includes('卡台')
    || err.includes('spacexcard') || err.includes('zovocard') || err.includes('zovocard.com')
  ) {
    return false
  }

  // 本站鉴权常见文案
  if (
    err.includes('missing authorization')
    || err.includes('invalid or expired token')
    || err.includes('invalid authorization')
    || err.includes('session expired')
    || err.includes('session revoked')
    || err.includes('invalid session')
    || err.includes('csrf')
    || code === 'unauthorized'
  ) {
    return true
  }

  if (/\/api\/v1\/admin\/(cardplatform|network)\//i.test(url)) return false

  // 默认：仅对「非卡台」的 401/403 踢登录；403 CSRF 也算会话保护
  return true
}

export const authFetch = async (input: RequestInfo | URL, init: RequestInit = {}) => {
  const authStore = useAuthStore()
  const url = sameOriginApiUrl(input).href
  const request = input instanceof Request ? input : null
  const headers = new Headers(init.headers || request?.headers || {})
  headers.delete('Authorization')
  headers.delete('X-CSRF-Token')

  // 基于 cookie 的写操作需要 CSRF 双提交：把可读的 csrf_token cookie 放进请求头
  const method = (init.method || request?.method || 'GET').toUpperCase()
  if (UNSAFE_METHODS.includes(method)) {
    const csrf = csrfToken()
    if (csrf) headers.set('X-CSRF-Token', csrf)
  }

  if (init.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  const response = await fetch(input, {
    ...init,
    headers,
    credentials: 'same-origin',
    redirect: 'error',
    cache: 'no-store',
  })

  if (response.status === 401 || response.status === 403) {
    let body: any = null
    try {
      body = await response.clone().json()
    } catch {
      body = null
    }
    if (isLocalSessionAuthFailure(url, response.status, body)) {
      authStore.logout()
      if (window.location.pathname.startsWith('/ops')) {
        window.location.href = '/ops/login'
      }
    }
  }

  return response
}

// 主动登出：先请求后端清除 HttpOnly cookie，再清本地状态
export const serverLogout = async () => {
  try {
    const csrf = csrfToken()
    const headers = new Headers()
    if (csrf) headers.set('X-CSRF-Token', csrf)
    const response = await fetch('/api/v1/auth/admin/logout', { method: 'POST', headers, credentials: 'same-origin', redirect: 'error' })
    if (!response.ok && response.status !== 401) throw new Error('退出失败，请重试')
  } catch (error) {
    throw new Error('未能注销服务器会话，请检查网络后重试')
  }
  useAuthStore().logout()
}
