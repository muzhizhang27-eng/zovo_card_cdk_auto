import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

const STORAGE_KEY = 'cdk-admin-auth'

interface StoredAuth {
  username: string
  name: string
  expiresAt: string
}

export const useAuthStore = defineStore('auth', () => {
  const username = ref('')
  const name = ref('')
  const expiresAt = ref('')
  let checking: Promise<boolean> | null = null
  const isLoggedIn = computed(() => !!username.value && Date.parse(expiresAt.value) > Date.now())

  // Persist an explicit metadata allowlist, never the old readable JWT.
  const save = (payload: StoredAuth) => {
    username.value = typeof payload.username === 'string' ? payload.username : ''
    name.value = typeof payload.name === 'string' ? payload.name : username.value
    expiresAt.value = typeof payload.expiresAt === 'string' ? payload.expiresAt : ''
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ username: username.value, name: name.value, expiresAt: expiresAt.value }))
    } catch { /* HttpOnly cookie authentication also works without browser storage. */ }
  }

  const logout = () => {
    username.value = ''; name.value = ''; expiresAt.value = ''
    try {
      localStorage.removeItem(STORAGE_KEY)
      localStorage.removeItem('cdk_full_code_cache_v1')
      sessionStorage.removeItem('cdk_recent_issued_v1')
      sessionStorage.removeItem('post_setup_pass')
      sessionStorage.removeItem('post_setup_user')
    } catch { /* Storage may be disabled. */ }
  }

  const restore = () => {
    try {
      const raw = localStorage.getItem(STORAGE_KEY)
      if (!raw) return
      save(JSON.parse(raw) as StoredAuth)
      if (!isLoggedIn.value) logout()
    } catch { logout() }
  }

  const verifySession = (): Promise<boolean> => {
    if (checking) return checking
    checking = (async () => {
      try {
        const response = await fetch('/api/v1/auth/admin/me', { credentials: 'same-origin', redirect: 'error', cache: 'no-store' })
        if (!response.ok) { logout(); return false }
        const data = await response.json()
        if (data.is_admin !== true || typeof data.username !== 'string' || !data.expires_at) {
          logout(); return false
        }
        save({ username: data.username, name: data.name || data.username, expiresAt: data.expires_at })
        return isLoggedIn.value
      } catch { return false }
      finally { checking = null }
    })()
    return checking
  }

  return { username, name, expiresAt, isLoggedIn, restore, save, logout, verifySession }
})
