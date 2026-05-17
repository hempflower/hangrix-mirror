<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'
import { AlertTriangle, Pencil, Plus, Trash2, KeyRound } from 'lucide-vue-next'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
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
import { Textarea } from '@/components/ui/textarea'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

import type {
  LLMProvider,
  LLMProviderCreateReq,
  LLMProviderListResp,
  LLMProviderPatchReq,
  ProviderType,
} from '~/types/llm-provider'

definePageMeta({ layout: 'admin' })

const { t } = useI18n()

setBreadcrumbs(() => [
  { label: t('admin.section'), to: '/admin/llm' },
  { label: t('admin.llm.title') },
])

const providers = ref<LLMProvider[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

const createOpen = ref(false)
const createError = ref<string | null>(null)

const editing = ref<LLMProvider | null>(null)
const editError = ref<string | null>(null)
const editOpen = computed({
  get: () => editing.value !== null,
  set: (v: boolean) => { if (!v) editing.value = null },
})

const PROVIDER_TYPES: ProviderType[] = ['openai', 'anthropic', 'openai-compat']

const createSchema = computed(() => toTypedSchema(z.object({
  name: z.string().min(1).max(64).regex(/^[a-z0-9][a-z0-9-]{0,63}$/),
  type: z.enum(['openai', 'anthropic', 'openai-compat']),
  base_url: z.string().url(),
  api_key: z.string().min(1),
  allowed_models: z.string().min(1),
})))

const editSchema = computed(() => toTypedSchema(z.object({
  base_url: z.string().url(),
  api_key: z.string().optional(),
  allowed_models: z.string().min(1),
})))

const createInitial = { name: '', type: 'openai-compat' as ProviderType, base_url: '', api_key: '', allowed_models: '' }
const editInitial = computed(() => editing.value ? {
  base_url: editing.value.base_url,
  api_key: '',
  allowed_models: editing.value.allowed_models.join(', '),
} : { base_url: '', api_key: '', allowed_models: '' })

function parseModels(s: string): string[] {
  return s.split(/[\s,]+/).map(x => x.trim()).filter(Boolean)
}

async function load() {
  loading.value = true
  error.value = null
  try {
    const res = await $fetch<LLMProviderListResp>('/api/admin/llm/providers', { credentials: 'include' })
    providers.value = res.items ?? []
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.llm.loadFailed')
  } finally {
    loading.value = false
  }
}

async function onCreate(values: any, ctx: any) {
  createError.value = null
  const body: LLMProviderCreateReq = {
    name: values.name,
    type: values.type,
    base_url: values.base_url,
    api_key: values.api_key,
    allowed_models: parseModels(values.allowed_models),
  }
  try {
    await $fetch('/api/admin/llm/providers', { method: 'POST', credentials: 'include', body })
    createOpen.value = false
    ctx?.resetForm?.({ values: createInitial })
    await load()
  } catch (e: any) {
    createError.value = e?.data?.error ?? t('admin.llm.createFailed')
  }
}

async function onEdit(values: any) {
  if (!editing.value) return
  editError.value = null
  const body: LLMProviderPatchReq = {
    base_url: values.base_url,
    allowed_models: parseModels(values.allowed_models),
  }
  if (values.api_key && values.api_key.trim()) {
    body.api_key = values.api_key
  }
  try {
    await $fetch(`/api/admin/llm/providers/${editing.value.name}`, {
      method: 'PATCH',
      credentials: 'include',
      body,
    })
    editing.value = null
    await load()
  } catch (e: any) {
    editError.value = e?.data?.error ?? t('admin.llm.updateFailed')
  }
}

async function onDelete(p: LLMProvider) {
  // eslint-disable-next-line no-alert
  if (!window.confirm(t('admin.llm.deleteConfirm', { name: p.name }))) return
  try {
    await $fetch(`/api/admin/llm/providers/${p.name}`, { method: 'DELETE', credentials: 'include' })
    await load()
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.llm.deleteFailed')
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <header class="flex items-start justify-between gap-4">
      <div class="space-y-1">
        <h1 class="text-2xl font-semibold tracking-tight">
          {{ t('admin.llm.title') }}
        </h1>
        <p class="text-sm text-muted-foreground">
          {{ t('admin.llm.subtitle') }}
        </p>
      </div>
      <Button @click="createOpen = true">
        <Plus class="size-4" />
        {{ t('admin.llm.create') }}
      </Button>
    </header>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('admin.llm.cardTitle') }}</CardTitle>
        <CardDescription>{{ t('admin.llm.cardDescription') }}</CardDescription>
      </CardHeader>
      <CardContent>
        <p v-if="error" class="mb-3 text-sm text-destructive">{{ error }}</p>

        <div v-if="!loading && providers.length === 0" class="rounded-lg border border-dashed p-8 text-center">
          <KeyRound class="mx-auto size-8 text-muted-foreground" />
          <p class="mt-3 text-sm font-medium">{{ t('admin.llm.empty') }}</p>
          <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.llm.emptyHint') }}</p>
        </div>

        <Table v-else>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t('admin.llm.cols.name') }}</TableHead>
              <TableHead>{{ t('admin.llm.cols.type') }}</TableHead>
              <TableHead>{{ t('admin.llm.cols.baseUrl') }}</TableHead>
              <TableHead>{{ t('admin.llm.cols.apiKey') }}</TableHead>
              <TableHead>{{ t('admin.llm.cols.models') }}</TableHead>
              <TableHead class="text-right">{{ t('common.actions') }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="p in providers" :key="p.id">
              <TableCell class="font-medium">{{ p.name }}</TableCell>
              <TableCell><Badge variant="outline">{{ p.type }}</Badge></TableCell>
              <TableCell class="font-mono text-xs text-muted-foreground">{{ p.base_url }}</TableCell>
              <TableCell>
                <Badge v-if="p.has_api_key" variant="secondary">{{ t('admin.llm.apiKeySet') }}</Badge>
                <Badge v-else variant="destructive">{{ t('admin.llm.apiKeyMissing') }}</Badge>
              </TableCell>
              <TableCell>
                <div class="flex flex-wrap gap-1">
                  <Badge v-for="m in p.allowed_models" :key="m" variant="outline" class="text-xs font-mono">{{ m }}</Badge>
                </div>
              </TableCell>
              <TableCell class="space-x-2 text-right">
                <Button size="sm" variant="outline" @click="editing = p">
                  <Pencil class="size-3" />
                  {{ t('common.edit') }}
                </Button>
                <Button size="sm" variant="destructive" @click="onDelete(p)">
                  <Trash2 class="size-3" />
                  {{ t('common.delete') }}
                </Button>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>

        <p v-if="loading" class="mt-3 text-sm text-muted-foreground">{{ t('common.loading') }}</p>
      </CardContent>
    </Card>

    <!-- Create dialog -->
    <Dialog v-model:open="createOpen">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{{ t('admin.llm.createTitle') }}</DialogTitle>
          <DialogDescription>{{ t('admin.llm.createSubtitle') }}</DialogDescription>
        </DialogHeader>
        <Form v-slot="{ isSubmitting, values, setFieldValue }" :validation-schema="createSchema" :initial-values="createInitial" keep-values @submit="onCreate">
          <div class="space-y-4">
            <FormField v-slot="{ componentField }" name="name">
              <FormItem>
                <FormLabel>{{ t('admin.llm.fields.name') }}</FormLabel>
                <FormControl><Input v-bind="componentField" autocomplete="off" /></FormControl>
                <p class="text-xs text-muted-foreground">{{ t('admin.llm.fields.nameHint') }}</p>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField name="type">
              <FormItem>
                <FormLabel>{{ t('admin.llm.fields.type') }}</FormLabel>
                <FormControl>
                  <Select :model-value="values.type" @update:model-value="(v) => setFieldValue('type', v as ProviderType)">
                    <SelectTrigger><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem v-for="ty in PROVIDER_TYPES" :key="ty" :value="ty">{{ ty }}</SelectItem>
                    </SelectContent>
                  </Select>
                </FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField v-slot="{ componentField }" name="base_url">
              <FormItem>
                <FormLabel>{{ t('admin.llm.fields.baseUrl') }}</FormLabel>
                <FormControl><Input v-bind="componentField" placeholder="https://api.example.com/v1" /></FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField v-slot="{ componentField }" name="api_key">
              <FormItem>
                <FormLabel>{{ t('admin.llm.fields.apiKey') }}</FormLabel>
                <FormControl><Input type="password" autocomplete="off" v-bind="componentField" /></FormControl>
                <p class="text-xs text-muted-foreground">{{ t('admin.llm.fields.apiKeyHint') }}</p>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField v-slot="{ componentField }" name="allowed_models">
              <FormItem>
                <FormLabel>{{ t('admin.llm.fields.allowedModels') }}</FormLabel>
                <FormControl><Textarea rows="3" v-bind="componentField" placeholder="gpt-4o, claude-sonnet-4-6" /></FormControl>
                <p class="text-xs text-muted-foreground">{{ t('admin.llm.fields.allowedModelsHint') }}</p>
                <FormMessage />
              </FormItem>
            </FormField>

            <p v-if="createError" class="text-sm text-destructive">{{ createError }}</p>
          </div>
          <DialogFooter class="mt-6">
            <Button type="button" variant="outline" @click="createOpen = false">{{ t('common.cancel') }}</Button>
            <Button type="submit" :disabled="isSubmitting">{{ isSubmitting ? t('common.submitting') : t('common.submit') }}</Button>
          </DialogFooter>
        </Form>
      </DialogContent>
    </Dialog>

    <!-- Edit dialog -->
    <Dialog v-model:open="editOpen">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{{ t('admin.llm.editTitle', { name: editing?.name }) }}</DialogTitle>
          <DialogDescription>{{ t('admin.llm.editSubtitle') }}</DialogDescription>
        </DialogHeader>
        <Form v-if="editing" v-slot="{ isSubmitting }" :validation-schema="editSchema" :initial-values="editInitial" keep-values @submit="onEdit">
          <div class="space-y-4">
            <FormField v-slot="{ componentField }" name="base_url">
              <FormItem>
                <FormLabel>{{ t('admin.llm.fields.baseUrl') }}</FormLabel>
                <FormControl><Input v-bind="componentField" /></FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField v-slot="{ componentField }" name="api_key">
              <FormItem>
                <FormLabel>{{ t('admin.llm.fields.apiKey') }}</FormLabel>
                <FormControl><Input type="password" autocomplete="off" v-bind="componentField" /></FormControl>
                <p class="text-xs text-muted-foreground flex items-start gap-1">
                  <AlertTriangle class="mt-0.5 size-3 shrink-0 text-amber-500" />
                  {{ t('admin.llm.fields.apiKeyEditHint') }}
                </p>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField v-slot="{ componentField }" name="allowed_models">
              <FormItem>
                <FormLabel>{{ t('admin.llm.fields.allowedModels') }}</FormLabel>
                <FormControl><Textarea rows="3" v-bind="componentField" /></FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <p v-if="editError" class="text-sm text-destructive">{{ editError }}</p>
          </div>
          <DialogFooter class="mt-6">
            <Button type="button" variant="outline" @click="editing = null">{{ t('common.cancel') }}</Button>
            <Button type="submit" :disabled="isSubmitting">{{ isSubmitting ? t('common.submitting') : t('common.save') }}</Button>
          </DialogFooter>
        </Form>
      </DialogContent>
    </Dialog>
  </div>
</template>
