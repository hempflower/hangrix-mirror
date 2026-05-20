<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'
import { GitBranch, Plus, Star, Trash2 } from 'lucide-vue-next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { PublicRepo, RepoRefs } from '~/types/repo'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()

setBreadcrumbs(() => {
  const owner = String(route.params.owner ?? '')
  const name = String(route.params.name ?? '')
  const base = `/${owner}/${name}`
  return [
    { label: owner, to: base },
    { label: name, to: base },
    { label: t('repo.tabs.branches') },
  ]
})
const { user } = useCurrentUser()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
useHead({ title: () => `${t('repo.branches.title')} · ${owner.value}/${name.value} - ${t('app.name')}` })

const repo = ref<PublicRepo | null>(null)
const refs = ref<RepoRefs | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

const createOpen = ref(false)
const createError = ref<string | null>(null)

const canManage = computed(() => {
  if (!repo.value || !user.value) return false
  return user.value.role === 'admin' || user.value.id === repo.value.owner_id
})

const branches = computed(() => refs.value?.branches ?? [])
const tags = computed(() => refs.value?.tags ?? [])
const defaultBranch = computed(() => refs.value?.default_branch ?? '')

const schema = computed(() => toTypedSchema(z.object({
  name: z.string().min(1).max(100),
  start_ref: z.string().min(1),
})))

const initial = computed(() => ({ name: '', start_ref: defaultBranch.value || '' }))

async function load() {
  loading.value = true
  error.value = null
  try {
    repo.value = await $fetch<PublicRepo>(`/api/repos/${owner.value}/${name.value}`, {
      credentials: 'include',
    })
    refs.value = await $fetch<RepoRefs>(`/api/repos/${owner.value}/${name.value}/refs`, {
      credentials: 'include',
    })
  } catch (e: any) {
    error.value = e?.data?.error ?? t('repo.loadFailed')
  } finally {
    loading.value = false
  }
}

function shortSha(s: string) { return s.slice(0, 7) }

async function onCreate(values: any, ctx: any) {
  createError.value = null
  try {
    await $fetch(`/api/repos/${owner.value}/${name.value}/branches`, {
      method: 'POST',
      credentials: 'include',
      body: { name: values.name, start_ref: values.start_ref },
    })
    createOpen.value = false
    ctx?.resetForm?.({ values: initial.value })
    await load()
  } catch (e: any) {
    createError.value = e?.data?.error ?? t('repo.branches.deleteFailed')
  }
}

async function setDefault(branchName: string) {
  try {
    repo.value = await $fetch<PublicRepo>(`/api/repos/${owner.value}/${name.value}`, {
      method: 'PATCH',
      credentials: 'include',
      body: { default_branch: branchName },
    })
    await load()
  } catch (e: any) {
    error.value = e?.data?.error ?? t('repo.branches.setDefaultFailed')
  }
}

function encodeBranch(n: string) {
  return encodeURIComponent(n).replace(/%2F/g, '/')
}

async function onDelete(branchName: string) {
  // eslint-disable-next-line no-alert
  if (!window.confirm(t('repo.branches.deleteConfirm', { name: branchName }))) return
  try {
    await $fetch(`/api/repos/${owner.value}/${name.value}/branches/${encodeBranch(branchName)}`, {
      method: 'DELETE',
      credentials: 'include',
    })
    await load()
  } catch (e: any) {
    error.value = e?.data?.error ?? t('repo.branches.deleteFailed')
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <header class="space-y-2">
      <nav class="text-sm text-muted-foreground">
        <NuxtLink :to="`/${owner}/${name}`" class="hover:text-foreground">
          {{ owner }} / {{ name }}
        </NuxtLink>
        <span class="mx-1">/</span>
        <span class="font-medium text-foreground">{{ t('repo.branches.title') }}</span>
      </nav>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="space-y-1">
          <h1 class="text-2xl font-semibold tracking-tight">
            {{ t('repo.branches.title') }}
          </h1>
          <p class="text-sm text-muted-foreground">
            {{ t('repo.branches.subtitle') }}
          </p>
        </div>
        <Button v-if="canManage" @click="createOpen = true">
          <Plus class="size-4" />
          {{ t('repo.branches.create') }}
        </Button>
      </div>
    </header>

    <p v-if="error" class="text-sm text-destructive">
      {{ error }}
    </p>

    <Card class="gap-0 py-0">
      <CardHeader class="rounded-t-xl border-b bg-muted/40 px-4 py-2">
        <CardTitle class="text-sm font-medium">
          {{ t('repo.branches.title') }} · {{ branches.length }}
        </CardTitle>
        <CardDescription v-if="defaultBranch">
          {{ t('repo.defaultBranch') }}: <code class="font-mono">{{ defaultBranch }}</code>
        </CardDescription>
      </CardHeader>
      <CardContent class="p-0">
        <p v-if="loading" class="p-3 text-sm text-muted-foreground">
          {{ t('common.loading') }}
        </p>
        <p v-else-if="branches.length === 0" class="p-6 text-center text-sm text-muted-foreground">
          {{ t('repo.branches.empty') }}
        </p>
        <Table v-else>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t('repo.branches.name') }}</TableHead>
              <TableHead>SHA</TableHead>
              <TableHead></TableHead>
              <TableHead class="text-right">{{ t('common.actions') }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="b in branches" :key="b.name">
              <TableCell class="font-medium">
                <span class="inline-flex items-center gap-2">
                  <GitBranch class="size-3.5 text-muted-foreground" />
                  <NuxtLink
                    :to="`/${owner}/${name}?ref=${encodeURIComponent(b.name)}`"
                    class="hover:underline"
                  >
                    {{ b.name }}
                  </NuxtLink>
                </span>
              </TableCell>
              <TableCell>
                <code class="font-mono text-xs text-muted-foreground">{{ shortSha(b.sha) }}</code>
              </TableCell>
              <TableCell>
                <Badge v-if="b.name === defaultBranch" variant="secondary">
                  {{ t('repo.branches.default') }}
                </Badge>
              </TableCell>
              <TableCell class="space-x-2 text-right">
                <Button
                  v-if="canManage && b.name !== defaultBranch"
                  size="sm"
                  variant="outline"
                  @click="setDefault(b.name)"
                >
                  <Star class="size-3" />
                  {{ t('repo.branches.setDefault') }}
                </Button>
                <Button
                  v-if="canManage"
                  size="sm"
                  variant="outline"
                  :disabled="b.name === defaultBranch"
                  :title="b.name === defaultBranch ? t('repo.branches.deleteDefaultBlocked') : ''"
                  @click="onDelete(b.name)"
                >
                  <Trash2 class="size-3" />
                  {{ t('repo.branches.delete') }}
                </Button>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </CardContent>
    </Card>

    <Dialog v-model:open="createOpen">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{{ t('repo.branches.create') }}</DialogTitle>
          <DialogDescription>{{ t('repo.branches.subtitle') }}</DialogDescription>
        </DialogHeader>
        <Form
          v-slot="{ isSubmitting, values, setFieldValue }"
          :validation-schema="schema"
          :initial-values="initial"
          keep-values
          @submit="onCreate"
        >
          <div class="space-y-4">
            <FormField v-slot="{ componentField }" name="name">
              <FormItem>
                <FormLabel>{{ t('repo.branches.name') }}</FormLabel>
                <FormControl>
                  <Input type="text" autocomplete="off" v-bind="componentField" />
                </FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField name="start_ref">
              <FormItem>
                <FormLabel>{{ t('repo.branches.startRef') }}</FormLabel>
                <FormControl>
                  <Select
                    :model-value="values.start_ref"
                    @update:model-value="(v) => setFieldValue('start_ref', String(v))"
                  >
                    <SelectTrigger>
                      <SelectValue :placeholder="t('repo.branches.startRef')" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup v-if="branches.length > 0">
                        <SelectLabel>{{ t('repo.branches.title') }}</SelectLabel>
                        <SelectItem v-for="b in branches" :key="`b-${b.name}`" :value="b.name">
                          {{ b.name }}
                        </SelectItem>
                      </SelectGroup>
                      <SelectGroup v-if="tags.length > 0">
                        <SelectLabel>{{ t('repo.tags.title') }}</SelectLabel>
                        <SelectItem v-for="tg in tags" :key="`t-${tg.name}`" :value="tg.name">
                          {{ tg.name }}
                        </SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                </FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <p v-if="createError" class="text-sm text-destructive">
              {{ createError }}
            </p>
          </div>
          <DialogFooter class="mt-6">
            <Button type="button" variant="outline" @click="createOpen = false">
              {{ t('common.cancel') }}
            </Button>
            <Button type="submit" :disabled="isSubmitting">
              {{ isSubmitting ? t('repo.branches.submitting') : t('repo.branches.submit') }}
            </Button>
          </DialogFooter>
        </Form>
      </DialogContent>
    </Dialog>
  </div>
</template>
