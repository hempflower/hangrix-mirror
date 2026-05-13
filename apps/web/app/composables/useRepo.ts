import type { PublicRepo } from '~/types/repo'

// Cache repo metadata across components for the lifetime of the page/route.
// Re-fetches when owner/name change.
export function useRepo(owner: () => string, name: () => string) {
  const key = computed(() => `repo:${owner()}/${name()}`)

  const repo = useState<PublicRepo | null>(key.value, () => null)
  const error = useState<string | null>(`${key.value}:error`, () => null)
  const loading = useState<boolean>(`${key.value}:loading`, () => false)

  async function load(force = false) {
    if (repo.value && !force) return repo.value
    if (loading.value) return repo.value
    loading.value = true
    error.value = null
    try {
      const data = await $fetch<PublicRepo>(`/api/repos/${owner()}/${name()}`, {
        credentials: 'include',
      })
      repo.value = data
      return data
    } catch (e: any) {
      error.value = e?.data?.error ?? 'load failed'
      repo.value = null
      return null
    } finally {
      loading.value = false
    }
  }

  // Re-fetch if route params change
  watch(key, async () => {
    repo.value = null
    error.value = null
    await load(true)
  })

  return { repo, error, loading, load }
}
