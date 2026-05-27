<script setup lang="ts">
import type { Questionnaire, AnswerEntry, QuestionnaireResult, MySubmission } from '~/types/questionnaire'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Bot, BarChart3, Users } from 'lucide-vue-next'
import ActorBadge from '@/components/ActorBadge.vue'
import MarkdownBody from '@/components/MarkdownBody.vue'
import QuestionnaireQuestion from './QuestionnaireQuestion.vue'
import QuestionnaireResults from './QuestionnaireResults.vue'
import type { ActorRef } from '~/types/actor'
import { relativeTime } from '~/utils/time'

const { t } = useI18n()
const { user } = useCurrentUser()

const props = defineProps<{
  questionnaire: Questionnaire
  owner: string
  name: string
  issueNumber: number
}>()

const emit = defineEmits<{
  submitted: []
  closed: []
}>()

const { submit, loadResults, close: closeApi, error } = useQuestionnaire(
  () => props.owner,
  () => props.name,
  () => props.issueNumber,
)

const draft = reactive<Record<number, AnswerEntry>>({})
const submitting = ref(false)
const submitError = ref<string | null>(null)
const view = ref<'summary' | 'details'>('summary')
const result = ref<QuestionnaireResult | null>(null)
const resultLoading = ref(false)

// Build an ActorRef for the issuing agent so ActorBadge renders it consistently.
const agentActor = computed<ActorRef>(() => ({
  kind: 'agent',
  id: `agent:${props.questionnaire.created_by_agent}`,
  display_name: `@agent-${props.questionnaire.created_by_agent}`,
  role_key: props.questionnaire.created_by_agent,
}))

function rel(s?: string | null) { return relativeTime(s ?? null, t) }
function formatDate(s?: string | null) {
  if (!s) return ''
  try { return new Date(s).toLocaleString() } catch { return s }
}

// Initialise draft answers from my_submission or empty
watch(
  () => props.questionnaire,
  (q) => {
    if (q.my_submission) {
      for (const a of q.my_submission.answers) {
        draft[a.question_id] = { ...a }
      }
    } else {
      for (const qq of q.questions) {
        if (!draft[qq.id]) {
          draft[qq.id] = {
            question_id: qq.id,
            option_ids: qq.type === 'single_choice' || qq.type === 'multi_choice' ? [] : undefined,
            text: qq.type === 'text_input' ? '' : undefined,
          }
        }
      }
    }
  },
  { immediate: true },
)

const mySubmission = computed<MySubmission | null | undefined>(
  () => props.questionnaire.my_submission,
)

const isOpen = computed(() => props.questionnaire.status === 'open')
const hasSubmitted = computed(() => !!mySubmission.value)
const isLoggedIn = computed(() => !!user.value)

async function handleSubmit() {
  submitError.value = null
  submitting.value = true
  try {
    const answers: AnswerEntry[] = props.questionnaire.questions.map((q) => ({
      question_id: q.id,
      option_ids: draft[q.id]?.option_ids?.length ? draft[q.id]!.option_ids : undefined,
      text: draft[q.id]?.text || undefined,
    }))
    const ok = await submit(props.questionnaire.id, { answers })
    if (ok) {
      emit('submitted')
    } else {
      submitError.value = error.value
    }
  } finally {
    submitting.value = false
  }
}

async function loadResultsIfClosed() {
  if (!result.value && props.questionnaire.status === 'closed') {
    resultLoading.value = true
    result.value = await loadResults(props.questionnaire.id)
    resultLoading.value = false
  }
}

onMounted(() => { if (!isOpen.value) loadResultsIfClosed() })
watch(() => props.questionnaire.status, (s) => { if (s === 'closed') loadResultsIfClosed() })

function answerForQuestion(qid: number): AnswerEntry | undefined {
  return draft[qid] ?? mySubmission.value?.answers.find((a) => a.question_id === qid)
}

function optionLabel(qid: number, oid: string): string {
  const q = props.questionnaire.questions.find((qq) => qq.id === qid)
  return q?.options?.find((o) => o.id === oid)?.label ?? oid
}
</script>

<template>
  <Card class="gap-0 py-0">
    <CardContent class="p-0">
      <!-- Header strip: same chrome as comments — ActorBadge + verb + timestamp -->
      <div class="flex items-center gap-2 border-b bg-muted/40 px-3 py-2 text-xs">
        <ActorBadge :actor="agentActor" size="sm" />
        <span class="text-muted-foreground">{{ t('issue.questionnaire.asked') }}</span>
        <span class="text-muted-foreground" :title="formatDate(questionnaire.created_at)">
          {{ rel(questionnaire.created_at) }}
        </span>
        <Badge
          :class="
            isOpen
              ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
              : 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
          "
          variant="secondary"
          class="ml-auto"
        >
          {{ isOpen ? t('issue.state.open') : t('issue.questionnaire.closed') }}
        </Badge>
      </div>

      <!-- Body -->
      <div class="space-y-3 px-4 py-3 text-sm">
        <!-- Title -->
        <p class="font-medium">{{ questionnaire.title }}</p>

        <!-- Description (markdown) -->
        <MarkdownBody v-if="questionnaire.description" :source="questionnaire.description" />

        <!-- --- STATUS BRANCHING --- -->

        <!-- Open: fill form -->
        <template v-if="isOpen && !hasSubmitted">
          <div class="space-y-3">
            <QuestionnaireQuestion
              v-for="q in questionnaire.questions"
              :key="q.id"
              :question="q"
              :model-value="answerForQuestion(q.id)!"
              @update:model-value="(v: AnswerEntry) => (draft[q.id] = v)"
            />
          </div>

          <div class="space-y-2">
            <Button
              v-if="isLoggedIn"
              :disabled="submitting"
              @click="handleSubmit"
            >
              {{ submitting ? t('issue.questionnaire.submitting') : t('issue.questionnaire.submit') }}
            </Button>
            <p v-else class="text-xs text-muted-foreground">
              {{ t('issue.questionnaire.loginToSubmit') }}
            </p>
            <p v-if="submitError" class="text-xs text-destructive">{{ submitError }}</p>
          </div>
        </template>

        <!-- Open: already submitted -->
        <template v-else-if="isOpen && hasSubmitted">
          <p class="text-xs font-medium text-emerald-600 dark:text-emerald-400">
            {{ t('issue.questionnaire.submitted') }}
          </p>

          <div class="space-y-2">
            <div
              v-for="q in questionnaire.questions"
              :key="q.id"
              class="text-xs"
            >
              <p class="text-muted-foreground">{{ q.text }}</p>
              <p class="font-medium">
                <template v-if="mySubmission">
                  <template
                    v-if="
                      mySubmission.answers.find((a) => a.question_id === q.id)
                        ?.option_ids?.length
                    "
                  >
                    {{
                      mySubmission
                        .answers.find((a) => a.question_id === q.id)!
                        .option_ids!.map((oid) => optionLabel(q.id, oid))
                        .join(', ')
                    }}
                  </template>
                  <template v-else>
                    {{
                      mySubmission.answers.find((a) => a.question_id === q.id)
                        ?.text || '—'
                    }}
                  </template>
                </template>
              </p>
            </div>
          </div>
        </template>

        <!-- Closed -->
        <template v-else>
          <p class="text-xs text-muted-foreground">
            {{ t('issue.questionnaire.submissions', { n: result?.submissions ?? '?' }) }}
          </p>

          <div v-if="resultLoading" class="space-y-2">
            <Skeleton class="h-3 w-full" />
            <Skeleton class="h-3 w-2/3" />
          </div>

          <template v-else-if="result">
            <!-- View toggle -->
            <div class="flex gap-1">
              <Button
                size="sm"
                :variant="view === 'summary' ? 'default' : 'outline'"
                class="h-7 text-xs"
                @click="view = 'summary'"
              >
                <BarChart3 class="size-3 mr-1" />
                {{ t('issue.questionnaire.summary') }}
              </Button>
              <Button
                size="sm"
                :variant="view === 'details' ? 'default' : 'outline'"
                class="h-7 text-xs"
                @click="view = 'details'"
              >
                <Users class="size-3 mr-1" />
                {{ t('issue.questionnaire.details') }}
              </Button>
            </div>

            <QuestionnaireResults :result="result" :view="view" />
          </template>

          <p v-else class="text-xs text-muted-foreground">
            {{ t('issue.questionnaire.closedNoResults') }}
          </p>
        </template>
      </div>
    </CardContent>
  </Card>
</template>
