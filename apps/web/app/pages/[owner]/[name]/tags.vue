<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'
import { Plus, Tag as TagIcon, Trash2 } from 'lucide-vue-next'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
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
import { Label } from '@/components/ui/label'
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
import { Textarea } from '@/components/ui/textarea'
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
    { label: t('repo.tabs.tags') },
  ]
})
const { user } = useCurrentUser()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
useHead({ title: () => `${t('repo.tags.title')} · ${owner.value}/${name.value} - ${t('app.name')}` })

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
  ref: z.string().min(1),
  annotated: z.boolean().optional(),
  message: z.string().optional(),
}).refine(v => !v.annotated || (v.message && v.message.trim().length > 0), {
  path: ['message'],
  message: ' annotated_requires_message',
})))

const initial = computed(() => ({
  name: '',
  ref: defaultBranch.value || '',
  annotated: false,
  message: '',
}))

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
  const body: Record<string, any> = {
    name: values.name,
    ref: values.ref,
    annotated: !!values.annotated,
  }
  if (values.annotated && values.message) body.message = values.message
  try {
    await $fetch(`/api/repos/${owner.value}/${name.value}/tags`, {
      method: 'POST',
      credentials: 'include',
      body,
    })
    createOpen.value = false
    ctx?.resetForm?.({ values: initial.value })
    await load()
  } catch (e: any) {
    createError.value = e?.data?.error ?? t('repo.tags.deleteFailed')
  }
}

function encodeRef(n: string) {
  return encodeURIComponent(n).replace(/%2F/g, '/')
}

async function onDelete(tagName: string) {
  // eslint-disable-next-line no-alert
  if (!window.confirm(t('repo.tags.deleteConfirm', { name: tagName }))) return
  try {
    await $fetch(`/api/repos/${owner.value}/${name.value}/tags/${encodeRef(tagName)}`, {
      method: 'DELETE',
      credentials: 'include',
    })
    await load()
  } catch (e: any) {
    error.value = e?.data?.error ?? t('repo.tags.deleteFailed')
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
        <span class="font-medium text-foreground">{{ t('repo.tags.title') }}</span>
      </nav>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="space-y-1">
          <h1 class="text-2xl font-semibold tracking-tight">
            {{ t('repo.tags.title') }}
          </h1>
          <p class="text-sm text-muted-foreground">
            {{ t('repo.tags.subtitle') }}
          </p>
        </div>
        <Button v-if="canManage" @click="createOpen = true">
          <Plus class="size-4" />
          {{ t('repo.tags.create') }}
        </Button>
      </div>
    </header>

    <p v-if="error" class="text-sm text-destructive">
      {{ error }}
    </p>

    <Card class="gap-0 py-0">
      <CardHeader class="rounded-t-xl border-b bg-muted/40 px-4 py-2">
        <CardTitle class="text-sm font-medium">
          {{ t('repo.tags.title') }} · {{ tags.length }}
        </CardTitle>
      </CardHeader>
      <CardContent class="p-0">
        <p v-if="loading" class="p-3 text-sm text-muted-foreground">
          {{ t('common.loading') }}
        </p>
        <p v-else-if="tags.length === 0" class="p-6 text-center text-sm text-muted-foreground">
          {{ t('repo.tags.empty') }}
        </p>
        <Table v-else>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t('repo.tags.name') }}</TableHead>
              <TableHead>SHA</TableHead>
              <TableHead class="text-right">{{ t('common.actions') }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="tg in tags" :key="tg.name">
              <TableCell class="font-medium">
                <span class="inline-flex items-center gap-2">
                  <TagIcon class="size-3.5 text-muted-foreground" />
                  <NuxtLink
                    :to="`/${owner}/${name}?ref=${encodeURIComponent(tg.name)}`"
                    class="hover:underline"
                  >
                    {{ tg.name }}
                  </NuxtLink>
                </span>
              </TableCell>
              <TableCell>
                <code class="font-mono text-xs text-muted-foreground">{{ shortSha(tg.sha) }}</code>
              </TableCell>
              <TableCell class="text-right">
                <Button
                  v-if="canManage"
                  size="sm"
                  variant="outline"
                  @click="onDelete(tg.name)"
                >
                  <Trash2 class="size-3" />
                  {{ t('repo.tags.delete') }}
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
          <DialogTitle>{{ t('repo.tags.create') }}</DialogTitle>
          <DialogDescription>{{ t('repo.tags.subtitle') }}</DialogDescription>
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
                <FormLabel>{{ t('repo.tags.name') }}</FormLabel>
                <FormControl>
                  <Input type="text" autocomplete="off" v-bind="componentField" />
                </FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField name="ref">
              <FormItem>
                <FormLabel>{{ t('repo.tags.ref') }}</FormLabel>
                <FormControl>
                  <Select
                    :model-value="values.ref"
                    @update:model-value="(v) => setFieldValue('ref', String(v))"
                  >
                    <SelectTrigger>
                      <SelectValue :placeholder="t('repo.tags.ref')" />
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

            <FormField name="annotated">
              <FormItem>
                <div class="flex items-start gap-2 rounded-md border p-3">
                  <Checkbox
                    id="tag-annotated"
                    class="mt-1"
                    :model-value="!!values.annotated"
                    @update:model-value="(v) => setFieldValue('annotated', !!v)"
                  />
                  <div class="space-y-0.5">
                    <Label for="tag-annotated" class="cursor-pointer text-sm font-medium">
                      {{ t('repo.tags.annotated') }}
                    </Label>
                    <p class="text-xs text-muted-foreground">{{ t('repo.tags.annotatedHint') }}</p>
                  </div>
                </div>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField v-if="values.annotated" v-slot="{ componentField }" name="message">
              <FormItem>
                <FormLabel>{{ t('repo.tags.message') }}</FormLabel>
                <FormControl>
                  <Textarea rows="3" v-bind="componentField" />
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
              {{ isSubmitting ? t('repo.tags.submitting') : t('repo.tags.submit') }}
            </Button>
          </DialogFooter>
        </Form>
      </DialogContent>
    </Dialog>
  </div>
</template>
