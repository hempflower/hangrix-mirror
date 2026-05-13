<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import {
  Box,
  Check,
  ChevronRight,
  Copy,
  FileText,
  Folder,
  GitBranch,
  GitCommit,
  KeyRound,
} from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableRow,
} from '@/components/ui/table'
import RepoReadme from '@/components/repo/RepoReadme.vue'
import type { Commit, EntryWithCommit, RepoRefs, TreeEntry, TreeView } from '~/types/repo'
import { relativeTime } from '~/utils/time'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { user } = useCurrentUser()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))

const { repo, error: repoError, load: loadRepo } = useRepo(() => owner.value, () => name.value)

const refs = ref<RepoRefs | null>(null)
const tab = ref<'files' | 'commits'>((route.query.tab as any) === 'commits' ? 'commits' : 'files')

const currentRef = ref<string>('')
const currentPath = ref<string>('')

const treeView = ref<TreeView | null>(null)
const treeError = ref<string | null>(null)
const treeLoading = ref(false)
const emptyRepo = ref(false)

const commits = ref<Commit[]>([])
const commitsLoading = ref(false)
const commitsError = ref<string | null>(null)

const copied = ref(false)

// Clone URL uses the browser origin so it works whether dev (:3001 proxy) or prod.
const cloneUrl = computed(() => {
  if (!repo.value) return ''
  const origin = import.meta.client ? window.location.origin : ''
  return `${origin}/git/${repo.value.owner_username}/${repo.value.name}.git`
})

const canManage = computed(() => {
  if (!repo.value || !user.value) return false
  return user.value.role === 'admin' || user.value.id === repo.value.owner_id
})

const breadcrumbParts = computed(() => {
  if (!currentPath.value) return []
  const parts = currentPath.value.split('/').filter(Boolean)
  const acc: { name: string; path: string }[] = []
  let p = ''
  for (const seg of parts) {
    p = p ? `${p}/${seg}` : seg
    acc.push({ name: seg, path: p })
  }
  return acc
})

const branchList = computed(() => refs.value?.branches ?? [])
const tagList = computed(() => refs.value?.tags ?? [])
const entries = computed<EntryWithCommit[]>(() => treeView.value?.entries ?? [])
const headCommit = computed<Commit | undefined>(() => treeView.value?.last_commit)
const totalCommits = computed<number>(() => treeView.value?.total_commits ?? 0)

async function loadRefs() {
  try {
    refs.value = await $fetch<RepoRefs>(`/api/repos/${owner.value}/${name.value}/refs`, {
      credentials: 'include',
    })
    if (!refs.value.default_branch_sha && refs.value.branches.length === 0) {
      emptyRepo.value = true
    } else {
      emptyRepo.value = false
    }
    const qRef = route.query.ref ? String(route.query.ref) : ''
    currentRef.value = qRef || refs.value.default_branch || ''
    currentPath.value = route.query.path ? String(route.query.path) : ''
  } catch (e: any) {
    treeError.value = e?.data?.error ?? t('repo.loadFailed')
  }
}

async function loadTreeView() {
  if (!currentRef.value || emptyRepo.value) {
    treeView.value = null
    return
  }
  treeLoading.value = true
  treeError.value = null
  try {
    const data = await $fetch<TreeView>(
      `/api/repos/${owner.value}/${name.value}/tree-view`,
      {
        credentials: 'include',
        query: { ref: currentRef.value, path: currentPath.value },
      },
    )
    treeView.value = data
  } catch (e: any) {
    treeError.value = e?.data?.error ?? t('repo.loadFailed')
    treeView.value = null
  } finally {
    treeLoading.value = false
  }
}

async function loadCommits() {
  if (emptyRepo.value) {
    commits.value = []
    return
  }
  commitsLoading.value = true
  commitsError.value = null
  try {
    const data = await $fetch<Commit[]>(
      `/api/repos/${owner.value}/${name.value}/commits`,
      {
        credentials: 'include',
        query: { ref: currentRef.value || undefined, limit: 50 },
      },
    )
    commits.value = data ?? []
  } catch (e: any) {
    commitsError.value = e?.data?.error ?? t('repo.loadFailed')
    commits.value = []
  } finally {
    commitsLoading.value = false
  }
}

function onSelectRef(v: any) {
  currentRef.value = String(v)
  currentPath.value = ''
  router.replace({ query: { ...route.query, ref: currentRef.value, path: undefined } })
}

function onBreadcrumbClick(p: string) {
  currentPath.value = p
  router.replace({ query: { ...route.query, ref: currentRef.value, path: p || undefined } })
}

function entryHref(entry: EntryWithCommit): string {
  if (entry.kind === 'tree') {
    const qs = new URLSearchParams()
    if (currentRef.value) qs.set('ref', currentRef.value)
    qs.set('path', entry.path)
    return `/${owner.value}/${name.value}?${qs.toString()}`
  }
  if (entry.kind === 'blob' || entry.kind === 'executable') {
    // GitHub-style: /<owner>/<name>/blob/<ref>/<path>
    // Ref is encoded so branch names with `/` survive (e.g. `feature/x`
    // becomes `feature%2Fx` — Vue Router decodes it back at param read).
    // Path segments are encoded individually so `#` / `?` etc. don't bleed
    // into the URL grammar, but `/` between segments stays literal.
    const encRef = encodeURIComponent(currentRef.value || '')
    const encPath = entry.path.split('/').map(encodeURIComponent).join('/')
    return `/${owner.value}/${name.value}/blob/${encRef}/${encPath}`
  }
  return ''
}

function onEntryClick(entry: EntryWithCommit, ev: MouseEvent) {
  if (entry.kind === 'tree') {
    ev.preventDefault()
    currentPath.value = entry.path
    router.replace({ query: { ...route.query, ref: currentRef.value, path: entry.path } })
  }
}

function entryIcon(kind: TreeEntry['kind']) {
  switch (kind) {
    case 'tree': return Folder
    case 'symlink': return KeyRound
    case 'submodule': return Box
    default: return FileText
  }
}

async function copyClone() {
  if (!cloneUrl.value) return
  try {
    await navigator.clipboard.writeText(cloneUrl.value)
    copied.value = true
    setTimeout(() => { copied.value = false }, 1500)
  } catch { /* ignore */ }
}

function shortSha(sha: string) {
  return sha.slice(0, 7)
}

function firstLine(msg: string | undefined) {
  if (!msg) return ''
  return msg.split('\n', 1)[0]
}

function formatDate(s: string) {
  try {
    return new Date(s).toLocaleString()
  } catch {
    return s
  }
}

function rel(iso: string | undefined) {
  return relativeTime(iso ?? null, t)
}

function authorInitial(c: Commit | undefined) {
  return c?.author?.name?.charAt(0)?.toUpperCase() ?? '?'
}

watch(tab, (v) => {
  router.replace({ query: { ...route.query, tab: v === 'files' ? undefined : v } })
  if (v === 'commits' && commits.value.length === 0 && !commitsError.value) {
    loadCommits()
  }
})

watch(() => route.query.tab, (q) => {
  const next = q === 'commits' ? 'commits' : 'files'
  if (next !== tab.value) {
    tab.value = next
    if (next === 'commits' && commits.value.length === 0 && !commitsError.value) {
      loadCommits()
    }
  }
})

watch(currentRef, () => {
  if (!emptyRepo.value) {
    loadTreeView()
    if (tab.value === 'commits') loadCommits()
  }
})

watch(currentPath, () => {
  if (!emptyRepo.value) loadTreeView()
})

watch(() => route.query.path, (p) => {
  const np = p ? String(p) : ''
  if (np !== currentPath.value) currentPath.value = np
})

watch(() => route.query.ref, (r) => {
  const nr = r ? String(r) : (refs.value?.default_branch ?? '')
  if (nr && nr !== currentRef.value) currentRef.value = nr
})

onMounted(async () => {
  await loadRepo()
  if (!repoError.value) {
    await loadRefs()
    if (!emptyRepo.value) {
      await loadTreeView()
      if (tab.value === 'commits') await loadCommits()
    }
  }
})
</script>

<template>
  <div class="space-y-6">
    <p v-if="repoError" class="text-sm text-destructive">
      {{ repoError }}
    </p>

    <template v-if="repo">
      <header class="space-y-2">
        <div class="flex flex-wrap items-center gap-2">
          <h1 class="text-2xl font-semibold tracking-tight">
            <NuxtLink :to="`/${repo.owner_username}/${repo.name}`" class="hover:underline">
              {{ repo.owner_username }} / {{ repo.name }}
            </NuxtLink>
          </h1>
          <Badge :variant="repo.visibility === 'private' ? 'outline' : 'secondary'">
            {{ t(`repo.visibility${repo.visibility === 'private' ? 'Private' : 'Public'}`) }}
          </Badge>
        </div>
        <p v-if="repo.description" class="text-sm text-muted-foreground">
          {{ repo.description }}
        </p>
      </header>

      <Tabs v-model="tab" class="space-y-4">
        <div class="flex flex-wrap items-center gap-3">
          <TabsList>
            <TabsTrigger value="files">
              {{ t('repo.tabs.files') }}
            </TabsTrigger>
            <TabsTrigger value="commits">
              {{ t('repo.tabs.commits') }}
            </TabsTrigger>
          </TabsList>
          <!-- Clone URL pinned to the right of the tab strip, matching the
               row height. Truncates for long URLs; copy button at the end. -->
          <div class="ml-auto flex min-w-0 max-w-xl flex-1 items-center gap-2 rounded-md border bg-muted/30 px-2 py-1">
            <span class="shrink-0 text-xs uppercase text-muted-foreground">{{ t('repo.cloneUrl') }}</span>
            <code class="min-w-0 flex-1 truncate font-mono text-xs">{{ cloneUrl }}</code>
            <Button size="sm" variant="ghost" class="h-7 shrink-0 gap-1 px-2" @click="copyClone">
              <component :is="copied ? Check : Copy" class="size-3" />
              {{ copied ? t('repo.copied') : t('repo.copy') }}
            </Button>
          </div>
        </div>

        <TabsContent value="files" class="space-y-4">
          <div v-if="emptyRepo" class="rounded-lg border border-dashed p-10 text-center">
            <GitBranch class="mx-auto size-10 text-muted-foreground" />
            <h2 class="mt-4 text-lg font-medium">
              {{ t('repo.files.emptyRepo') }}
            </h2>
            <p class="mt-1 text-sm text-muted-foreground">
              {{ t('repo.files.emptyRepoHint') }}
            </p>
            <div class="mx-auto mt-4 max-w-md rounded-md border bg-muted/30 p-2 text-left">
              <code class="block break-all font-mono text-xs">git clone {{ cloneUrl }}</code>
            </div>
          </div>

          <template v-else>
            <div class="flex flex-wrap items-center gap-3">
              <div class="flex items-center gap-2">
                <GitBranch class="size-4 text-muted-foreground" />
                <Select :model-value="currentRef" @update:model-value="onSelectRef">
                  <SelectTrigger class="w-[220px]">
                    <SelectValue :placeholder="t('repo.files.ref')" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup v-if="branchList.length > 0">
                      <SelectLabel>{{ t('repo.files.ref') }}</SelectLabel>
                      <SelectItem v-for="b in branchList" :key="`b-${b.name}`" :value="b.name">
                        {{ b.name }}
                      </SelectItem>
                    </SelectGroup>
                    <SelectGroup v-if="tagList.length > 0">
                      <SelectLabel>tags</SelectLabel>
                      <SelectItem v-for="tg in tagList" :key="`t-${tg.name}`" :value="tg.name">
                        {{ tg.name }}
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </div>

              <nav class="flex flex-wrap items-center gap-1 text-sm">
                <button
                  type="button"
                  class="text-muted-foreground hover:text-foreground"
                  @click="onBreadcrumbClick('')"
                >
                  {{ t('repo.files.rootBreadcrumb') }}
                </button>
                <template v-for="(part, idx) in breadcrumbParts" :key="part.path">
                  <ChevronRight class="size-3 text-muted-foreground" />
                  <button
                    v-if="idx < breadcrumbParts.length - 1"
                    type="button"
                    class="text-muted-foreground hover:text-foreground"
                    @click="onBreadcrumbClick(part.path)"
                  >
                    {{ part.name }}
                  </button>
                  <span v-else class="font-medium">{{ part.name }}</span>
                </template>
              </nav>
            </div>

            <Card class="gap-0 py-0">
              <CardContent class="p-0">
                <p v-if="treeError" class="p-3 text-sm text-destructive">
                  {{ treeError }}
                </p>
                <p v-else-if="treeLoading" class="p-3 text-sm text-muted-foreground">
                  {{ t('common.loading') }}
                </p>
                <template v-else>
                  <!-- Top header strip: latest commit + N commits link -->
                  <div
                    v-if="headCommit"
                    class="flex flex-wrap items-center gap-3 rounded-t-xl border-b bg-muted/40 px-4 py-2 text-sm"
                  >
                    <div class="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-medium text-primary">
                      {{ authorInitial(headCommit) }}
                    </div>
                    <span class="shrink-0 font-medium">{{ headCommit.author.name }}</span>
                    <NuxtLink
                      :to="`/${owner}/${name}/commits/${headCommit.sha}`"
                      class="min-w-0 flex-1 truncate text-muted-foreground hover:text-foreground hover:underline"
                      :title="headCommit.message"
                    >
                      {{ firstLine(headCommit.message) }}
                    </NuxtLink>
                    <code class="hidden font-mono text-xs text-muted-foreground sm:inline">{{ shortSha(headCommit.sha) }}</code>
                    <span class="text-xs text-muted-foreground" :title="formatDate(headCommit.committed_at)">
                      {{ rel(headCommit.committed_at) }}
                    </span>
                    <NuxtLink
                      :to="`/${owner}/${name}?tab=commits`"
                      class="ml-auto inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground hover:underline"
                    >
                      <GitCommit class="size-3" />
                      {{ t('repo.files.commitsCount', { n: totalCommits }) }}
                    </NuxtLink>
                  </div>

                  <!-- Tree rows table -->
                  <Table>
                    <TableBody>
                      <TableRow v-if="currentPath" class="cursor-pointer" @click="onBreadcrumbClick(breadcrumbParts.length > 1 ? breadcrumbParts[breadcrumbParts.length - 2].path : '')">
                        <TableCell class="w-[40%]">
                          <span class="inline-flex items-center gap-2">
                            <Folder class="size-4 text-muted-foreground" />
                            <span class="font-mono">..</span>
                          </span>
                        </TableCell>
                        <TableCell class="text-sm text-muted-foreground" />
                        <TableCell class="text-right text-xs text-muted-foreground" />
                      </TableRow>
                      <TableRow v-for="entry in entries" :key="entry.path">
                        <TableCell class="w-[40%]">
                          <span class="inline-flex items-center gap-2">
                            <component
                              :is="entryIcon(entry.kind)"
                              :class="['size-4', entry.kind === 'tree' ? 'text-sky-500' : 'text-muted-foreground']"
                            />
                            <template v-if="entry.kind === 'tree' || entry.kind === 'blob' || entry.kind === 'executable'">
                              <NuxtLink
                                :to="entryHref(entry)"
                                class="truncate font-mono hover:underline"
                                @click="(e: MouseEvent) => onEntryClick(entry, e)"
                              >
                                {{ entry.name }}
                              </NuxtLink>
                            </template>
                            <template v-else>
                              <span class="truncate font-mono text-muted-foreground" :title="entry.kind === 'submodule' ? t('repo.files.submoduleHint') : t('repo.files.symlinkHint')">
                                {{ entry.name }}
                              </span>
                            </template>
                          </span>
                        </TableCell>
                        <TableCell class="min-w-0 text-sm text-muted-foreground">
                          <template v-if="entry.last_commit">
                            <NuxtLink
                              :to="`/${owner}/${name}/commits/${entry.last_commit.sha}`"
                              class="block truncate hover:text-foreground hover:underline"
                              :title="entry.last_commit.message"
                            >
                              {{ firstLine(entry.last_commit.message) }}
                            </NuxtLink>
                          </template>
                          <template v-else>
                            {{ t('repo.files.noLastCommit') }}
                          </template>
                        </TableCell>
                        <TableCell class="text-right text-xs text-muted-foreground">
                          <span :title="entry.last_commit ? formatDate(entry.last_commit.committed_at) : ''">
                            {{ entry.last_commit ? rel(entry.last_commit.committed_at) : t('repo.files.noLastCommit') }}
                          </span>
                        </TableCell>
                      </TableRow>
                    </TableBody>
                  </Table>
                </template>
              </CardContent>
            </Card>

            <RepoReadme
              v-if="!currentPath"
              :owner="owner"
              :name="name"
              :ref-name="currentRef"
              :tree="entries"
            />
          </template>
        </TabsContent>

        <TabsContent value="commits" class="space-y-4">
          <Card class="gap-0 py-0">
            <CardContent class="p-0">
              <p v-if="commitsError" class="p-3 text-sm text-destructive">
                {{ commitsError }}
              </p>
              <p v-else-if="commitsLoading" class="p-3 text-sm text-muted-foreground">
                {{ t('common.loading') }}
              </p>
              <p v-else-if="commits.length === 0" class="p-6 text-center text-sm text-muted-foreground">
                {{ t('repo.commits.none') }}
              </p>
              <ul v-else class="divide-y">
                <li v-for="c in commits" :key="c.sha" class="hover:bg-muted/30">
                  <NuxtLink
                    :to="`/${owner}/${name}/commits/${c.sha}`"
                    class="flex items-center gap-3 px-4 py-2.5"
                  >
                    <GitCommit class="size-4 shrink-0 text-muted-foreground" />
                    <div class="min-w-0 flex-1">
                      <p class="truncate text-sm font-medium">{{ firstLine(c.message) }}</p>
                      <p class="text-xs text-muted-foreground">
                        {{ c.author.name }} · {{ formatDate(c.committed_at) }}
                      </p>
                    </div>
                    <code class="hidden font-mono text-xs text-muted-foreground sm:inline">{{ shortSha(c.sha) }}</code>
                  </NuxtLink>
                </li>
              </ul>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </template>
  </div>
</template>
