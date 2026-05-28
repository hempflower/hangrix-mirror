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
  const stateKey = `q-list-${issueNumber()}`
  const questionnaires = useState<Questionnaire[]>(stateKey, () => [])
  const loading = ref(false)
  const error = ref<string | null>(null)
  const locked = ref(false)

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
    locked.value = false
    try {
      const res = await $fetch<SubmitAnswersResponse>(
        `${apiBase.value}/${id}/answers`,
        {
          method: 'POST',
          credentials: 'include',
          body: answers,
        },
      )
      // Update local questionnaire status from the response immediately,
      // then re-fetch so my_submission is populated.
      const q = questionnaires.value.find((q) => q.id === id)
      if (q) {
        q.status = res.data.questionnaire_status
      }
      await loadOne(id)
      return true
    } catch (e: any) {
      const body = e?.data
      if (body?.error?.code === 'questionnaire_locked') {
        error.value = body.error.message
        locked.value = true
        // Re-fetch so the status flips to closed
        await loadOne(id)
        return false
      }
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


  /** @deprecated The timeline card no longer renders aggregate results (see issue #236).
   *  Kept for potential non-UI consumers (agent scripts, exports). */
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
    locked,
    load,
    loadOne,
    submit,
    loadResults,
    close,
  }
}
