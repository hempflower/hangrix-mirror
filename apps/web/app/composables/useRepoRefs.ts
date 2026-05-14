import type { RepoRefs } from '~/types/repo'

// Module-level map of in-flight fetches keyed by `repo-refs:owner/name`. The
// repo layout mounts the sidebar before the page, so both call `load()` in
// quick succession on a fresh navigation; without dedupe the second caller
// would short-circuit on `loading.value === true` and resolve with the still-
// null refs, leaving the branch selector empty even after the sidebar's
// fetch lands. By sharing one promise here, every caller awaits the same
// resolution and sees the populated `refs.value` afterwards.
const inflight = new Map<string, Promise<RepoRefs | null>>()

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
    const k = key.value
    const existing = inflight.get(k)
    if (existing) return existing

    loading.value = true
    error.value = null
    const promise = (async () => {
      try {
        const data = await $fetch<RepoRefs>(`/api/repos/${owner()}/${name()}/refs`, {
          credentials: 'include',
        })
        // Normalize null → [] so downstream `.length` reads can't crash if
        // the server ever regresses on the array contract.
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
        inflight.delete(k)
      }
    })()
    inflight.set(k, promise)
    return promise
  }

  watch(key, async () => {
    refs.value = null
    error.value = null
    await load(true)
  })

  return { refs, emptyRepo, error, loading, load }
}
