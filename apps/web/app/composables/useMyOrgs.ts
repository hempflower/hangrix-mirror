import type { PublicOrg, OrgListResp } from '~/types/org'

const STATE_KEY = 'my-orgs'

/**
 * useMyOrgs is shared cached state holding the orgs the current user
 * belongs to. The new-repo form and ownership-transfer dialog both read
 * from this list to populate their owner pickers; refresh() forces a
 * round-trip after the user creates / leaves an org.
 */
export function useMyOrgs() {
  const orgs = useState<PublicOrg[] | null>(STATE_KEY, () => null)

  async function refresh(): Promise<PublicOrg[]> {
    try {
      const resp = await $fetch<OrgListResp>('/api/orgs', {
        params: { member_of: 'me' },
        credentials: 'include',
      })
      orgs.value = resp.items ?? []
    } catch {
      orgs.value = []
    }
    return orgs.value ?? []
  }

  return { orgs, refresh }
}
