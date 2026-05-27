import type { PlatformSetting, PlatformSettingsListResp } from '~/types/platform-settings'

const DURATION_RE = /^\d+(s|m|h|d)$|^0$/

export function useLifecycleSettings() {
  const settings = ref<PlatformSetting[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function load() {
    loading.value = true
    error.value = null
    try {
      const res = await $fetch<PlatformSettingsListResp>('/api/admin/platform-settings', {
        credentials: 'include',
      })
      settings.value = res.settings ?? []
    } catch (e: any) {
      error.value = e?.data?.error ?? 'Failed to load lifecycle settings'
    } finally {
      loading.value = false
    }
  }

  function getValue(key: string): string {
    return settings.value.find(s => s.key === key)?.value ?? ''
  }

  function getDefaultValue(key: string): string {
    return settings.value.find(s => s.key === key)?.default_value ?? ''
  }

  function getUpdatedMeta(key: string): { at?: string | null; by?: string | null } {
    const s = settings.value.find(s => s.key === key)
    return { at: s?.updated_at, by: s?.updated_by }
  }

  function validateDuration(value: string): boolean {
    return DURATION_RE.test(value)
  }

  async function patch(key: string, value: string): Promise<boolean> {
    error.value = null
    try {
      const updated = await $fetch<PlatformSetting>(`/api/admin/platform-settings/${encodeURIComponent(key)}`, {
        method: 'PATCH',
        credentials: 'include',
        body: { value },
      })
      // Update local state with the server response
      const idx = settings.value.findIndex(s => s.key === key)
      if (idx !== -1) {
        settings.value[idx] = updated
      }
      return true
    } catch (e: any) {
      error.value = e?.data?.error ?? 'Failed to update setting'
      return false
    }
  }

  return {
    settings,
    loading,
    error,
    load,
    getValue,
    getDefaultValue,
    getUpdatedMeta,
    validateDuration,
    patch,
  }
}
