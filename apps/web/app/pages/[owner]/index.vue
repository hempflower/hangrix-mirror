<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { ArrowRight, Building2, FolderGit2, Settings, User } from 'lucide-vue-next'

import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { MemberListResp, PublicMember, PublicOrg } from '~/types/org'
import type { PublicRepo, RepoListResp } from '~/types/repo'
import type { User as CurrentUser } from '~/types/user'

const { t } = useI18n()
const route = useRoute()

setBreadcrumbs(() => [{ label: String(route.params.owner ?? '') }])
const { user: currentUser, refresh: refreshUser } = useCurrentUser()

const ownerName = computed(() => String(route.params.owner ?? ''))
useHead({ title: () => `${ownerName.value} - ${t('app.name')}` })

// "kind === null" is the loading-but-unresolved state; we render skeleton.
const kind = ref<'user' | 'org' | null>(null)
const ownerUser = ref<CurrentUser | null>(null)
const ownerOrg = ref<PublicOrg | null>(null)
const repos = ref<PublicRepo[]>([])
const members = ref<PublicMember[]>([])
const loading = ref(false)
const loadError = ref<string | null>(null)
const tab = ref<'repos' | 'members'>('repos')

const isSelf = computed(() => kind.value === 'user' && currentUser.value?.id === ownerUser.value?.id)
const isOrgMember = computed(() => {
  if (kind.value !== 'org' || !currentUser.value) return false
  return members.value.some(m => m.user_id === currentUser.value!.id)
})
const isOrgOwner = computed(() => {
  if (kind.value !== 'org' || !currentUser.value) return false
  return members.value.some(m => m.user_id === currentUser.value!.id && m.role === 'owner')
})

const canManageOrg = computed(() => {
  if (currentUser.value?.role === 'admin') return true
  return isOrgOwner.value
})

function shortInitial(s: string) {
  return s ? s.charAt(0).toUpperCase() : '?'
}

function formatDate(s: string) {
  try {
    return new Date(s).toLocaleString()
  } catch {
    return s
  }
}

async function loadAsUser(name: string): Promise<boolean> {
  // The user GET-by-id endpoint exists but not GET-by-username for non-self
  // callers. Use the public repos-by-username route as a probe: a 404 there
  // means neither a user nor (after the next try) an org exists.
  try {
    const list = await $fetch<RepoListResp>(`/api/users/${name}/repos`, { credentials: 'include' })
    repos.value = list.items
    ownerUser.value = {
      id: 0,
      username: name,
      email: '',
      role: 'user',
      disabled: false,
      created_at: '',
    }
    kind.value = 'user'
    return true
  } catch (e: any) {
    if (e?.response?.status === 404) return false
    throw e
  }
}

async function loadAsOrg(name: string): Promise<boolean> {
  try {
    ownerOrg.value = await $fetch<PublicOrg>(`/api/orgs/${name}`, { credentials: 'include' })
  } catch (e: any) {
    if (e?.response?.status === 404) return false
    throw e
  }
  const [reposResp, membersResp] = await Promise.all([
    $fetch<RepoListResp>(`/api/orgs/${name}/repos`, { credentials: 'include' }),
    $fetch<MemberListResp>(`/api/orgs/${name}/members`, { credentials: 'include' }),
  ])
  repos.value = reposResp.items
  members.value = membersResp.items
  kind.value = 'org'
  return true
}

async function load() {
  if (!ownerName.value) return
  loading.value = true
  loadError.value = null
  kind.value = null
  repos.value = []
  members.value = []
  ownerUser.value = null
  ownerOrg.value = null
  try {
    if (!currentUser.value) await refreshUser()
    if (await loadAsUser(ownerName.value)) return
    if (await loadAsOrg(ownerName.value)) return
    loadError.value = t('owner.notFound')
  } catch (e: any) {
    loadError.value = e?.data?.error ?? t('owner.loadFailed')
  } finally {
    loading.value = false
  }
}

watch(ownerName, load)
onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <p v-if="loadError" class="text-sm text-destructive">
      {{ loadError }}
    </p>

    <p v-if="loading && !kind" class="text-sm text-muted-foreground">
      {{ t('common.loading') }}
    </p>

    <template v-if="kind === 'user' && ownerUser">
      <header class="flex items-start gap-4">
        <Avatar class="size-16 rounded-lg">
          <AvatarFallback class="rounded-lg bg-primary/10 text-2xl font-medium text-primary">
            {{ shortInitial(ownerUser.username) }}
          </AvatarFallback>
        </Avatar>
        <div class="flex-1 space-y-1">
          <div class="flex items-center gap-2">
            <h1 class="text-2xl font-semibold tracking-tight">
              {{ ownerUser.username }}
            </h1>
            <Badge variant="secondary" class="gap-1">
              <User class="size-3" />
              {{ t('owner.kindUser') }}
            </Badge>
          </div>
          <p class="text-sm text-muted-foreground">
            {{ t('owner.userSubtitle') }}
          </p>
        </div>
        <Button v-if="isSelf" as-child variant="outline">
          <NuxtLink to="/profile">
            <Settings class="size-4" />
            {{ t('owner.editProfile') }}
          </NuxtLink>
        </Button>
      </header>
    </template>

    <template v-if="kind === 'org' && ownerOrg">
      <header class="flex items-start gap-4">
        <Avatar class="size-16 rounded-lg">
          <AvatarFallback class="rounded-lg bg-primary/10 text-2xl font-medium text-primary">
            {{ shortInitial(ownerOrg.name) }}
          </AvatarFallback>
        </Avatar>
        <div class="flex-1 space-y-1">
          <div class="flex items-center gap-2">
            <h1 class="text-2xl font-semibold tracking-tight">
              {{ ownerOrg.display_name || ownerOrg.name }}
            </h1>
            <Badge variant="secondary" class="gap-1">
              <Building2 class="size-3" />
              {{ t('owner.kindOrg') }}
            </Badge>
          </div>
          <p class="text-sm text-muted-foreground">
            {{ ownerOrg.description || t('owner.orgSubtitle') }}
          </p>
        </div>
        <div class="flex items-center gap-2">
          <Button v-if="isOrgMember" as-child variant="outline">
            <NuxtLink :to="`/repos/new?owner=${ownerOrg.name}`">
              {{ t('repo.create') }}
            </NuxtLink>
          </Button>
          <Button v-if="canManageOrg" as-child variant="outline">
            <NuxtLink :to="`/${ownerOrg.name}/settings`">
              <Settings class="size-4" />
              {{ t('owner.orgSettings') }}
            </NuxtLink>
          </Button>
        </div>
      </header>
    </template>

    <div v-if="kind" class="flex items-center gap-2 border-b">
      <button
        :class="['border-b-2 px-3 py-2 text-sm', tab === 'repos' ? 'border-primary font-medium' : 'border-transparent text-muted-foreground']"
        type="button"
        @click="tab = 'repos'"
      >
        {{ t('owner.tabsRepos', { n: repos.length }) }}
      </button>
      <button
        v-if="kind === 'org'"
        :class="['border-b-2 px-3 py-2 text-sm', tab === 'members' ? 'border-primary font-medium' : 'border-transparent text-muted-foreground']"
        type="button"
        @click="tab = 'members'"
      >
        {{ t('owner.tabsMembers', { n: members.length }) }}
      </button>
    </div>

    <section v-if="kind && tab === 'repos'" class="space-y-4">
      <p v-if="repos.length === 0" class="text-sm text-muted-foreground">
        {{ t('repo.empty') }}
      </p>
      <div v-else class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Card v-for="r in repos" :key="r.id" class="transition-shadow hover:shadow-md">
          <CardHeader>
            <div class="flex items-center justify-between gap-2">
              <CardTitle class="truncate text-base">
                <NuxtLink :to="`/${r.owner_name}/${r.name}`" class="hover:underline">
                  {{ r.owner_name }} / {{ r.name }}
                </NuxtLink>
              </CardTitle>
              <Badge :variant="r.visibility === 'private' ? 'outline' : 'secondary'">
                {{ t(`repo.visibility${r.visibility === 'private' ? 'Private' : 'Public'}`) }}
              </Badge>
            </div>
            <CardDescription class="line-clamp-2 min-h-[2.5rem]">
              {{ r.description || '—' }}
            </CardDescription>
          </CardHeader>
          <CardContent class="flex items-center justify-between text-xs text-muted-foreground">
            <span class="truncate">{{ formatDate(r.updated_at) }}</span>
            <NuxtLink
              :to="`/${r.owner_name}/${r.name}`"
              class="inline-flex items-center gap-1 text-foreground hover:text-primary"
            >
              <span>{{ r.default_branch }}</span>
              <ArrowRight class="size-3" />
            </NuxtLink>
          </CardContent>
        </Card>
      </div>
    </section>

    <section v-if="kind === 'org' && tab === 'members'" class="space-y-2">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{{ t('common.username') }}</TableHead>
            <TableHead>{{ t('org.role') }}</TableHead>
            <TableHead>{{ t('org.addedAt') }}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow v-for="m in members" :key="m.user_id">
            <TableCell>
              <NuxtLink :to="`/${m.username}`" class="hover:underline">
                {{ m.username }}
              </NuxtLink>
            </TableCell>
            <TableCell>
              <Badge :variant="m.role === 'owner' ? 'default' : 'outline'">
                {{ t(`org.role${m.role === 'owner' ? 'Owner' : 'Member'}`) }}
              </Badge>
            </TableCell>
            <TableCell class="text-sm text-muted-foreground">
              {{ formatDate(m.added_at) }}
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
    </section>
  </div>
</template>
