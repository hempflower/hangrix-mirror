<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Clock, Play, Plus, RotateCcw, Trash2 } from 'lucide-vue-next'
import YAML from 'yaml'

import type { AutomationConfig, AutomationRun, AutomationTask } from '~/types/automation'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'

const props = defineProps<{
  owner: string
  name: string
}>()

const { t } = useI18n()

const config = ref<AutomationConfig | null>(null)
const loading = ref(false)
const loadError = ref<string | null>(null)
const saveMsg = ref<{ kind: 'ok' | 'err'; text: string } | null>(null)
const saving = ref(false)

// --- Dialog state ---
const dialogOpen = ref(false)
const editIndex = ref<number | null>(null)
const form = ref({
  name: '',
  schedule: '',
  title: '',
  body: '',
  labels: '',
  roles: '',
  enabled: true,
})
const formError = ref<string | null>(null)

function resetForm() {
  form.value = { name: '', schedule: '', title: '', body: '', labels: '', roles: '', enabled: true }
  editIndex.value = null
  formError.value = null
}

function openCreate() {
  resetForm()
  dialogOpen.value = true
}

function openEdit(index: number) {
  if (!config.value) return
  const task = config.value.tasks[index]
  if (!task) return
  form.value = {
    name: task.name,
    schedule: task.schedule,
    title: task.issue.title,
    body: task.issue.body,
    labels: (task.issue.labels ?? []).join(', '),
    roles: task.roles.join(', '),
    enabled: task.enabled,
  }
  editIndex.value = index
  formError.value = null
  dialogOpen.value = true
}

// --- Load ---
async function load() {
  loading.value = true
  loadError.value = null
  try {
    config.value = await $fetch<AutomationConfig>(
      `/api/repos/${props.owner}/${props.name}/automation`,
      { credentials: 'include' },
    )
  } catch (e: any) {
    loadError.value = e?.data?.error ?? t('repo.automation.loadFailed')
    config.value = null
  } finally {
    loading.value = false
  }
}

// --- Save ---
async function save() {
  if (!config.value) return
  saveMsg.value = null
  saving.value = true

  const tasksObj = config.value.tasks.map((t) => ({
    name: t.name,
    schedule: t.schedule,
    issue: {
      title: t.issue.title,
      body: t.issue.body,
      labels: t.issue.labels ?? [],
    },
    roles: t.roles,
    enabled: t.enabled,
  }))

  const yamlStr = YAML.stringify({ version: 1, tasks: tasksObj })

  try {
    config.value = await $fetch<AutomationConfig>(
      `/api/repos/${props.owner}/${props.name}/automation`,
      {
        method: 'PUT',
        credentials: 'include',
        body: yamlStr,
      },
    )
    saveMsg.value = { kind: 'ok', text: t('repo.automation.saved') }
  } catch (e: any) {
    saveMsg.value = { kind: 'err', text: e?.data?.error ?? t('repo.automation.saveFailed') }
  } finally {
    saving.value = false
  }
}

// --- Delete task ---
async function deleteTask(index: number) {
  if (!config.value) return
  const task = config.value.tasks[index]
  if (!task) return
  if (!window.confirm(t('repo.automation.deleteConfirm', { name: task.name }))) return
  config.value.tasks.splice(index, 1)
  await save()
}

// --- Toggle enabled ---
async function toggleEnabled(index: number) {
  if (!config.value) return
  const task = config.value.tasks[index]
  if (!task) return
  task.enabled = !task.enabled
  await save()
}

// --- Trigger ---
async function triggerTask(taskName: string) {
  try {
    await $fetch(`/api/repos/${props.owner}/${props.name}/automation/${encodeURIComponent(taskName)}/trigger`, {
      method: 'POST',
      credentials: 'include',
    })
    await load()
  } catch (e: any) {
    // eslint-disable-next-line no-alert
    window.alert(e?.data?.error ?? t('repo.automation.triggerFailed'))
  }
}

// --- Form submit ---
function onSubmit() {
  formError.value = null

  const name = form.value.name.trim()
  if (!name || !/^[a-z][a-z0-9-]*$/.test(name)) {
    formError.value = t('repo.automation.nameInvalid')
    return
  }

  const schedule = form.value.schedule.trim()
  if (!schedule) {
    formError.value = t('repo.automation.scheduleRequired')
    return
  }

  const title = form.value.title.trim()
  if (!title || title.length > 200) {
    formError.value = t('repo.automation.titleInvalid')
    return
  }

  const roles = form.value.roles
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
  if (roles.length === 0) {
    formError.value = t('repo.automation.rolesRequired')
    return
  }

  const labels = form.value.labels
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)

  const task: AutomationTask = {
    name,
    schedule,
    issue: {
      title,
      body: form.value.body,
      labels,
    },
    roles,
    enabled: form.value.enabled,
  }

  if (!config.value) {
    config.value = { tasks: [], runs: [] }
  }

  if (editIndex.value !== null) {
    config.value.tasks[editIndex.value] = task
  } else {
    // Check for duplicate name
    if (config.value.tasks.some((t) => t.name === name)) {
      formError.value = t('repo.automation.nameDuplicate')
      return
    }
    config.value.tasks.push(task)
  }

  dialogOpen.value = false
  resetForm()
  save()
}

// --- Helpers ---
function roleBadgeVariant(_role: string, _index: number) {
  return 'secondary' as const
}

function runStatusVariant(status: string) {
  switch (status) {
    case 'success': return 'default' as const
    case 'failed': return 'destructive' as const
    default: return 'outline' as const
  }
}

function formatTime(ts: string) {
  if (!ts) return '—'
  try {
    return new Date(ts).toLocaleString()
  } catch {
    return ts
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <!-- Task list -->
    <Card>
      <CardHeader>
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="space-y-1">
            <CardTitle class="flex items-center gap-2">
              <Clock class="size-5" />
              {{ t('repo.automation.tasksTitle') }}
            </CardTitle>
            <CardDescription>{{ t('repo.automation.tasksSubtitle') }}</CardDescription>
          </div>
          <Button size="sm" @click="openCreate">
            <Plus class="size-4" />
            {{ t('repo.automation.createTask') }}
          </Button>
        </div>
      </CardHeader>
      <CardContent class="space-y-3">
        <p v-if="loadError" class="text-sm text-destructive">{{ loadError }}</p>
        <p v-else-if="loading" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>

        <template v-else-if="config">
          <p v-if="config.tasks.length === 0" class="text-sm text-muted-foreground">
            {{ t('repo.automation.empty') }}
          </p>
          <Table v-else>
            <TableHeader>
              <TableRow>
                <TableHead>{{ t('repo.automation.colName') }}</TableHead>
                <TableHead>{{ t('repo.automation.colSchedule') }}</TableHead>
                <TableHead>{{ t('repo.automation.colRoles') }}</TableHead>
                <TableHead class="text-center">{{ t('repo.automation.colEnabled') }}</TableHead>
                <TableHead class="text-right">{{ t('common.actions') }}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="(task, idx) in config.tasks" :key="task.name">
                <TableCell class="font-medium">{{ task.name }}</TableCell>
                <TableCell class="font-mono text-sm">{{ task.schedule }}</TableCell>
                <TableCell>
                  <div class="flex flex-wrap gap-1">
                    <Badge
                      v-for="(role, ri) in task.roles"
                      :key="ri"
                      :variant="roleBadgeVariant(role, ri)"
                      class="text-xs"
                    >
                      {{ role }}
                    </Badge>
                  </div>
                </TableCell>
                <TableCell class="text-center">
                  <Checkbox
                    :model-value="task.enabled"
                    @update:model-value="() => toggleEnabled(idx)"
                  />
                </TableCell>
                <TableCell class="text-right">
                  <div class="inline-flex items-center gap-1">
                    <Button size="sm" variant="outline" @click="openEdit(idx)">
                      {{ t('common.edit') }}
                    </Button>
                    <Button size="sm" variant="outline" @click="triggerTask(task.name)">
                      <Play class="size-3" />
                      <span class="ml-1">{{ t('repo.automation.trigger') }}</span>
                    </Button>
                    <Button size="sm" variant="outline" @click="deleteTask(idx)">
                      <Trash2 class="size-3" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </template>
      </CardContent>
    </Card>

    <!-- Recent runs -->
    <Card v-if="config && config.runs.length > 0">
      <CardHeader>
        <CardTitle class="flex items-center gap-2">
          <RotateCcw class="size-5" />
          {{ t('repo.automation.runsTitle') }}
        </CardTitle>
        <CardDescription>{{ t('repo.automation.runsSubtitle') }}</CardDescription>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t('repo.automation.colTask') }}</TableHead>
              <TableHead>{{ t('common.status') }}</TableHead>
              <TableHead>{{ t('repo.automation.colIssue') }}</TableHead>
              <TableHead>{{ t('repo.automation.colStarted') }}</TableHead>
              <TableHead>{{ t('repo.automation.colFinished') }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="run in config.runs" :key="run.id">
              <TableCell class="font-medium">{{ run.task_name }}</TableCell>
              <TableCell>
                <Badge :variant="runStatusVariant(run.status)">
                  {{ run.status }}
                </Badge>
                <span v-if="run.error_message" class="ml-2 text-xs text-muted-foreground">
                  {{ run.error_message }}
                </span>
              </TableCell>
              <TableCell>
                <NuxtLink
                  v-if="run.issue_id && run.issue_number"
                  :to="`/${owner}/${name}/issues/${run.issue_number}`"
                  class="text-primary hover:underline"
                >
                  #{{ run.issue_number }}
                </NuxtLink>
                <span v-else-if="run.issue_id" class="text-sm">
                  #{{ run.issue_id }} (DB)
                </span>
                <span v-else class="text-muted-foreground">—</span>
              </TableCell>
              <TableCell class="text-sm">{{ formatTime(run.started_at) }}</TableCell>
              <TableCell class="text-sm">{{ formatTime(run.finished_at ?? '') }}</TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </CardContent>
    </Card>

    <!-- Save feedback -->
    <p v-if="saveMsg" :class="saveMsg.kind === 'ok' ? 'text-sm text-emerald-500' : 'text-sm text-destructive'">
      {{ saveMsg.text }}
    </p>

    <!-- Create/Edit dialog -->
    <Dialog v-model:open="dialogOpen">
      <DialogContent class="max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {{ editIndex !== null ? t('repo.automation.editTask') : t('repo.automation.createTask') }}
          </DialogTitle>
          <DialogDescription>
            {{ t('repo.automation.taskFormSubtitle') }}
          </DialogDescription>
        </DialogHeader>
        <div class="space-y-4">
          <div class="space-y-2">
            <Label for="auto-name">{{ t('repo.automation.fieldName') }}</Label>
            <Input id="auto-name" v-model="form.name" autocomplete="off" placeholder="daily-check" />
            <p class="text-xs text-muted-foreground">{{ t('repo.automation.fieldNameHint') }}</p>
          </div>
          <div class="space-y-2">
            <Label for="auto-schedule">{{ t('repo.automation.fieldSchedule') }}</Label>
            <Input id="auto-schedule" v-model="form.schedule" autocomplete="off" placeholder="0 8 * * 1" />
            <p class="text-xs text-muted-foreground">{{ t('repo.automation.fieldScheduleHint') }}</p>
          </div>
          <div class="space-y-2">
            <Label for="auto-title">{{ t('repo.automation.fieldIssueTitle') }}</Label>
            <Input id="auto-title" v-model="form.title" autocomplete="off" :placeholder="t('repo.automation.fieldIssueTitlePlaceholder')" />
          </div>
          <div class="space-y-2">
            <Label for="auto-body">{{ t('repo.automation.fieldIssueBody') }}</Label>
            <Textarea id="auto-body" v-model="form.body" :rows="4" :placeholder="t('repo.automation.fieldIssueBodyPlaceholder')" />
          </div>
          <div class="space-y-2">
            <Label for="auto-labels">{{ t('repo.automation.fieldLabels') }}</Label>
            <Input id="auto-labels" v-model="form.labels" autocomplete="off" placeholder="security, automated" />
            <p class="text-xs text-muted-foreground">{{ t('repo.automation.fieldLabelsHint') }}</p>
          </div>
          <div class="space-y-2">
            <Label for="auto-roles">{{ t('repo.automation.fieldRoles') }}</Label>
            <Input id="auto-roles" v-model="form.roles" autocomplete="off" placeholder="implementer, reviewer" />
            <p class="text-xs text-muted-foreground">{{ t('repo.automation.fieldRolesHint') }}</p>
          </div>
          <div class="flex items-center gap-2">
            <Checkbox id="auto-enabled" v-model="form.enabled" />
            <Label for="auto-enabled">{{ t('repo.automation.fieldEnabled') }}</Label>
          </div>
          <p v-if="formError" class="text-sm text-destructive">{{ formError }}</p>
        </div>
        <DialogFooter>
          <Button variant="outline" @click="dialogOpen = false">
            {{ t('common.cancel') }}
          </Button>
          <Button @click="onSubmit">
            {{ editIndex !== null ? t('repo.automation.updateTask') : t('repo.automation.createTask') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
