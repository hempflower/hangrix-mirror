<script setup lang="ts">
import { computed, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'
import { Rocket } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import type { Release, ReleaseCreateReq } from '~/types/release'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.tabs2.releases'), to: `${base}/releases` },
    { label: t('release.newTitle') },
  ]
})

const { refs, load: loadRefs } = useRepoRefs(() => owner.value, () => name.value)

onMounted(() => { loadRefs() })

const tags = computed(() => refs.value?.tags ?? [])

const schema = computed(() => toTypedSchema(z.object({
  tag_name: z.string().min(1, t('release.fields.tagRequired')),
  title: z.string().optional(),
  notes: z.string().optional(),
})))

const initial = computed(() => ({
  tag_name: '',
  title: '',
  notes: '',
}))

const createError = ref<string | null>(null)

async function onCreate(values: any, ctx: any) {
  createError.value = null
  const body: ReleaseCreateReq = {
    tag_name: values.tag_name,
  }
  if (values.title?.trim()) body.title = values.title.trim()
  if (values.notes?.trim()) body.notes = values.notes.trim()

  try {
    const rel = await $fetch<Release>(`/api/repos/${owner.value}/${name.value}/releases`, {
      method: 'POST',
      credentials: 'include',
      body,
    })
    router.push(`/${owner.value}/${name.value}/releases/${rel.id}`)
  } catch (e: any) {
    createError.value = e?.data?.error ?? t('release.createFailed')
  }
}
</script>

<template>
  <div class="mx-auto max-w-2xl space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ t('release.newTitle') }}
      </h1>
      <p class="text-sm text-muted-foreground">
        {{ t('release.newSubtitle') }}
      </p>
    </header>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('release.newTitle') }}</CardTitle>
      </CardHeader>
      <CardContent>
        <Form
          v-slot="{ isSubmitting, values, setFieldValue }"
          :validation-schema="schema"
          :initial-values="initial"
          keep-values
          @submit="onCreate"
        >
          <div class="space-y-4">
            <FormField name="tag_name">
              <FormItem>
                <FormLabel>{{ t('release.fields.tag') }}</FormLabel>
                <FormControl>
                  <Select
                    :model-value="values.tag_name"
                    @update:model-value="(v) => setFieldValue('tag_name', String(v))"
                  >
                    <SelectTrigger>
                      <SelectValue :placeholder="t('release.fields.tagHint')" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        <SelectLabel>{{ t('repo.tabs.tags') }}</SelectLabel>
                        <SelectItem v-for="tg in tags" :key="tg.name" :value="tg.name">
                          {{ tg.name }}
                        </SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                </FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField v-slot="{ componentField }" name="title">
              <FormItem>
                <FormLabel>{{ t('release.fields.title') }}</FormLabel>
                <FormControl>
                  <Input type="text" autocomplete="off" v-bind="componentField" />
                </FormControl>
                <p class="text-xs text-muted-foreground">{{ t('release.fields.titleHint') }}</p>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField v-slot="{ componentField }" name="notes">
              <FormItem>
                <FormLabel>{{ t('release.fields.notes') }}</FormLabel>
                <FormControl>
                  <Textarea rows="6" v-bind="componentField" />
                </FormControl>
                <p class="text-xs text-muted-foreground">{{ t('release.fields.notesHint') }}</p>
                <FormMessage />
              </FormItem>
            </FormField>

            <p v-if="createError" class="text-sm text-destructive">
              {{ createError }}
            </p>
          </div>

          <div class="mt-6 flex items-center gap-3">
            <Button type="submit" :disabled="isSubmitting">
              <Rocket class="size-4" />
              {{ isSubmitting ? t('release.creating') : t('release.create') }}
            </Button>
            <Button type="button" variant="outline" @click="router.back()">
              {{ t('common.cancel') }}
            </Button>
          </div>
        </Form>
      </CardContent>
    </Card>
  </div>
</template>
