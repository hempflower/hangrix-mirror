<script setup lang="ts">
import { computed } from 'vue'
import { ArrowRight, BadgeCheck, FolderGit2, ShieldCheck, User as UserIcon, Users } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'

const { t } = useI18n()
const { user } = useCurrentUser()

setBreadcrumbs(() => [{ label: t('nav.dashboard') }])
useHead({ title: () => `${t('home.title')} - ${t('app.name')}` })

const stats = computed(() => {
  if (!user.value) return []
  return [
    {
      key: 'role',
      label: t('home.stats.role'),
      value: t(`role.${user.value.role}`),
      icon: ShieldCheck,
    },
    {
      key: 'status',
      label: t('home.stats.status'),
      value: user.value.disabled ? t('status.disabled') : t('status.active'),
      icon: BadgeCheck,
    },
  ]
})
</script>

<template>
  <div class="space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ t('home.title') }}
      </h1>
      <p class="text-sm text-muted-foreground">
        {{ t('home.welcome', { name: user?.username }) }} — {{ t('home.subtitle') }}
      </p>
    </header>

    <section class="grid gap-4 sm:grid-cols-2">
      <Card v-for="s in stats" :key="s.key">
        <CardHeader>
          <CardDescription>{{ s.label }}</CardDescription>
          <CardTitle class="text-xl font-semibold tabular-nums">
            {{ s.value }}
          </CardTitle>
          <CardAction>
            <component :is="s.icon" class="size-4 text-muted-foreground" />
          </CardAction>
        </CardHeader>
      </Card>
    </section>

    <section class="grid gap-4 lg:grid-cols-3">
      <Card class="lg:col-span-2">
        <CardHeader>
          <CardTitle>{{ t('home.m1Title') }}</CardTitle>
          <CardDescription>{{ t('home.m1Description') }}</CardDescription>
        </CardHeader>
        <CardContent class="flex items-center gap-2">

          <span class="text-xs text-muted-foreground">{{ t('app.tagline') }}</span>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{{ t('home.quickActions.title') }}</CardTitle>
          <CardDescription>{{ t('home.quickActions.description') }}</CardDescription>
        </CardHeader>
        <CardContent class="flex flex-col gap-2">
          <Button variant="outline" class="justify-between" as-child>
            <NuxtLink to="/repos/new">
              <span class="flex items-center gap-2">
                <FolderGit2 class="size-4" />
                {{ t('home.quickActions.newRepo') }}
              </span>
              <ArrowRight class="size-4" />
            </NuxtLink>
          </Button>
          <Button variant="outline" class="justify-between" as-child>
            <NuxtLink to="/profile">
              <span class="flex items-center gap-2">
                <UserIcon class="size-4" />
                {{ t('home.quickActions.profile') }}
              </span>
              <ArrowRight class="size-4" />
            </NuxtLink>
          </Button>
          <Button v-if="user?.role === 'admin'" variant="outline" class="justify-between" as-child>
            <NuxtLink to="/admin/users">
              <span class="flex items-center gap-2">
                <Users class="size-4" />
                {{ t('home.quickActions.users') }}
              </span>
              <ArrowRight class="size-4" />
            </NuxtLink>
          </Button>
        </CardContent>
      </Card>
    </section>
  </div>
</template>
