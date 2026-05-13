<script setup lang="ts">
import { computed } from 'vue'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import type { FileDiff } from '~/types/repo'

const props = defineProps<{ diffs: FileDiff[] }>()
const { t } = useI18n()

const diffList = computed(() => props.diffs ?? [])

function statusVariant(s: FileDiff['status']) {
  switch (s) {
    case 'added': return 'secondary' as const
    case 'deleted': return 'destructive' as const
    case 'renamed': return 'outline' as const
    default: return 'outline' as const
  }
}

function patchLines(patch: string) {
  return patch.split('\n').map((line) => {
    let cls = ''
    if (line.startsWith('@@')) cls = 'text-muted-foreground'
    else if (line.startsWith('+++') || line.startsWith('---')) cls = 'text-muted-foreground'
    else if (line.startsWith('+')) cls = 'text-emerald-500'
    else if (line.startsWith('-')) cls = 'text-destructive'
    return { text: line, cls }
  })
}
</script>

<template>
  <Card v-for="(d, i) in diffList" :key="`${d.new_path}-${i}`" class="gap-0 py-0">
    <CardHeader class="rounded-t-xl border-b bg-muted/40 px-4 py-2">
      <div class="flex flex-wrap items-center gap-2">
        <Badge :variant="statusVariant(d.status)">{{ d.status }}</Badge>
        <code class="break-all font-mono text-sm">
          <template v-if="d.status === 'renamed'">
            {{ d.old_path }} → {{ d.new_path }}
          </template>
          <template v-else>
            {{ d.new_path || d.old_path }}
          </template>
        </code>
      </div>
    </CardHeader>
    <CardContent class="p-0">
      <div v-if="d.binary" class="p-3 text-sm text-muted-foreground">
        {{ t('repo.commit.binaryDiff') }}
      </div>
      <pre v-else class="overflow-x-auto whitespace-pre p-3 font-mono text-xs leading-5"><span
        v-for="(ln, j) in patchLines(d.patch)"
        :key="j"
        :class="ln.cls"
      >{{ ln.text }}
</span></pre>
    </CardContent>
  </Card>
</template>
