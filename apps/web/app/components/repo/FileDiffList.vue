<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronDown, ChevronRight, FileCode, FileDiff as FileDiffIcon } from 'lucide-vue-next'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import type { FileDiff } from '~/types/repo'

// `refBefore` / `refAfter` enable the "view file" link icons on each file
// header. They're optional: when omitted (e.g. on a commit's diff against
// an empty parent tree we have nothing to point "before" at), the affected
// link is hidden. `owner` / `name` are required only when either ref is
// supplied — they're consumed to build the /<owner>/<name>/blob/... URL.
const props = defineProps<{
  diffs: FileDiff[]
  owner?: string
  name?: string
  refBefore?: string
  refAfter?: string
}>()
const { t } = useI18n()

const diffList = computed(() => props.diffs ?? [])

// `collapsed[i] === true` keeps the i-th file folded. Defaults to expanded;
// the file header acts as the toggle, matching GitHub's "click the path to
// hide the patch" affordance.
const collapsed = ref<Record<number, boolean>>({})
function toggle(i: number) {
  collapsed.value[i] = !collapsed.value[i]
}

function statusVariant(s: FileDiff['status']) {
  switch (s) {
    case 'added': return 'secondary' as const
    case 'deleted': return 'destructive' as const
    case 'renamed': return 'outline' as const
    default: return 'outline' as const
  }
}

// A single rendered diff row. `marker` is what we draw in the third gutter
// column (the +/-/space glyph). `oldNo` / `newNo` are blank strings when the
// row only exists on one side, matching how GitHub leaves the corresponding
// gutter empty.
type RowKind = 'hunk' | 'add' | 'del' | 'ctx' | 'note'
interface DiffRow {
  kind: RowKind
  oldNo: string
  newNo: string
  marker: string
  content: string
}

// parsePatch consumes a unified-diff blob (the per-file output go-git's
// UnifiedEncoder emits) and yields one DiffRow per line we want to render.
// It throws away the file header lines (`--- a/file` / `+++ b/file`) since
// the file path is already in the card header; rolling them up here is
// purely cosmetic.
//
// @@ headers carry both line-number anchors and the surrounding "section
// header" (typically the enclosing function name). We capture both so the
// hunk row can show GitHub's familiar "@@ -3,7 +3,14 @@ funcName" line.
const HUNK_RE = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/
function parsePatch(patch: string): DiffRow[] {
  const out: DiffRow[] = []
  let oldNo = 0
  let newNo = 0
  for (const raw of patch.split('\n')) {
    if (raw.startsWith('+++') || raw.startsWith('---')) continue
    const m = raw.match(HUNK_RE)
    if (m) {
      oldNo = Number(m[1])
      newNo = Number(m[2])
      out.push({
        kind: 'hunk',
        oldNo: '',
        newNo: '',
        marker: '',
        content: raw + (m[3] ? '' : ''),
      })
      continue
    }
    // \ No newline at end of file — annotate but don't advance counters.
    if (raw.startsWith('\\')) {
      out.push({ kind: 'note', oldNo: '', newNo: '', marker: '', content: raw })
      continue
    }
    const first = raw.charAt(0)
    const body = raw.slice(1)
    if (first === '+') {
      out.push({ kind: 'add', oldNo: '', newNo: String(newNo), marker: '+', content: body })
      newNo++
    } else if (first === '-') {
      out.push({ kind: 'del', oldNo: String(oldNo), newNo: '', marker: '-', content: body })
      oldNo++
    } else {
      // Context (space-prefixed) or empty line at the tail of a hunk.
      out.push({ kind: 'ctx', oldNo: String(oldNo), newNo: String(newNo), marker: ' ', content: body })
      oldNo++
      newNo++
    }
  }
  return out
}

// fileStats walks the rows once and counts add/del, ignoring hunk headers
// and notes. The numbers drive the small green/red "+N -M" badge on the
// file header.
function fileStats(rows: DiffRow[]): { added: number, deleted: number } {
  let added = 0
  let deleted = 0
  for (const r of rows) {
    if (r.kind === 'add') added++
    else if (r.kind === 'del') deleted++
  }
  return { added, deleted }
}

// Memoize rows + stats per file so toggling collapse / reactive updates
// don't re-parse. Computed re-runs only when the diff list identity changes.
const parsed = computed(() => diffList.value.map((d) => {
  const rows = d.binary ? [] : parsePatch(d.patch)
  return { rows, stats: fileStats(rows) }
}))

// Build a /<owner>/<name>/blob/<ref>/<path> URL. Ref is encoded whole (so
// `feature/x` round-trips through Vue Router's param parser); each path
// segment is encoded individually so `/` between segments stays literal.
// Returns '' when any required piece is missing — caller uses that to hide
// the link entirely.
function blobHref(ref: string | undefined, path: string): string {
  if (!ref || !path || !props.owner || !props.name) return ''
  const encRef = encodeURIComponent(ref)
  const encPath = path.split('/').map(encodeURIComponent).join('/')
  return `/${props.owner}/${props.name}/blob/${encRef}/${encPath}`
}

// "Before" link points at the file as it was at refBefore. Suppressed when
// the file was newly added (no prior version exists).
function beforeHref(d: FileDiff): string {
  if (d.status === 'added') return ''
  return blobHref(props.refBefore, d.old_path || d.new_path)
}

// "After" link points at the file as it stands at refAfter. Suppressed
// when the file was deleted (no post version).
function afterHref(d: FileDiff): string {
  if (d.status === 'deleted') return ''
  return blobHref(props.refAfter, d.new_path || d.old_path)
}
</script>

<template>
  <Card
    v-for="(d, i) in diffList"
    :key="`${d.new_path || d.old_path}-${i}`"
    class="gap-0 py-0"
  >
    <!-- File header: the path region (chevron + badge + path) is the
         collapse toggle. The right edge holds independent NuxtLinks to the
         "before" / "after" blobs and the +/-/ stats badge. Keeping these
         pieces as separate interactive elements avoids nesting <a>s inside
         a <button>, which screen readers and browsers both dislike. -->
    <div class="flex w-full flex-wrap items-center gap-2 rounded-t-xl border-b bg-muted/40 px-3 py-2">
      <button
        type="button"
        class="flex min-w-0 flex-1 cursor-pointer items-center gap-2 text-left"
        @click="toggle(i)"
      >
        <component
          :is="collapsed[i] ? ChevronRight : ChevronDown"
          class="size-4 shrink-0 text-muted-foreground"
        />
        <Badge :variant="statusVariant(d.status)" class="capitalize">
          {{ d.status }}
        </Badge>
        <code class="min-w-0 flex-1 truncate font-mono text-sm">
          <template v-if="d.status === 'renamed'">
            {{ d.old_path }} → {{ d.new_path }}
          </template>
          <template v-else>
            {{ d.new_path || d.old_path }}
          </template>
        </code>
      </button>

      <div class="flex shrink-0 items-center gap-1">
        <NuxtLink
          v-if="beforeHref(d)"
          :to="beforeHref(d)"
          :title="t('repo.commit.viewBefore')"
          class="inline-flex size-7 items-center justify-center rounded text-muted-foreground hover:bg-background hover:text-foreground"
          @click.stop
        >
          <FileCode class="size-4" />
        </NuxtLink>
        <NuxtLink
          v-if="afterHref(d)"
          :to="afterHref(d)"
          :title="t('repo.commit.viewAfter')"
          class="inline-flex size-7 items-center justify-center rounded text-muted-foreground hover:bg-background hover:text-foreground"
          @click.stop
        >
          <FileDiffIcon class="size-4" />
        </NuxtLink>
        <span
          v-if="!d.binary && ((parsed[i]?.stats.added ?? 0) || (parsed[i]?.stats.deleted ?? 0))"
          class="ml-1 font-mono text-xs"
        >
          <span class="text-emerald-600 dark:text-emerald-400">+{{ parsed[i]?.stats.added ?? 0 }}</span>
          <span class="ml-1 text-red-600 dark:text-red-400">−{{ parsed[i]?.stats.deleted ?? 0 }}</span>
        </span>
      </div>
    </div>

    <CardContent v-show="!collapsed[i]" class="p-0">
      <!-- Binary files have no patch text; surface that explicitly rather
           than rendering an empty pane. -->
      <div v-if="d.binary" class="p-3 text-sm text-muted-foreground">
        {{ t('repo.commit.binaryDiff') }}
      </div>

      <!-- Whole patch scrolls horizontally as one unit so the line-number
           gutter slides in lock-step with the content. Rows use display:
           table so the gutter stays at a fixed width regardless of line
           contents. -->
      <div v-else class="overflow-x-auto">
        <table v-if="parsed[i]" class="w-full border-collapse font-mono text-xs leading-5">
          <tbody>
            <tr
              v-for="(r, j) in parsed[i].rows"
              :key="j"
              :class="rowClass(r)"
            >
              <!-- Hunk header spans all four columns. We strip the leading
                   "@@ ... @@" delimiters off the trailing section-header
                   text so it reads naturally in the gray strip. -->
              <td
                v-if="r.kind === 'hunk'"
                colspan="4"
                class="select-none whitespace-pre bg-sky-500/10 px-3 py-0.5 text-sky-700 dark:text-sky-300"
              >{{ r.content }}</td>
              <template v-else-if="r.kind === 'note'">
                <td colspan="4" class="select-none px-3 py-0.5 text-muted-foreground italic">
                  {{ r.content }}
                </td>
              </template>
              <template v-else>
                <td :class="gutterClass(r)">{{ r.oldNo }}</td>
                <td :class="gutterClass(r)">{{ r.newNo }}</td>
                <td :class="markerClass(r)">{{ r.marker }}</td>
                <td class="whitespace-pre pl-2 pr-3">{{ r.content }}</td>
              </template>
            </tr>
          </tbody>
        </table>
      </div>
    </CardContent>
  </Card>
</template>

<script lang="ts">
// rowClass / gutterClass / markerClass are pulled out of <script setup> so
// they can be referenced in the template without being exposed as
// component-level reactives — they're plain string mappers.
function rowClass(r: { kind: string }) {
  switch (r.kind) {
    case 'add': return 'bg-emerald-500/10'
    case 'del': return 'bg-red-500/10'
    default: return ''
  }
}
function gutterClass(r: { kind: string }) {
  // Numeric gutter columns: fixed width, right-aligned, subtle tint that
  // matches the row body so the eye reads them as one band.
  const base = 'select-none px-2 py-0 text-right text-muted-foreground w-12 min-w-[3rem]'
  if (r.kind === 'add') return `${base} bg-emerald-500/15 text-emerald-700 dark:text-emerald-300`
  if (r.kind === 'del') return `${base} bg-red-500/15 text-red-700 dark:text-red-300`
  return base
}
function markerClass(r: { kind: string }) {
  const base = 'select-none w-4 text-center'
  if (r.kind === 'add') return `${base} text-emerald-600 dark:text-emerald-400`
  if (r.kind === 'del') return `${base} text-red-600 dark:text-red-400`
  return `${base} text-muted-foreground`
}
</script>
