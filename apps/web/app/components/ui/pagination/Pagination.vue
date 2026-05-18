<script setup lang="ts">
import { computed } from 'vue'
import { ChevronLeft, ChevronRight } from 'lucide-vue-next'

import { Button } from '@/components/ui/button'

interface Props {
  total: number
  offset: number
  limit: number
}

const props = defineProps<Props>()
const emit = defineEmits<{
  (e: 'update:offset', value: number): void
}>()

const { t } = useI18n()

const pageCount = computed(() => Math.max(1, Math.ceil(props.total / Math.max(1, props.limit))))
const currentPage = computed(() => Math.floor(props.offset / Math.max(1, props.limit)) + 1)

const from = computed(() => (props.total === 0 ? 0 : props.offset + 1))
const to = computed(() => Math.min(props.total, props.offset + props.limit))

const canPrev = computed(() => props.offset > 0)
const canNext = computed(() => props.offset + props.limit < props.total)

function prev() {
  if (!canPrev.value) return
  emit('update:offset', Math.max(0, props.offset - props.limit))
}
function next() {
  if (!canNext.value) return
  emit('update:offset', props.offset + props.limit)
}
</script>

<template>
  <div class="flex items-center justify-between gap-3 text-sm">
    <span class="text-muted-foreground tabular-nums">
      {{ t('common.pagination.summary', { from, to, total }) }}
    </span>
    <div class="flex items-center gap-2">
      <span class="text-xs text-muted-foreground tabular-nums">
        {{ t('common.pagination.page', { page: currentPage, total: pageCount }) }}
      </span>
      <Button size="sm" variant="outline" :disabled="!canPrev" @click="prev">
        <ChevronLeft class="size-4" />
        {{ t('common.pagination.prev') }}
      </Button>
      <Button size="sm" variant="outline" :disabled="!canNext" @click="next">
        {{ t('common.pagination.next') }}
        <ChevronRight class="size-4" />
      </Button>
    </div>
  </div>
</template>
