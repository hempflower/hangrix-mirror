<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'
import { AlertTriangle, Bot, Check, Copy, Download, Plus, Trash2, X } from 'lucide-vue-next'

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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

import type { Runner, RunnerCreateResp, RunnerListResp } from '~/types/runner'

definePageMeta({ layout: 'admin' })

const { t } = useI18n()

setBreadcrumbs(() => [
  { label: t('admin.section'), to: '/admin/runners' },
  { label: t('admin.runners.title') },
])

const runners = ref<Runner[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

const createOpen = ref(false)
const createError = ref<string | null>(null)

const enrollOpen = ref(false)
const enrollToken = ref('')
const enrollRunner = ref<Runner | null>(null)
const enrollCopied = ref(false)

const schema = computed(() => toTypedSchema(z.object({
  name: z.string().min(1).max(64).regex(/^[a-z0-9][a-z0-9-]{0,63}$/),
})))
const initial = { name: '' }

// Server origin so the install one-liner targets the same host the
// admin browser is talking to. Falls back to `<server-url>` placeholder
// during SSR / when window is unavailable.
const serverOrigin = computed(() => {
  if (typeof window === 'undefined') return '<server-url>'
  return window.location.origin
})

// One-shot curl|sh installer — fetches /install/runner.sh, pipes to sh,
// passes the enroll token as a positional arg.
const installOneLiner = computed(() => {
  if (!enrollToken.value) return ''
  return `curl -fsSL ${serverOrigin.value}/install/runner.sh | sh -s -- ${enrollToken.value}`
})

const scriptURL = computed(() => `${serverOrigin.value}/install/runner.sh`)
const binaryURL = computed(() => `${serverOrigin.value}/install/hangrix-runner`)

// Manual enroll command, for operators who already have hangrix-runner
// on PATH (e.g. installed via package manager / built from source).
const manualEnroll = computed(() => {
  if (!enrollToken.value) return ''
  return `hangrix-runner enroll --server ${serverOrigin.value} --token ${enrollToken.value}`
})

function formatDate(s?: string | null) {
  if (!s) return ''
  try { return new Date(s).toLocaleString() } catch { return s }
}

function statusVariant(s: string) {
  if (s === 'active') return 'secondary'
  if (s === 'disabled') return 'destructive'
  return 'outline'
}

async function load() {
  loading.value = true
  error.value = null
  try {
    const res = await $fetch<RunnerListResp>('/api/admin/runners', { credentials: 'include' })
    runners.value = res.items ?? []
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.runners.loadFailed')
  } finally {
    loading.value = false
  }
}

async function onCreate(values: any, ctx: any) {
  createError.value = null
  try {
    const res = await $fetch<RunnerCreateResp>('/api/admin/runners', {
      method: 'POST',
      credentials: 'include',
      body: { name: values.name },
    })
    enrollToken.value = res.enroll_token
    enrollRunner.value = res.runner
    enrollCopied.value = false
    createOpen.value = false
    enrollOpen.value = true
    ctx?.resetForm?.({ values: initial })
    await load()
  } catch (e: any) {
    createError.value = e?.data?.error ?? t('admin.runners.createFailed')
  }
}

async function onDisable(r: Runner) {
  // eslint-disable-next-line no-alert
  if (!window.confirm(t('admin.runners.disableConfirm', { name: r.name }))) return
  try {
    await $fetch(`/api/admin/runners/${r.id}`, { method: 'DELETE', credentials: 'include' })
    await load()
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.runners.disableFailed')
  }
}

async function onRemove(r: Runner) {
  // eslint-disable-next-line no-alert
  if (!window.confirm(t('admin.runners.removeConfirm', { name: r.name }))) return
  try {
    await $fetch(`/api/admin/runners/${r.id}/permanent`, { method: 'DELETE', credentials: 'include' })
    await load()
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.runners.removeFailed')
  }
}

async function copyEnroll() {
  try {
    await navigator.clipboard.writeText(enrollToken.value)
    enrollCopied.value = true
    setTimeout(() => { enrollCopied.value = false }, 1500)
  } catch { /* ignore */ }
}

const oneLinerCopied = ref(false)
async function copyOneLiner() {
  try {
    await navigator.clipboard.writeText(installOneLiner.value)
    oneLinerCopied.value = true
    setTimeout(() => { oneLinerCopied.value = false }, 1500)
  } catch { /* ignore */ }
}

function acknowledge() {
  enrollOpen.value = false
  enrollToken.value = ''
  enrollRunner.value = null
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <header class="flex items-start justify-between gap-4">
      <div class="space-y-1">
        <h1 class="text-2xl font-semibold tracking-tight">{{ t('admin.runners.title') }}</h1>
        <p class="text-sm text-muted-foreground">{{ t('admin.runners.subtitle') }}</p>
      </div>
      <Button @click="createOpen = true">
        <Plus class="size-4" />
        {{ t('admin.runners.create') }}
      </Button>
    </header>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('admin.runners.cardTitle') }}</CardTitle>
        <CardDescription>{{ t('admin.runners.cardDescription') }}</CardDescription>
      </CardHeader>
      <CardContent>
        <p v-if="error" class="mb-3 text-sm text-destructive">{{ error }}</p>

        <div v-if="!loading && runners.length === 0" class="rounded-lg border border-dashed p-8 text-center">
          <Bot class="mx-auto size-8 text-muted-foreground" />
          <p class="mt-3 text-sm font-medium">{{ t('admin.runners.empty') }}</p>
          <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.runners.emptyHint') }}</p>
        </div>

        <Table v-else>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t('admin.runners.cols.name') }}</TableHead>
              <TableHead>{{ t('admin.runners.cols.visibility') }}</TableHead>
              <TableHead>{{ t('admin.runners.cols.status') }}</TableHead>
              <TableHead>{{ t('admin.runners.cols.enroll') }}</TableHead>
              <TableHead>{{ t('admin.runners.cols.lastHeartbeat') }}</TableHead>
              <TableHead class="text-right">{{ t('common.actions') }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="r in runners" :key="r.id">
              <TableCell class="font-medium">{{ r.name }}</TableCell>
              <TableCell><Badge variant="outline">{{ r.visibility }}</Badge></TableCell>
              <TableCell><Badge :variant="statusVariant(r.status)">{{ r.status }}</Badge></TableCell>
              <TableCell>
                <Badge v-if="r.enroll_token_used" variant="secondary">{{ t('admin.runners.enrolled') }}</Badge>
                <Badge v-else variant="outline">{{ t('admin.runners.notEnrolled') }}</Badge>
              </TableCell>
              <TableCell class="text-xs text-muted-foreground">{{ formatDate(r.last_heartbeat_at) || t('admin.runners.neverHeartbeat') }}</TableCell>
              <TableCell class="space-x-2 text-right">
                <Button
                  v-if="r.status !== 'disabled'"
                  size="sm"
                  variant="outline"
                  @click="onDisable(r)"
                >
                  <Trash2 class="size-3" />
                  {{ t('admin.runners.disable') }}
                </Button>
                <Button
                  size="sm"
                  variant="destructive"
                  @click="onRemove(r)"
                >
                  <X class="size-3" />
                  {{ t('admin.runners.remove') }}
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
          <DialogTitle>{{ t('admin.runners.createTitle') }}</DialogTitle>
          <DialogDescription>{{ t('admin.runners.createSubtitle') }}</DialogDescription>
        </DialogHeader>
        <Form v-slot="{ isSubmitting }" :validation-schema="schema" :initial-values="initial" keep-values @submit="onCreate">
          <div class="space-y-4">
            <FormField v-slot="{ componentField }" name="name">
              <FormItem>
                <FormLabel>{{ t('admin.runners.fields.name') }}</FormLabel>
                <FormControl><Input autocomplete="off" v-bind="componentField" /></FormControl>
                <p class="text-xs text-muted-foreground">{{ t('admin.runners.fields.nameHint') }}</p>
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

    <!-- Enroll-token one-shot dialog -->
    <Dialog v-model:open="enrollOpen" @update:open="(v) => { if (!v) acknowledge() }">
      <DialogContent :show-close-button="false">
        <DialogHeader>
          <DialogTitle class="flex items-center gap-2">
            <AlertTriangle class="size-5 text-amber-500" />
            {{ t('admin.runners.enrollTitle') }}
          </DialogTitle>
          <DialogDescription class="text-amber-600 dark:text-amber-400">
            {{ t('admin.runners.enrollWarning') }}
          </DialogDescription>
        </DialogHeader>
        <div class="space-y-4">
          <p v-if="enrollRunner" class="text-sm">
            {{ t('admin.runners.runnerCreated', { name: enrollRunner.name }) }}
          </p>

          <!-- Section 1: token itself (in case operator wants to paste it elsewhere) -->
          <div>
            <p class="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
              {{ t('admin.runners.enrollToken') }}
            </p>
            <div class="rounded-md border bg-muted/30 p-3">
              <code class="block break-all font-mono text-sm">{{ enrollToken }}</code>
            </div>
            <Button class="mt-2 w-full" size="sm" variant="outline" @click="copyEnroll">
              <component :is="enrollCopied ? Check : Copy" class="size-3.5" />
              {{ enrollCopied ? t('admin.runners.copied') : t('admin.runners.copyToken') }}
            </Button>
          </div>

          <!-- Section 2: one-line install (the recommended path) -->
          <div>
            <p class="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
              {{ t('admin.runners.installOneLiner') }}
            </p>
            <p class="mb-2 text-xs text-muted-foreground">{{ t('admin.runners.installHint') }}</p>
            <div class="rounded-md border bg-muted/30 p-3">
              <code class="block break-all font-mono text-xs">{{ installOneLiner }}</code>
            </div>
            <Button class="mt-2 w-full" size="sm" variant="outline" @click="copyOneLiner">
              <component :is="oneLinerCopied ? Check : Copy" class="size-3.5" />
              {{ oneLinerCopied ? t('admin.runners.copied') : t('admin.runners.copyInstall') }}
            </Button>
          </div>

          <!-- Section 3: download links (alt path: inspect script, manual install) -->
          <div class="rounded-md border bg-muted/10 p-3 text-xs">
            <p class="mb-2 font-medium text-muted-foreground">{{ t('admin.runners.directDownloads') }}</p>
            <div class="space-y-1.5">
              <a
                :href="scriptURL"
                target="_blank"
                rel="noopener"
                class="flex items-center gap-1.5 font-mono text-primary hover:underline"
              >
                <Download class="size-3" />
                {{ scriptURL }}
              </a>
              <a
                :href="binaryURL"
                target="_blank"
                rel="noopener"
                class="flex items-center gap-1.5 font-mono text-primary hover:underline"
              >
                <Download class="size-3" />
                {{ binaryURL }}
              </a>
            </div>
          </div>

          <!-- Section 4: manual enroll, for operators with the binary already -->
          <details class="rounded-md border bg-muted/10 p-3 text-xs">
            <summary class="cursor-pointer font-medium text-muted-foreground">
              {{ t('admin.runners.manualHeader') }}
            </summary>
            <p class="mt-2 text-muted-foreground">{{ t('admin.runners.manualHint') }}</p>
            <div class="mt-2 rounded-md border bg-muted/30 p-2">
              <code class="block break-all font-mono">{{ manualEnroll }}</code>
            </div>
          </details>
        </div>
        <DialogFooter>
          <Button @click="acknowledge">{{ t('common.acknowledge') }}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
