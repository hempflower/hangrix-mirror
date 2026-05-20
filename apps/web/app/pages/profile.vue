<script setup lang="ts">
import { computed, ref, watchEffect } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'


import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from '@/components/ui/form'
import PersonalAccessTokens from '@/components/tokens/PersonalAccessTokens.vue'

import type { User } from '~/types/user'

const { t } = useI18n()
const { user, refresh } = useCurrentUser()

setBreadcrumbs(() => [{ label: t('nav.profile') }])
useHead({ title: () => `${t('profile.title')} - ${t('app.name')}` })

// Email form
const emailSchema = computed(() => toTypedSchema(z.object({
  email: z.string().email(),
})))
const emailInitial = ref({ email: '' })
watchEffect(() => {
  if (user.value) emailInitial.value = { email: user.value.email }
})
const profileMsg = ref<{ kind: 'ok' | 'err'; text: string } | null>(null)

async function onSaveProfile(values: any) {
  profileMsg.value = null
  try {
    await $fetch<User>('/api/users/me', {
      method: 'PATCH',
      credentials: 'include',
      body: { email: values.email },
    })
    await refresh()
    profileMsg.value = { kind: 'ok', text: t('profile.profileSaved') }
  } catch (e: any) {
    profileMsg.value = { kind: 'err', text: e?.data?.error ?? t('profile.updateFailed') }
  }
}

// Password form
const passwordSchema = computed(() => toTypedSchema(z.object({
  old_password: z.string().min(1),
  new_password: z.string().min(8),
})))
const passMsg = ref<{ kind: 'ok' | 'err'; text: string } | null>(null)

async function onChangePassword(values: any, ctx: any) {
  passMsg.value = null
  try {
    await $fetch('/api/users/me', {
      method: 'PATCH',
      credentials: 'include',
      body: values,
    })
    ctx.resetForm()
    passMsg.value = { kind: 'ok', text: t('profile.passwordChanged') }
  } catch (e: any) {
    passMsg.value = { kind: 'err', text: e?.data?.error ?? t('profile.changeFailed') }
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-4xl space-y-6">
    <header class="space-y-2">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ t('profile.title') }}
      </h1>
      <p class="text-sm text-muted-foreground">
        {{ t('profile.subtitle') }}
      </p>
      <div v-if="user" class="flex items-center gap-2 text-sm text-muted-foreground">
        <span>{{ user.username }}</span>
        <Badge :variant="user.role === 'admin' ? 'secondary' : 'outline'">{{ t(`role.${user.role}`) }}</Badge>
      </div>
    </header>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('profile.emailSection') }}</CardTitle>
        <CardDescription>{{ t('profile.emailDescription') }}</CardDescription>
      </CardHeader>
      <Form
        v-slot="{ isSubmitting }"
        :validation-schema="emailSchema"
        :initial-values="emailInitial"
        keep-values
        @submit="onSaveProfile"
      >
        <CardContent class="space-y-4">
          <FormField v-slot="{ componentField }" name="email">
            <FormItem>
              <FormLabel>{{ t('common.email') }}</FormLabel>
              <FormControl>
                <Input type="email" autocomplete="email" v-bind="componentField" />
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>
          <p v-if="profileMsg" :class="profileMsg.kind === 'ok' ? 'text-sm text-emerald-500' : 'text-sm text-destructive'">
            {{ profileMsg.text }}
          </p>
        </CardContent>
        <CardFooter>
          <Button type="submit" :disabled="isSubmitting">
            {{ isSubmitting ? t('profile.savingProfile') : t('profile.saveProfile') }}
          </Button>
        </CardFooter>
      </Form>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('profile.passwordSection') }}</CardTitle>
        <CardDescription>{{ t('profile.passwordDescription') }}</CardDescription>
      </CardHeader>
      <Form v-slot="{ isSubmitting }" :validation-schema="passwordSchema" @submit="onChangePassword">
        <CardContent class="space-y-4">
          <FormField v-slot="{ componentField }" name="old_password">
            <FormItem>
              <FormLabel>{{ t('common.currentPassword') }}</FormLabel>
              <FormControl>
                <Input type="password" autocomplete="current-password" v-bind="componentField" />
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>
          <FormField v-slot="{ componentField }" name="new_password">
            <FormItem>
              <FormLabel>{{ t('common.newPassword') }}</FormLabel>
              <FormControl>
                <Input type="password" autocomplete="new-password" v-bind="componentField" />
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>
          <p v-if="passMsg" :class="passMsg.kind === 'ok' ? 'text-sm text-emerald-500' : 'text-sm text-destructive'">
            {{ passMsg.text }}
          </p>
        </CardContent>
        <CardFooter>
          <Button type="submit" :disabled="isSubmitting">
            {{ isSubmitting ? t('profile.changingPassword') : t('profile.changePassword') }}
          </Button>
        </CardFooter>
      </Form>
    </Card>

    <PersonalAccessTokens />
  </div>
</template>
