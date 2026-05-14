import type { RepoRefs } from '~/types/repo'

// Cache repo refs across components for the lifetime of the page/route, so
// the sidebar and page reuse one fetch. `emptyRepo` is a derived flag for
// callers that want to gate UI on "has any commits yet".
export function useRepoRefs(owner: () => string, name: () => string) {
  const key = computed(() => `repo-refs:${owner()}/${name()}`)

  const refs = useState<RepoRefs | null>(key.value, () => null)
  const error = useState<string | null>(`${key.value}:error`, () => null)
  const loading = useState<boolean>(`${key.value}:loading`, () => false)

  const emptyRepo = computed(() => {
    const r = refs.value
    if (!r) return false
    return !r.default_branch_sha && (r.branches?.length ?? 0) === 0
  })

  async function load(force = false) {
    if (refs.value && !force) return refs.value
    if (loading.value) return refs.value
    loading.value = true
    error.value = null
    try {
      const data = await $fetch<RepoRefs>(`/api/repos/${owner()}/${name()}/refs`, {
        credentials: 'include',
      })
      // Normalize null → [] so downstream `.length` reads can't crash if the
      // server ever regresses on the array contract.
      refs.value = {
        ...data,
        branches: data.branches ?? [],
        tags: data.tags ?? [],
      }
      return refs.value
    } catch (e: any) {
      error.value = e?.data?.error ?? 'load failed'
      refs.value = null
      return null
    } finally {
      loading.value = false
    }
  }

  watch(key, async () => {
    refs.value = null
    error.value = null
    await load(true)
  })

  return { refs, emptyRepo, error, loading, load }
}
