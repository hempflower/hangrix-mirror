<script setup lang="ts">
import { computed, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from '@/components/ui/form'
import type { User } from '~/types/user'

definePageMeta({ layout: 'auth' })

const { t } = useI18n()
useHead({ title: () => `${t('register.title')} - ${t('app.name')}` })
const router = useRouter()
const { refresh } = useCurrentUser()

const schema = computed(() => toTypedSchema(z.object({
  username: z.string().min(3).max(32),
  email: z.string().email(),
  password: z.string().min(8),
})))

const formError = ref<string | null>(null)

async function onSubmit(values: any) {
  formError.value = null
  try {
    await $fetch<User>('/api/auth/register', {
      method: 'POST',
      credentials: 'include',
      body: values,
    })
    await refresh()
    router.push('/')
  } catch (e: any) {
    formError.value = e?.data?.error ?? t('register.failed')
  }
}
</script>

<template>
  <Card class="w-full max-w-sm">
      <CardHeader>
        <CardTitle>{{ t('register.title') }}</CardTitle>
        <CardDescription>{{ t('register.description') }}</CardDescription>
      </CardHeader>
      <Form v-slot="{ isSubmitting }" :validation-schema="schema" @submit="onSubmit">
        <CardContent class="space-y-4">
          <FormField v-slot="{ componentField }" name="username">
            <FormItem>
              <FormLabel>{{ t('common.username') }}</FormLabel>
              <FormControl>
                <Input type="text" autocomplete="username" v-bind="componentField" />
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>
          <FormField v-slot="{ componentField }" name="email">
            <FormItem>
              <FormLabel>{{ t('common.email') }}</FormLabel>
              <FormControl>
                <Input type="email" autocomplete="email" v-bind="componentField" />
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>
          <FormField v-slot="{ componentField }" name="password">
            <FormItem>
              <FormLabel>{{ t('common.password') }}</FormLabel>
              <FormControl>
                <Input type="password" autocomplete="new-password" v-bind="componentField" />
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>
          <p v-if="formError" class="text-sm text-destructive">
            {{ formError }}
          </p>
        </CardContent>
        <CardFooter class="flex items-center justify-between">
          <NuxtLink to="/login" class="text-sm text-muted-foreground hover:text-foreground">
            {{ t('register.haveAccount') }}
          </NuxtLink>
          <Button type="submit" :disabled="isSubmitting">
            {{ isSubmitting ? t('register.submitting') : t('register.submit') }}
          </Button>
        </CardFooter>
      </Form>
    </Card>
</template>
