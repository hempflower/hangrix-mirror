<script setup lang="ts">
import { computed, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Label } from '@/components/ui/label'
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from '@/components/ui/form'
import type { PublicRepo } from '~/types/repo'

const { t } = useI18n()
const router = useRouter()

const schema = computed(() => toTypedSchema(z.object({
  name: z.string().regex(/^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$/),
  description: z.string().max(500).optional(),
  visibility: z.enum(['public', 'private']),
  default_branch: z.string().max(100).optional(),
  init_readme: z.boolean().optional(),
})))

const initial = {
  name: '',
  description: '',
  visibility: 'private' as const,
  default_branch: 'main',
  init_readme: true,
}

const formError = ref<string | null>(null)

async function onSubmit(values: any) {
  formError.value = null
  const body: Record<string, any> = {
    name: values.name,
    description: values.description ?? '',
    visibility: values.visibility,
    init_readme: !!values.init_readme,
  }
  if (values.default_branch && values.default_branch.trim()) {
    body.default_branch = values.default_branch.trim()
  }
  try {
    const repo = await $fetch<PublicRepo>('/api/repos', {
      method: 'POST',
      credentials: 'include',
      body,
    })
    router.push(`/${repo.owner_username}/${repo.name}`)
  } catch (e: any) {
    formError.value = e?.data?.error ?? t('repo.createFailed')
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-2xl space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ t('repo.createTitle') }}
      </h1>
      <p class="text-sm text-muted-foreground">
        {{ t('repo.createSubtitle') }}
      </p>
    </header>

    <Card>
      <Form v-slot="{ isSubmitting, values, setFieldValue }" :validation-schema="schema" :initial-values="initial" keep-values @submit="onSubmit">
        <CardHeader>
          <CardTitle>{{ t('repo.create') }}</CardTitle>
          <CardDescription>{{ t('repo.createSubtitle') }}</CardDescription>
        </CardHeader>
        <CardContent class="space-y-4">
          <FormField v-slot="{ componentField }" name="name">
            <FormItem>
              <FormLabel>{{ t('repo.name') }}</FormLabel>
              <FormControl>
                <Input type="text" autocomplete="off" v-bind="componentField" />
              </FormControl>
              <p class="text-xs text-muted-foreground">{{ t('repo.nameHint') }}</p>
              <FormMessage />
            </FormItem>
          </FormField>

          <FormField v-slot="{ componentField }" name="description">
            <FormItem>
              <FormLabel>{{ t('repo.description') }}</FormLabel>
              <FormControl>
                <Input type="text" autocomplete="off" v-bind="componentField" />
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>

          <FormField name="visibility">
            <FormItem class="space-y-3">
              <FormLabel>{{ t('repo.visibility') }}</FormLabel>
              <FormControl>
                <RadioGroup
                  :model-value="values.visibility"
                  class="gap-3"
                  @update:model-value="(v) => setFieldValue('visibility', v as 'public' | 'private')"
                >
                  <div class="flex items-start gap-3 rounded-md border p-3">
                    <RadioGroupItem id="visibility-private" value="private" class="mt-1" />
                    <div class="space-y-0.5">
                      <Label for="visibility-private" class="text-sm font-medium">
                        {{ t('repo.visibilityPrivate') }}
                      </Label>
                      <p class="text-xs text-muted-foreground">{{ t('repo.visibilityPrivateHint') }}</p>
                    </div>
                  </div>
                  <div class="flex items-start gap-3 rounded-md border p-3">
                    <RadioGroupItem id="visibility-public" value="public" class="mt-1" />
                    <div class="space-y-0.5">
                      <Label for="visibility-public" class="text-sm font-medium">
                        {{ t('repo.visibilityPublic') }}
                      </Label>
                      <p class="text-xs text-muted-foreground">{{ t('repo.visibilityPublicHint') }}</p>
                    </div>
                  </div>
                </RadioGroup>
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>

          <FormField v-slot="{ componentField }" name="default_branch">
            <FormItem>
              <FormLabel>{{ t('repo.defaultBranch') }}</FormLabel>
              <FormControl>
                <Input type="text" autocomplete="off" v-bind="componentField" />
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>

          <FormField name="init_readme">
            <FormItem>
              <div class="flex items-start gap-3 rounded-md border p-3">
                <Checkbox
                  id="init-readme"
                  class="mt-1"
                  :model-value="!!values.init_readme"
                  @update:model-value="(v) => setFieldValue('init_readme', !!v)"
                />
                <div class="space-y-0.5">
                  <Label for="init-readme" class="text-sm font-medium">
                    {{ t('repo.initReadme') }}
                  </Label>
                  <p class="text-xs text-muted-foreground">{{ t('repo.initReadmeHint') }}</p>
                </div>
              </div>
              <FormMessage />
            </FormItem>
          </FormField>

          <p v-if="formError" class="text-sm text-destructive">
            {{ formError }}
          </p>
        </CardContent>
        <CardFooter class="flex items-center justify-between">
          <NuxtLink to="/repos" class="text-sm text-muted-foreground hover:text-foreground">
            {{ t('common.cancel') }}
          </NuxtLink>
          <Button type="submit" :disabled="isSubmitting">
            {{ isSubmitting ? t('repo.submitting') : t('repo.submit') }}
          </Button>
        </CardFooter>
      </Form>
    </Card>
  </div>
</template>
