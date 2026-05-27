import type { LifecycleSettings, PlatformSettings } from '~/types/platform-settings'

/**
 * Fetch and patch platform lifecycle settings (container idle stop, archive
 * removal, periodic check intervals).  Callers can PATCH individual fields
 * on blur without sending the whole object.
 */
export function useLifecycleSettings() {
  const settings = useState<PlatformSettings | null>('lifecycle-settings', () => null)
  const loading = useState<boolean>('lifecycle-settings:loading', () => false)
  const error = useState<string | null>('lifecycle-settings:error', () => null)

  async function load() {
    loading.value = true
    error.value = null
    try {
      const data = await $fetch<PlatformSettings>('/api/admin/platform-settings', {
        credentials: 'include',
      })
      settings.value = data
    } catch (e: any) {
      error.value = e?.data?.error ?? 'Failed to load platform settings'
    } finally {
      loading.value = false
    }
  }

  /** PATCH a single lifecycle field and optimistically update local state. */
  async function patchField(field: keyof LifecycleSettings, value: number) {
    const prev = settings.value?.lifecycle[field]
    // optimistically apply
    if (settings.value) {
      settings.value = {
        ...settings.value,
        lifecycle: { ...settings.value.lifecycle, [field]: value },
      }
    }
    try {
      const data = await $fetch<PlatformSettings>('/api/admin/platform-settings', {
        method: 'PATCH',
        credentials: 'include',
        body: { lifecycle: { [field]: value } },
      })
      settings.value = data
    } catch (e: any) {
      // rollback
      if (settings.value) {
        settings.value = {
          ...settings.value,
          lifecycle: { ...settings.value.lifecycle, [field]: prev },
        }
      }
      error.value = e?.data?.error ?? `Failed to update ${field}`
    }
  }

  return { settings, loading, error, load, patchField }
}
