<script setup lang="ts">
import { ref, onMounted, watch, nextTick } from 'vue'
import MarkdownBody from '@/components/MarkdownBody.vue'

const props = withDefaults(defineProps<{
  source: string
  /** Rendering height threshold (px). Content shorter than this isn't foldable. */
  maxHeight?: number
}>(), {
  maxHeight: 280,
})

const { t } = useI18n()

const containerRef = ref<HTMLElement | null>(null)
const needsFold = ref(false)
const expanded = ref(false)

async function measure() {
  await nextTick()
  if (containerRef.value) {
    needsFold.value = containerRef.value.scrollHeight > props.maxHeight
  }
}

onMounted(measure)

watch(() => props.source, () => {
  expanded.value = false
  measure()
})
</script>

<template>
  <div v-if="source">
    <div
      ref="containerRef"
      :style="needsFold && !expanded ? { maxHeight: `${maxHeight}px`, overflow: 'hidden' } : {}"
      class="relative"
    >
      <MarkdownBody :source="source" />
      <!-- Fade overlay at the bottom when content is clamped -->
      <div
        v-if="needsFold && !expanded"
        class="pointer-events-none absolute bottom-0 left-0 right-0 h-10 bg-gradient-to-t from-card to-transparent"
      />
    </div>
    <button
      v-if="needsFold"
      class="mt-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
      @click="expanded = !expanded"
    >
      {{ expanded ? t('issue.collapse') : t('issue.expand') }}
    </button>
  </div>
</template>
