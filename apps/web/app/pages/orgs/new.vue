<script setup lang="ts">
import { computed, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from '@/components/ui/form'
import type { PublicOrg } from '~/types/org'

const { t } = useI18n()
useHead({ title: () => `${t('org.createTitle')} - ${t('app.name')}` })

setBreadcrumbs(() => [{ label: t('org.createTitle') }])
const router = useRouter()
const { refresh: refreshMyOrgs } = useMyOrgs()

const schema = computed(() => toTypedSchema(z.object({
  name: z.string().regex(/^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$/),
  display_name: z.string().max(100).optional(),
  description: z.string().max(500).optional(),
})))

const initial = {
  name: '',
  display_name: '',
  description: '',
}

const formError = ref<string | null>(null)

async function onSubmit(values: any) {
  formError.value = null
  try {
    const org = await $fetch<PublicOrg>('/api/orgs', {
      method: 'POST',
      credentials: 'include',
      body: {
        name: values.name,
        display_name: values.display_name ?? '',
        description: values.description ?? '',
      },
    })
    await refreshMyOrgs()
    router.push(`/${org.name}`)
  } catch (e: any) {
    formError.value = e?.data?.error ?? t('org.createFailed')
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-2xl space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ t('org.createTitle') }}
      </h1>
      <p class="text-sm text-muted-foreground">
        {{ t('org.createSubtitle') }}
      </p>
    </header>

    <Card>
      <Form v-slot="{ isSubmitting }" :validation-schema="schema" :initial-values="initial" keep-values @submit="onSubmit">
        <CardHeader>
          <CardTitle>{{ t('org.create') }}</CardTitle>
          <CardDescription>{{ t('org.createSubtitle') }}</CardDescription>
        </CardHeader>
        <CardContent class="space-y-4">
          <FormField v-slot="{ componentField }" name="name">
            <FormItem>
              <FormLabel>{{ t('org.name') }}</FormLabel>
              <FormControl>
                <Input type="text" autocomplete="off" v-bind="componentField" />
              </FormControl>
              <p class="text-xs text-muted-foreground">{{ t('org.nameHint') }}</p>
              <FormMessage />
            </FormItem>
          </FormField>

          <FormField v-slot="{ componentField }" name="display_name">
            <FormItem>
              <FormLabel>{{ t('org.displayName') }}</FormLabel>
              <FormControl>
                <Input type="text" autocomplete="off" v-bind="componentField" />
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>

          <FormField v-slot="{ componentField }" name="description">
            <FormItem>
              <FormLabel>{{ t('org.description') }}</FormLabel>
              <FormControl>
                <Input type="text" autocomplete="off" v-bind="componentField" />
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>

          <p v-if="formError" class="text-sm text-destructive">
            {{ formError }}
          </p>
        </CardContent>
        <CardFooter class="flex items-center justify-between">
          <NuxtLink to="/" class="text-sm text-muted-foreground hover:text-foreground">
            {{ t('common.cancel') }}
          </NuxtLink>
          <Button type="submit" :disabled="isSubmitting">
            {{ isSubmitting ? t('common.saving') : t('org.submit') }}
          </Button>
        </CardFooter>
      </Form>
    </Card>
  </div>
</template>
