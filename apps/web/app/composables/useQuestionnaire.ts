import type {
  Questionnaire,
  QuestionnaireListResponse,
  QuestionnaireSingleResponse,
  SubmitAnswersRequest,
  SubmitAnswersResponse,
  QuestionnaireResultResponse,
  QuestionnaireResult,
} from '~/types/questionnaire'

export function useQuestionnaire(
  owner: () => string,
  name: () => string,
  issueNumber: () => number,
) {
  // Use ref (not useState with a shared key) so that each issue's
  // questionnaire list stays independent — navigating between issues
  // won't flash stale questionnaires from the previous page.
  const questionnaires = ref<Questionnaire[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  const apiBase = computed(() =>
    `/api/repos/${owner()}/${name()}/issues/${issueNumber()}/questionnaires`,
  )

  async function load() {
    loading.value = true
    error.value = null
    try {
      const res = await $fetch<QuestionnaireListResponse>(apiBase.value, {
        credentials: 'include',
      })
      questionnaires.value = res.data ?? []
    } catch (e: any) {
      error.value = e?.data?.message ?? 'Load failed'
      questionnaires.value = []
    } finally {
      loading.value = false
    }
  }

  async function loadOne(id: number): Promise<Questionnaire | null> {
    error.value = null
    try {
      const res = await $fetch<QuestionnaireSingleResponse>(
        `${apiBase.value}/${id}`,
        { credentials: 'include' },
      )
      // Update the item in the local list
      const idx = questionnaires.value.findIndex((q) => q.id === id)
      const q = res.data
      if (idx >= 0) questionnaires.value[idx] = q
      return q
    } catch (e: any) {
      error.value = e?.data?.message ?? 'Load failed'
      return null
    }
  }

  async function submit(
    id: number,
    answers: SubmitAnswersRequest,
  ): Promise<boolean> {
    error.value = null
    try {
      const res = await $fetch<SubmitAnswersResponse>(
        `${apiBase.value}/${id}/answers`,
        {
          method: 'POST',
          credentials: 'include',
          body: answers,
        },
      )
      // Refresh the questionnaire so my_submission is populated
      await loadOne(id)
      return true
    } catch (e: any) {
      const body = e?.data
      if (body?.errors?.length) {
        error.value = body.errors
          .map((err: any) => err.message ?? err.code)
          .join('; ')
      } else {
        error.value = body?.message ?? 'Submit failed'
      }
      return false
    }
  }

  async function loadResults(
    id: number,
  ): Promise<QuestionnaireResult | null> {
    error.value = null
    try {
      const res = await $fetch<QuestionnaireResultResponse>(
        `${apiBase.value}/${id}/results`,
        { credentials: 'include' },
      )
      return res.data
    } catch (e: any) {
      error.value = e?.data?.message ?? 'Load results failed'
      return null
    }
  }

  async function close(id: number, reason?: string): Promise<boolean> {
    error.value = null
    try {
      await $fetch(`${apiBase.value}/${id}/close`, {
        method: 'POST',
        credentials: 'include',
        body: { reason: reason ?? '' },
      })
      await load()
      return true
    } catch (e: any) {
      error.value = e?.data?.message ?? 'Close failed'
      return false
    }
  }

  return {
    questionnaires,
    loading,
    error,
    load,
    loadOne,
    submit,
    loadResults,
    close,
  }
}
