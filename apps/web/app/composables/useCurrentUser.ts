import type { User } from '~/types/user'

const STATE_KEY = 'current-user'

export function useCurrentUser() {
  const user = useState<User | null>(STATE_KEY, () => null)

  async function refresh(): Promise<User | null> {
    try {
      const data = await $fetch<User>('/api/auth/me', {
        credentials: 'include',
      })
      user.value = data
    } catch {
      user.value = null
    }
    return user.value
  }

  async function logout() {
    try {
      await $fetch('/api/auth/logout', { method: 'POST', credentials: 'include' })
    } catch { /* ignore */ }
    user.value = null
  }

  return { user, refresh, logout }
}
