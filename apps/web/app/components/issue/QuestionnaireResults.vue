<script setup lang="ts">
import type { QuestionnaireResult, Question, ChoiceTally, TextResponse, Submitter } from '~/types/questionnaire'
import { Badge } from '@/components/ui/badge'

const { t } = useI18n()

const props = defineProps<{
  result: QuestionnaireResult
  view: 'summary' | 'details'
}>()

const questions = computed<Question[]>(() => props.result.questionnaire.questions ?? [])

function questionTally(qid: number): ChoiceTally[] | undefined {
  return props.result.by_question[String(qid)]?.tallies
}

function questionTexts(qid: number): TextResponse[] | undefined {
  return props.result.by_question[String(qid)]?.responses
}

function maxCount(tallies: ChoiceTally[]): number {
  return tallies.reduce((m, t) => Math.max(m, t.count), 0)
}
</script>

<template>
  <div class="space-y-4">
    <!-- Summary view: choice → progress bars, text → list -->
    <template v-if="view === 'summary'">
      <div
        v-for="q in questions"
        :key="q.id"
        class="space-y-1.5"
      >
        <p class="text-xs font-medium text-foreground">{{ q.text }}</p>

        <!-- Choice tallies -->
        <template v-if="q.type === 'single_choice' || q.type === 'multi_choice'">
          <div
            v-if="questionTally(q.id)?.length"
            class="space-y-1"
          >
            <div
              v-for="tally in questionTally(q.id)!"
              :key="tally.option_id"
              class="flex items-center gap-2 text-xs"
            >
              <span class="w-24 shrink-0 truncate text-muted-foreground">
                {{ tally.label }}
              </span>
              <div class="h-2 flex-1 rounded bg-muted">
                <div
                  class="h-full rounded bg-primary transition-all"
                  :style="{ width: `${tally.percent}%` }"
                />
              </div>
              <span class="w-16 shrink-0 text-right tabular-nums text-muted-foreground">
                {{ tally.count }} ({{ Math.round(tally.percent) }}%)
              </span>
            </div>
          </div>
          <p v-else class="text-xs text-muted-foreground">
            {{ t('issue.questionnaire.noResponses') }}
          </p>
        </template>

        <!-- Text responses (summary: just count) -->
        <template v-else>
          <p class="text-xs text-muted-foreground">
            {{ t('issue.questionnaire.textResponses', { n: questionTexts(q.id)?.length ?? 0 }) }}
          </p>
        </template>
      </div>
    </template>

    <!-- Details view: per-submitter answers -->
    <template v-else>
      <div
        v-for="sub in result.submitters"
        :key="sub.user_id"
        class="rounded border p-3 space-y-2"
      >
        <div class="flex items-center gap-2 text-xs">
          <span class="font-medium text-foreground">{{ sub.user_display }}</span>
          <span class="text-muted-foreground">· {{ new Date(sub.submitted_at).toLocaleString() }}</span>
        </div>

        <div
          v-for="a in sub.answers"
          :key="a.question_id"
          class="text-xs"
        >
          <p class="text-muted-foreground">
            {{ questions.find((q) => q.id === a.question_id)?.text ?? `Q#${a.question_id}` }}
          </p>
          <p class="text-foreground">
            <template v-if="a.option_ids?.length">
              {{ a.option_ids.map((oid) => {
                const q = questions.find((qq) => qq.id === a.question_id)
                return q?.options?.find((o) => o.id === oid)?.label ?? oid
              }).join(', ') }}
            </template>
            <template v-else-if="a.text">
              {{ a.text }}
            </template>
            <template v-else>
              <span class="text-muted-foreground">—</span>
            </template>
          </p>
        </div>
      </div>
    </template>
  </div>
</template>
