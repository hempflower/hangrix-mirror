<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  Bot,
  CircleDot,
  CornerDownRight,
  Diff as DiffIcon,
  GitBranch,
  GitCommit,
  GitMerge,
  Lock,
  MessageSquare,
  Plus,
} from 'lucide-vue-next'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import AgentSessionsView from '@/components/issue/AgentSessionsView.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import FileDiffList from '@/components/repo/FileDiffList.vue'
import type { Issue, IssueState, IssueTimeline, IssueMergeResp } from '~/types/issue'
import type { Commit, FileDiff } from '~/types/repo'
import { relativeTime } from '~/utils/time'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { user } = useCurrentUser()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
const number = computed(() => Number(route.params.number ?? 0))

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.tabs2.issues'), to: `${base}/issues` },
    { label: `#${number.value}` },
  ]
})

const { repo, load: loadRepo } = useRepo(() => owner.value, () => name.value)

const issue = ref<Issue | null>(null)
const issueError = ref<string | null>(null)
const timeline = ref<IssueTimeline | null>(null)
const diff = ref<FileDiff[]>([])
const diffError = ref<string | null>(null)
const commits = ref<Commit[]>([])
const parent = ref<Issue | null>(null)
const children = ref<Issue[]>([])

const commentBody = ref('')
const commentBusy = ref(false)
const commentError = ref<string | null>(null)

const stateBusy = ref(false)
const mergeBusy = ref(false)
const actionError = ref<string | null>(null)
const actionInfo = ref<string | null>(null)

type IssueTab = 'conversation' | 'commits' | 'diff' | 'agents'

// tab state is mirrored into ?tab= so the URL is shareable / refresh-stable.
// `conversation` is the implicit default — we drop the query key entirely
// when it's selected so deep links to "/issues/N" stay clean.
function parseTab(raw: unknown): IssueTab {
  if (raw === 'commits' || raw === 'diff' || raw === 'agents') return raw
  return 'conversation'
}
const tab = ref<IssueTab>(parseTab(route.query.tab))

watch(tab, (v) => {
  router.replace({
    query: { ...route.query, tab: v === 'conversation' ? undefined : v },
  })
})

watch(() => route.query.tab, (q) => {
  const next = parseTab(q)
  if (next !== tab.value) tab.value = next
})

const canManage = computed(() => {
  if (!repo.value || !user.value) return false
  return user.value.role === 'admin' || user.value.id === repo.value.owner_id
})

const canMerge = computed(() => {
  return canManage.value && issue.value?.state === 'open' && Boolean(issue.value?.head_sha)
})

// Aggregate +/- across every file in the issue diff. Parses each unified
// patch once and counts `+`/`-` lines (not the file-header `+++`/`---`).
// The result feeds the "Changes" card so users see the issue's footprint
// without having to expand individual files.
const diffStats = computed(() => {
  let added = 0
  let deleted = 0
  let filesChanged = 0
  for (const f of diff.value) {
    if (f.binary) {
      filesChanged++
      continue
    }
    let touched = false
    for (const line of f.patch.split('\n')) {
      if (line.startsWith('+++') || line.startsWith('---')) continue
      if (line.startsWith('+')) { added++; touched = true }
      else if (line.startsWith('-')) { deleted++; touched = true }
    }
    if (touched || f.status !== 'modified') filesChanged++
  }
  return { added, deleted, filesChanged }
})

async function loadIssue() {
  issueError.value = null
  try {
    issue.value = await $fetch<Issue>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}`,
      { credentials: 'include' },
    )
  } catch (e: any) {
    issueError.value = e?.data?.error ?? t('issue.listFailed')
    issue.value = null
  }
}

async function loadParent() {
  parent.value = null
  if (!issue.value || !issue.value.parent_number) return
  try {
    parent.value = await $fetch<Issue>(
      `/api/repos/${owner.value}/${name.value}/issues/${issue.value.parent_number}`,
      { credentials: 'include' },
    )
  } catch {
    parent.value = null
  }
}

async function loadChildren() {
  if (!issue.value) {
    children.value = []
    return
  }
  try {
    const res = await $fetch<Issue[]>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/children`,
      { credentials: 'include' },
    )
    children.value = res ?? []
  } catch {
    children.value = []
  }
}

async function loadTimeline() {
  try {
    timeline.value = await $fetch<IssueTimeline>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/timeline`,
      { credentials: 'include' },
    )
  } catch {
    timeline.value = { comments: [], events: [] }
  }
}

async function loadDiff() {
  diffError.value = null
  try {
    const data = await $fetch<FileDiff[]>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/diff`,
      { credentials: 'include' },
    )
    diff.value = data ?? []
  } catch (e: any) {
    diffError.value = e?.data?.error ?? t('issue.diffLoadFailed')
    diff.value = []
  }
}

async function loadCommits() {
  try {
    const data = await $fetch<Commit[]>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/commits`,
      { credentials: 'include' },
    )
    commits.value = data ?? []
  } catch {
    commits.value = []
  }
}

async function submitComment() {
  const body = commentBody.value.trim()
  if (!body || !issue.value) return
  commentBusy.value = true
  commentError.value = null
  try {
    await $fetch(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/comments`,
      {
        method: 'POST',
        credentials: 'include',
        body: { body },
      },
    )
    commentBody.value = ''
    await loadTimeline()
  } catch (e: any) {
    commentError.value = e?.data?.error ?? t('issue.commentForm.failed')
  } finally {
    commentBusy.value = false
  }
}

async function toggleState() {
  if (!issue.value) return
  stateBusy.value = true
  actionError.value = null
  try {
    const next: IssueState = issue.value.state === 'open' ? 'closed' : 'open'
    issue.value = await $fetch<Issue>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}`,
      {
        method: 'PATCH',
        credentials: 'include',
        body: { state: next },
      },
    )
    await loadTimeline()
  } catch (e: any) {
    actionError.value = e?.data?.error ?? t('issue.stateChangeFailed')
  } finally {
    stateBusy.value = false
  }
}

async function merge() {
  if (!issue.value || !repo.value) return
  if (!confirm(t('issue.mergeConfirm', { n: issue.value.number, base: issue.value.base_branch }))) return
  mergeBusy.value = true
  actionError.value = null
  actionInfo.value = null
  try {
    const res = await $fetch<IssueMergeResp>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/merge`,
      { method: 'POST', credentials: 'include', body: {} },
    )
    issue.value = res.issue
    actionInfo.value = t('issue.mergeOk', { base: res.issue.base_branch, mode: res.mode })
    await Promise.all([loadTimeline(), loadDiff()])
  } catch (e: any) {
    actionError.value = e?.data?.error ?? t('issue.mergeFailed')
  } finally {
    mergeBusy.value = false
  }
}

function rel(s?: string | null) { return relativeTime(s ?? null, t) }
function shortSha(s: string) { return s ? s.slice(0, 7) : '' }
function formatDate(s?: string | null) {
  if (!s) return ''
  try { return new Date(s).toLocaleString() } catch { return s }
}
function initialOf(s?: string) {
  return s ? s.charAt(0).toUpperCase() : '?'
}

function stateBadgeClass(s: IssueState) {
  switch (s) {
    case 'open': return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
    case 'merged': return 'bg-violet-500/15 text-violet-700 dark:text-violet-300'
    case 'closed': return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
  }
}
function stateBadgeIcon(s: IssueState) {
  if (s === 'merged') return GitMerge
  if (s === 'closed') return Lock
  return CircleDot
}

// timelineItems flattens comments + events into a single chronological list
// the template can iterate without picking out the kinds. Each item carries
// a discriminator the renderer switches on.
interface TimelineItem {
  key: string
  at: string
  kind: 'comment' | 'event'
  data: any
}
const timelineItems = computed<TimelineItem[]>(() => {
  const out: TimelineItem[] = []
  for (const c of timeline.value?.comments ?? []) {
    out.push({ key: `c-${c.id}`, at: c.created_at, kind: 'comment', data: c })
  }
  for (const e of timeline.value?.events ?? []) {
    out.push({ key: `e-${e.id}`, at: e.created_at, kind: 'event', data: e })
  }
  out.sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
  return out
})

function eventLabel(e: any): string {
  const name = e.actor_username || '—'
  switch (e.kind) {
    case 'commit_pushed': {
      const n = e.payload?.commits?.length ?? 0
      return t('issue.timeline.commitPushed', { name, n })
    }
    case 'branch_merged':
      return t('issue.timeline.branchMerged', {
        name,
        from: e.payload?.from_branch ?? '',
        into: e.payload?.into_branch ?? '',
        mode: e.payload?.mode ?? '',
      })
    case 'state_changed': {
      const to = e.payload?.to
      const from = e.payload?.from
      let transition = t('issue.timeline.transitionClosed')
      if (to === 'open' && from === 'closed') transition = t('issue.timeline.transitionOpenedFromClosed')
      else if (to === 'merged') transition = t('issue.timeline.transitionMerged')
      else if (to === 'open') transition = t('issue.timeline.transitionOpened')
      else if (to === 'closed') transition = t('issue.timeline.transitionClosed')
      return t('issue.timeline.stateChanged', { name, transition })
    }
    case 'title_changed':
      return t('issue.timeline.titleChanged', { name })
    default:
      return e.kind
  }
}

const cloneUrl = computed(() => {
  if (!repo.value) return ''
  const origin = import.meta.client ? window.location.origin : ''
  return `${origin}/git/${repo.value.owner_username}/${repo.value.name}.git`
})

const pushSnippet = computed(() => {
  if (!issue.value || !cloneUrl.value) return ''
  return [
    `git fetch origin`,
    `git checkout -b ${issue.value.branch_name} origin/${issue.value.base_branch}`,
    `# make changes`,
    `git add .`,
    `git commit -m "..."`,
    `git push -u origin ${issue.value.branch_name}`,
  ].join('\n')
})

// Auto-refresh keeps the page in sync with the server-side push pipeline:
// the PostReceive observer writes commit_pushed events after a CLI push, so
// every ~15s we re-pull issue + timeline + diff + children. We don't touch
// commentBody / parent / repo — those are either user-edited locally or
// immutable for the page's lifetime. Hidden tabs pause the poll so we don't
// burn requests while the user is elsewhere.
const REFRESH_INTERVAL_MS = 15_000
let refreshTimer: ReturnType<typeof setInterval> | null = null

async function refreshLive() {
  if (!issue.value) return
  await Promise.all([loadIssue(), loadTimeline(), loadDiff(), loadCommits(), loadChildren()])
}

function startRefreshTimer() {
  if (refreshTimer || typeof window === 'undefined') return
  refreshTimer = setInterval(() => {
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
    refreshLive()
  }, REFRESH_INTERVAL_MS)
}

function stopRefreshTimer() {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
}

onMounted(async () => {
  await loadRepo()
  await loadIssue()
  if (issue.value) {
    await Promise.all([loadTimeline(), loadDiff(), loadCommits(), loadParent(), loadChildren()])
  }
  startRefreshTimer()
})

onUnmounted(stopRefreshTimer)
</script>

<template>
  <div class="space-y-6">
    <p v-if="issueError" class="text-sm text-destructive">{{ issueError }}</p>

    <template v-if="issue">
      <header class="space-y-2">
        <div class="flex flex-wrap items-center gap-2">
          <h1 class="text-2xl font-semibold tracking-tight">
            {{ issue.title }}
            <span class="text-muted-foreground">#{{ issue.number }}</span>
          </h1>
          <Badge :class="stateBadgeClass(issue.state)" variant="secondary">
            <component :is="stateBadgeIcon(issue.state)" class="mr-1 size-3" />
            {{ t(`issue.state.${issue.state}`) }}
          </Badge>
        </div>
        <p class="text-sm text-muted-foreground">
          {{ t('issue.openedBy', { name: issue.author_username, time: rel(issue.created_at) }) }}
        </p>
      </header>

      <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div class="min-w-0 space-y-4">
          <Tabs v-model="tab">
            <TabsList>
              <TabsTrigger value="conversation">
                <MessageSquare class="size-4" />
                {{ t('issue.tabs.conversation') }}
              </TabsTrigger>
              <TabsTrigger value="commits">
                <GitCommit class="size-4" />
                {{ t('issue.tabs.commits') }}
                <span v-if="commits.length > 0" class="ml-1 text-xs text-muted-foreground">
                  {{ commits.length }}
                </span>
              </TabsTrigger>
              <TabsTrigger value="diff">
                <DiffIcon class="size-4" />
                {{ t('issue.tabs.diff') }}
              </TabsTrigger>
              <TabsTrigger value="agents">
                <Bot class="size-4" />
                {{ t('issue.tabs.agents') }}
              </TabsTrigger>
            </TabsList>

            <TabsContent value="conversation" class="space-y-3">
              <!-- Issue body: same comment-card shape as replies. The opener
                   is just the first comment with a different verb ("opened"
                   vs "commented") in the header strip. -->
              <Card class="gap-0 py-0">
                <CardContent class="p-0">
                  <div class="flex items-center gap-2 border-b bg-muted/40 px-3 py-2 text-xs">
                    <Avatar class="size-6 shrink-0">
                      <AvatarFallback class="bg-primary/10 text-[10px] text-primary">
                        {{ initialOf(issue.author_username) }}
                      </AvatarFallback>
                    </Avatar>
                    <span class="font-medium text-foreground">{{ issue.author_username }}</span>
                    <span class="text-muted-foreground">{{ t('issue.opened') }}</span>
                    <span class="text-muted-foreground" :title="formatDate(issue.created_at)">
                      {{ rel(issue.created_at) }}
                    </span>
                  </div>
                  <p class="whitespace-pre-wrap px-4 py-3 text-sm">
                    {{ issue.body || '—' }}
                  </p>
                </CardContent>
              </Card>

              <template v-for="it in timelineItems" :key="it.key">
                <!-- Comments render as full cards with a header strip — the
                     same layout GitHub uses on issue threads. -->
                <Card v-if="it.kind === 'comment'" class="gap-0 py-0">
                  <CardContent class="p-0">
                    <div class="flex items-center gap-2 border-b bg-muted/40 px-3 py-2 text-xs">
                      <Avatar class="size-6 shrink-0">
                        <AvatarFallback class="bg-primary/10 text-[10px] text-primary">
                          {{ initialOf(it.data.author_username) }}
                        </AvatarFallback>
                      </Avatar>
                      <span class="font-medium text-foreground">{{ it.data.author_username }}</span>
                      <span class="text-muted-foreground">{{ t('issue.commented') }}</span>
                      <span class="text-muted-foreground" :title="formatDate(it.data.created_at)">
                        {{ rel(it.data.created_at) }}
                      </span>
                    </div>
                    <p class="whitespace-pre-wrap px-4 py-3 text-sm">{{ it.data.body }}</p>
                  </CardContent>
                </Card>

                <!-- System events render as a thin inline strip between
                     comments — they're context, not threads of their own,
                     so they shouldn't get the heavy card chrome. -->
                <div v-else class="flex items-start gap-2 px-1 text-xs text-muted-foreground">
                  <div class="mt-1 size-2 shrink-0 rounded-full bg-muted-foreground/40" />
                  <div class="min-w-0 flex-1 space-y-1">
                    <p>
                      {{ eventLabel(it.data) }}
                      <span :title="formatDate(it.at)">· {{ rel(it.at) }}</span>
                    </p>
                    <ul
                      v-if="it.data.kind === 'commit_pushed'"
                      class="space-y-0.5"
                    >
                      <li v-for="c in (it.data.payload?.commits ?? [])" :key="c.sha">
                        <NuxtLink
                          :to="`/${owner}/${name}/commits/${c.sha}`"
                          class="hover:text-foreground hover:underline"
                        >
                          <code class="font-mono">{{ shortSha(c.sha) }}</code>
                          — {{ c.message.split('\n', 1)[0] }}
                        </NuxtLink>
                      </li>
                    </ul>
                  </div>
                </div>
              </template>

              <!-- Compose card: matches GitHub's "Add a comment" footer.
                   Header strip carries the active user's identity, body has
                   a tall textarea, footer holds the submit button. Tight
                   inner padding so the textarea dominates the card. -->
              <Card v-if="issue.state === 'open'" class="gap-0 py-0">
                <CardContent class="p-0">
                  <div class="flex items-center gap-2 border-b bg-muted/40 px-3 py-2 text-xs">
                    <Avatar class="size-6 shrink-0">
                      <AvatarFallback class="bg-primary/10 text-[10px] text-primary">
                        {{ initialOf(user?.username) }}
                      </AvatarFallback>
                    </Avatar>
                    <span class="font-medium text-foreground">{{ user?.username ?? t('issue.you') }}</span>
                    <span class="text-muted-foreground">{{ t('issue.commentForm.title') }}</span>
                  </div>
                  <div class="space-y-2 px-3 py-3">
                    <Textarea
                      v-model="commentBody"
                      rows="8"
                      class="min-h-44 resize-y text-sm leading-relaxed"
                      :placeholder="t('issue.commentForm.placeholder')"
                    />
                    <p v-if="commentError" class="text-sm text-destructive">
                      {{ commentError }}
                    </p>
                    <div class="flex justify-end">
                      <Button :disabled="commentBusy || !commentBody.trim()" @click="submitComment">
                        {{ commentBusy ? t('issue.commentForm.submitting') : t('issue.commentForm.submit') }}
                      </Button>
                    </div>
                  </div>
                </CardContent>
              </Card>
            </TabsContent>

            <TabsContent value="commits" class="space-y-4">
              <Card v-if="commits.length === 0" class="gap-0 py-0">
                <CardContent class="p-4 text-sm text-muted-foreground">
                  {{ t('issue.commitsEmpty', { branch: issue.branch_name }) }}
                </CardContent>
              </Card>
              <Card v-else class="gap-0 py-0">
                <CardContent class="p-0">
                  <ul class="divide-y">
                    <li v-for="c in commits" :key="c.sha" class="hover:bg-muted/30">
                      <NuxtLink
                        :to="`/${owner}/${name}/commits/${c.sha}`"
                        class="flex items-center gap-3 px-4 py-2.5"
                      >
                        <GitCommit class="size-4 shrink-0 text-muted-foreground" />
                        <div class="min-w-0 flex-1">
                          <p class="truncate text-sm font-medium">
                            {{ c.message.split('\n', 1)[0] }}
                          </p>
                          <p class="text-xs text-muted-foreground">
                            {{ c.author.name }}
                            <span :title="formatDate(c.committed_at)">· {{ rel(c.committed_at) }}</span>
                          </p>
                        </div>
                        <code class="hidden font-mono text-xs text-muted-foreground sm:inline">
                          {{ shortSha(c.sha) }}
                        </code>
                      </NuxtLink>
                    </li>
                  </ul>
                </CardContent>
              </Card>
            </TabsContent>

            <TabsContent value="diff" class="space-y-4">
              <p v-if="diffError" class="text-sm text-destructive">{{ diffError }}</p>
              <Card v-if="diff.length === 0" class="gap-0">
                <CardContent class="p-4 text-sm text-muted-foreground">
                  {{ t('issue.diffEmpty', { branch: issue.branch_name }) }}
                </CardContent>
              </Card>
              <FileDiffList
                v-else
                :diffs="diff"
                :owner="owner"
                :name="name"
                :ref-before="issue.base_branch"
                :ref-after="issue.branch_name"
              />
            </TabsContent>

            <TabsContent value="agents" class="space-y-4">
              <AgentSessionsView
                :active="tab === 'agents'"
                :owner="owner"
                :name="name"
                :issue-number="Number(number)"
              />
            </TabsContent>
          </Tabs>
        </div>

        <aside class="space-y-4">
          <Card class="gap-0 py-0">
            <CardContent class="space-y-3 p-4 text-sm">
              <div class="flex items-center gap-2 text-xs text-muted-foreground">
                <GitBranch class="size-3" />
                <span>{{ t('issue.branch') }}</span>
              </div>
              <code class="block break-all rounded bg-muted/40 px-2 py-1 font-mono text-xs">
                {{ issue.branch_name }}
              </code>
              <div class="text-xs text-muted-foreground">
                {{ t('issue.base') }}:
                <code class="font-mono">{{ issue.base_branch }}</code>
              </div>
              <div class="text-xs text-muted-foreground">
                {{ t('issue.headSHA') }}:
                <code v-if="issue.head_sha" class="font-mono">{{ shortSha(issue.head_sha) }}</code>
                <span v-else>{{ t('issue.noHead') }}</span>
              </div>
              <div v-if="issue.merge_commit_sha" class="text-xs text-muted-foreground">
                {{ t('issue.mergedSHA') }}:
                <code class="font-mono">{{ shortSha(issue.merge_commit_sha) }}</code>
              </div>
              <!-- Branch totals: only meaningful once there's anything on
                   the issue branch beyond base. Hidden for unborn / empty
                   branches so the sidebar doesn't show "0 files / +0 −0". -->
              <div
                v-if="diffStats.filesChanged > 0"
                class="space-y-1 border-t pt-3 text-xs text-muted-foreground"
              >
                <p class="flex items-center gap-2 text-xs text-muted-foreground">
                  <DiffIcon class="size-3" />
                  {{ t('issue.diffStats') }}
                </p>
                <p>{{ t('issue.filesChanged', { n: diffStats.filesChanged }) }}</p>
                <p class="font-mono">
                  <span class="text-emerald-600 dark:text-emerald-400">+{{ diffStats.added }}</span>
                  <span class="ml-2 text-red-600 dark:text-red-400">−{{ diffStats.deleted }}</span>
                </p>
              </div>
            </CardContent>
          </Card>

          <Card v-if="parent" class="gap-0 py-0">
            <CardContent class="space-y-2 p-4 text-sm">
              <p class="flex items-center gap-1 text-xs text-muted-foreground">
                <CornerDownRight class="size-3" />
                {{ t('issue.parent') }}
              </p>
              <NuxtLink
                :to="`/${owner}/${name}/issues/${parent.number}`"
                class="block truncate text-sm font-medium hover:underline"
              >
                #{{ parent.number }} {{ parent.title }}
              </NuxtLink>
              <Badge :class="stateBadgeClass(parent.state)" variant="secondary">
                {{ t(`issue.state.${parent.state}`) }}
              </Badge>
            </CardContent>
          </Card>

          <Card class="gap-0 py-0">
            <CardContent class="space-y-3 p-4">
              <div class="flex items-center justify-between gap-2">
                <p class="text-xs text-muted-foreground">{{ t('issue.subIssues') }}</p>
                <Button
                  v-if="issue.state === 'open'"
                  size="sm"
                  variant="ghost"
                  class="h-7 gap-1 px-2 text-xs"
                  as-child
                >
                  <NuxtLink :to="`/${owner}/${name}/issues/new?parent=${issue.number}`">
                    <Plus class="size-3" />
                    {{ t('issue.newSubIssue') }}
                  </NuxtLink>
                </Button>
              </div>
              <p v-if="children.length === 0" class="text-xs text-muted-foreground">
                {{ t('issue.noSubIssues') }}
              </p>
              <ul v-else class="space-y-1.5">
                <li v-for="kid in children" :key="kid.id" class="flex items-center gap-2 text-sm">
                  <component :is="stateBadgeIcon(kid.state)" class="size-3 shrink-0 text-muted-foreground" />
                  <NuxtLink
                    :to="`/${owner}/${name}/issues/${kid.number}`"
                    class="min-w-0 flex-1 truncate hover:underline"
                  >
                    <span class="text-muted-foreground">#{{ kid.number }}</span>
                    {{ kid.title }}
                  </NuxtLink>
                </li>
              </ul>
            </CardContent>
          </Card>

          <Card v-if="!issue.head_sha && issue.state === 'open'" class="gap-0 py-0">
            <CardContent class="space-y-2 p-4">
              <p class="text-xs text-muted-foreground">{{ t('issue.pushHint') }}</p>
              <pre class="overflow-x-auto rounded bg-muted/40 p-2 font-mono text-xs leading-relaxed"><code>{{ pushSnippet }}</code></pre>
            </CardContent>
          </Card>

          <div v-if="actionError" class="text-sm text-destructive">{{ actionError }}</div>
          <div v-if="actionInfo" class="text-sm text-emerald-700 dark:text-emerald-400">{{ actionInfo }}</div>

          <div class="space-y-2">
            <Button
              v-if="canMerge"
              class="w-full"
              :disabled="mergeBusy"
              @click="merge"
            >
              <GitMerge class="size-4" />
              {{ mergeBusy ? t('issue.merging') : t('issue.merge') }}
            </Button>

            <Button
              v-if="issue.state !== 'merged' && (issue.author_id === user?.id || canManage)"
              variant="outline"
              class="w-full"
              :disabled="stateBusy"
              @click="toggleState"
            >
              {{ issue.state === 'open' ? t('issue.close') : t('issue.reopen') }}
            </Button>
          </div>
        </aside>
      </div>
    </template>
  </div>
</template>
