<script setup lang="ts">
import { onMounted, ref } from 'vue'

definePageMeta({ layout: 'admin' })
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { User, UserListResp } from '~/types/user'

const { t } = useI18n()
const { user: me } = useCurrentUser()
useHead({ title: () => `${t('admin.users.title')} - ${t('admin.section')} - ${t('app.name')}` })

setBreadcrumbs(() => [
  { label: t('admin.section'), to: '/admin/users' },
  { label: t('admin.users.title') },
])

const users = ref<User[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const res = await $fetch<UserListResp>('/api/admin/users', { credentials: 'include' })
    users.value = res.items
    total.value = res.total
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.users.loadFailed')
  } finally {
    loading.value = false
  }
}

async function toggleDisabled(u: User) {
  try {
    const updated = await $fetch<User>(`/api/admin/users/${u.id}`, {
      method: 'PATCH',
      credentials: 'include',
      body: { disabled: !u.disabled },
    })
    Object.assign(u, updated)
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.users.updateFailed')
  }
}

async function toggleRole(u: User) {
  const next = u.role === 'admin' ? 'user' : 'admin'
  try {
    const updated = await $fetch<User>(`/api/admin/users/${u.id}`, {
      method: 'PATCH',
      credentials: 'include',
      body: { role: next },
    })
    Object.assign(u, updated)
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.users.updateFailed')
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ t('admin.users.title') }}
      </h1>
      <p class="text-sm text-muted-foreground">
        {{ t('admin.users.subtitle') }} · {{ t('common.total', { n: total }) }}
      </p>
    </header>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('admin.users.cardTitle') }}</CardTitle>
        <CardDescription>{{ t('admin.users.cardDescription') }}</CardDescription>
      </CardHeader>
      <CardContent>
        <p v-if="error" class="mb-3 text-sm text-destructive">
          {{ error }}
        </p>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t('common.id') }}</TableHead>
              <TableHead>{{ t('common.username') }}</TableHead>
              <TableHead>{{ t('common.email') }}</TableHead>
              <TableHead>{{ t('common.role') }}</TableHead>
              <TableHead>{{ t('common.status') }}</TableHead>
              <TableHead class="text-right">
                {{ t('common.actions') }}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="u in users" :key="u.id">
              <TableCell>{{ u.id }}</TableCell>
              <TableCell class="font-medium">
                {{ u.username }}
              </TableCell>
              <TableCell class="text-muted-foreground">
                {{ u.email }}
              </TableCell>
              <TableCell>
                <Badge :variant="u.role === 'admin' ? 'secondary' : 'outline'">{{ t(`role.${u.role}`) }}</Badge>
              </TableCell>
              <TableCell>
                <Badge v-if="u.disabled" variant="destructive">{{ t('status.disabled') }}</Badge>
                <Badge v-else variant="outline">{{ t('status.active') }}</Badge>
              </TableCell>
              <TableCell class="space-x-2 text-right">
                <Button
                  size="sm"
                  variant="outline"
                  :disabled="u.id === me?.id"
                  @click="toggleRole(u)"
                >
                  {{ u.role === 'admin' ? t('admin.users.demote') : t('admin.users.promote') }}
                </Button>
                <Button
                  size="sm"
                  :variant="u.disabled ? 'outline' : 'destructive'"
                  :disabled="u.id === me?.id"
                  @click="toggleDisabled(u)"
                >
                  {{ u.disabled ? t('admin.users.enable') : t('admin.users.disable') }}
                </Button>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
        <p v-if="loading" class="mt-3 text-sm text-muted-foreground">
          {{ t('common.loading') }}
        </p>
      </CardContent>
    </Card>
  </div>
</template>
