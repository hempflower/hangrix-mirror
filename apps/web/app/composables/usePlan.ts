import { onMounted, onUnmounted, reactive, ref, type Ref } from 'vue'
import type { PlanResp } from '~/types/issue'

interface PlanState {
  data: Ref<PlanResp | null>
  isLoading: Ref<boolean>
  error: Ref<string | null>
  collapsed: Set<number>
  hideDone: Ref<boolean>
  refresh: () => void
}

const POLL_INTERVAL_MS = 5_000

export function usePlan(
  owner: () => string,
  name: () => string,
  number: () => number,
): PlanState {
  const data = ref<PlanResp | null>(null)
  const isLoading = ref(true)
  const error = ref<string | null>(null)
  const collapsed = reactive(new Set<number>())
  const hideDone = ref(false)

  let timer: ReturnType<typeof setInterval> | null = null

  async function fetch() {
    try {
      const res = await $fetch<PlanResp>(
        `/api/repos/${owner()}/${name()}/issues/${number()}/plan`,
        { credentials: 'include' },
      )
      data.value = res
      error.value = null
    } catch (e: any) {
      const { t } = useI18n()
      error.value = e?.data?.error ?? t('issue.plan.error.fallback')
      // Keep old data — do not clear
    } finally {
      isLoading.value = false
    }
  }

  function refresh() {
    fetch()
  }

  function startPoll() {
    if (timer || typeof window === 'undefined') return
    timer = setInterval(() => {
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
      fetch()
    }, POLL_INTERVAL_MS)
  }

  function stopPoll() {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  }

  function onVisibilityChange() {
    if (typeof document !== 'undefined' && document.visibilityState === 'visible') {
      fetch()
    }
  }

  onMounted(() => {
    fetch()
    startPoll()
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', onVisibilityChange)
    }
  })

  onUnmounted(() => {
    stopPoll()
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  })

  return {
    data,
    isLoading,
    error,
    collapsed,
    hideDone,
    refresh,
  }
}
