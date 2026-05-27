import type { PlatformSetting, PlatformSettingResp, PlatformSettingsListResp } from '~/types/platform-settings'

/**
 * Fetch and patch platform lifecycle settings (container idle stop, archive
 * removal, abandoned cleanup).  Settings are stored as flat key-value pairs
 * on the server; callers PATCH individual keys via
 *   PATCH /api/admin/platform-settings/{key}
 * with body { value: "1h" }.
 */
export function useLifecycleSettings() {
  const settings = useState<PlatformSetting[]>('lifecycle-settings', () => [])
  const loading = useState<boolean>('lifecycle-settings:loading', () => false)
  const error = useState<string | null>('lifecycle-settings:error', () => null)

  async function load() {
    loading.value = true
    error.value = null
    try {
      const data = await $fetch<PlatformSettingsListResp>('/api/admin/platform-settings', {
        credentials: 'include',
      })
      settings.value = data.items ?? []
    } catch (e: any) {
      error.value = e?.data?.error ?? 'Failed to load platform settings'
    } finally {
      loading.value = false
    }
  }

  /** PATCH a single setting by key, optimistically updating local state. */
  async function patchSetting(key: string, value: string) {
    const prev = settings.value.find(s => s.key === key)
    // optimistically apply
    settings.value = settings.value.map(s =>
      s.key === key ? { ...s, value } : s,
    )
    try {
      const updated = await $fetch<PlatformSettingResp>(`/api/admin/platform-settings/${key}`, {
        method: 'PATCH',
        credentials: 'include',
        body: { value },
      })
      // reconcile with server response
      settings.value = settings.value.map(s =>
        s.key === updated.key ? { ...s, value: updated.value } : s,
      )
    } catch (e: any) {
      // rollback
      if (prev) {
        settings.value = settings.value.map(s =>
          s.key === key ? prev : s,
        )
      }
      throw e
    }
  }

  return { settings, loading, error, load, patchSetting }
}
