<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Eye, EyeOff, Key, Plus, Trash2 } from 'lucide-vue-next'

import type { RepoSecretMeta, RepoVariable, RepoVariableListResp, VariableKind } from '~/types/repo'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
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
  Select,
  SelectContent,
  SelectItem,
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

const props = defineProps<{
  owner: string
  name: string
}>()

const { t } = useI18n()

const variables = ref<RepoVariable[]>([])
const secrets = ref<RepoSecretMeta[]>([])
const loading = ref(false)
const loadError = ref<string | null>(null)

// --- Dialog state ---
const dialogOpen = ref(false)
const dialogMode = ref<'create' | 'edit'>('create')
const dialogTarget = ref<{ name: string; kind: VariableKind } | null>(null)
const form = ref({
  name: '',
  value: '',
  kind: 'plain' as VariableKind,
})
const formError = ref<string | null>(null)
const submitting = ref(false)

// --- Reveal tracking for plain variables ---
const revealed = ref<Set<string>>(new Set())

function toggleReveal(varName: string) {
  const next = new Set(revealed.value)
  if (next.has(varName)) {
    next.delete(varName)
  } else {
    next.add(varName)
  }
  revealed.value = next
}

// --- Load ---
async function load() {
  loading.value = true
  loadError.value = null
  try {
    const resp = await $fetch<RepoVariableListResp>(
      `/api/repos/${props.owner}/${props.name}/variables`,
      { credentials: 'include' },
    )
    variables.value = resp.variables ?? []
    secrets.value = resp.secrets ?? []
  } catch (e: any) {
    loadError.value = e?.data?.error ?? t('repo.variables.loadFailed')
    variables.value = []
    secrets.value = []
  } finally {
    loading.value = false
  }
}

function resetForm() {
  form.value = { name: '', value: '', kind: 'plain' }
  dialogTarget.value = null
  formError.value = null
  dialogMode.value = 'create'
}

function openCreate() {
  resetForm()
  dialogOpen.value = true
}

function openEdit(variable: RepoVariable) {
  resetForm()
  dialogMode.value = 'edit'
  dialogTarget.value = { name: variable.name, kind: 'plain' }
  form.value = { name: variable.name, value: variable.value, kind: 'plain' }
  dialogOpen.value = true
}

function openEditSecret(secret: RepoSecretMeta) {
  resetForm()
  dialogMode.value = 'edit'
  dialogTarget.value = { name: secret.name, kind: 'secret' }
  form.value = { name: secret.name, value: '', kind: 'secret' }
  dialogOpen.value = true
}

// --- Submit ---
async function onSubmit() {
  if (!form.value.name.trim()) {
    formError.value = t('repo.variables.nameRequired')
    return
  }
  if (!form.value.value.trim() && dialogMode.value === 'create') {
    formError.value = t('repo.variables.valueRequired')
    return
  }

  submitting.value = true
  formError.value = null

  try {
    if (dialogMode.value === 'create') {
      await $fetch(`/api/repos/${props.owner}/${props.name}/variables`, {
        method: 'POST',
        credentials: 'include',
        body: {
          name: form.value.name.trim(),
          value: form.value.value,
          kind: form.value.kind,
        },
      })
    } else if (dialogTarget.value) {
      const body: Record<string, string> = {}
      const newName = form.value.name.trim()
      // Only send changed fields
      if (newName !== dialogTarget.value.name) {
        body.name = newName
      }
      if (form.value.value.trim()) {
        body.value = form.value.value
      }
      if (dialogTarget.value.kind !== form.value.kind) {
        body.kind = form.value.kind
      }

      await $fetch(
        `/api/repos/${props.owner}/${props.name}/variables/${encodeURIComponent(dialogTarget.value.name)}`,
        {
          method: 'PATCH',
          credentials: 'include',
          body,
        },
      )
    }

    dialogOpen.value = false
    resetForm()
    await load()
  } catch (e: any) {
    formError.value = e?.data?.error ?? t('repo.variables.saveFailed')
  } finally {
    submitting.value = false
  }
}

// --- Delete ---
async function onDelete(name: string, kind: VariableKind) {
  // eslint-disable-next-line no-alert
  const label = kind === 'secret' ? t('repo.variables.secretLabel') : t('repo.variables.variableLabel')
  if (!window.confirm(t('repo.variables.deleteConfirm', { name, kind: label }))) return

  try {
    await $fetch(`/api/repos/${props.owner}/${props.name}/variables/${encodeURIComponent(name)}`, {
      method: 'DELETE',
      credentials: 'include',
    })
    await load()
  } catch (e: any) {
    loadError.value = e?.data?.error ?? t('repo.variables.deleteFailed')
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <p v-if="loadError" class="text-sm text-destructive">{{ loadError }}</p>

    <!-- Variables -->
    <Card>
      <CardHeader class="flex flex-row items-center justify-between gap-2">
        <div class="space-y-1">
          <CardTitle>{{ t('repo.variables.variablesTitle') }} ({{ variables.length }})</CardTitle>
          <CardDescription>{{ t('repo.variables.variablesSubtitle') }}</CardDescription>
        </div>
        <Button size="sm" @click="openCreate">
          <Plus class="size-4" />
          {{ t('repo.variables.create') }}
        </Button>
      </CardHeader>
      <CardContent>
        <p v-if="variables.length === 0 && !loading" class="text-sm text-muted-foreground">
          {{ t('repo.variables.noVariables') }}
        </p>
        <Table v-else>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t('repo.variables.colName') }}</TableHead>
              <TableHead>{{ t('repo.variables.colValue') }}</TableHead>
              <TableHead>{{ t('repo.variables.colUpdated') }}</TableHead>
              <TableHead class="w-12 text-right" />
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="v in variables" :key="v.name">
              <TableCell class="font-mono text-sm">{{ v.name }}</TableCell>
              <TableCell class="font-mono text-sm">
                <span v-if="revealed.has(v.name)">{{ v.value }}</span>
                <span v-else class="text-muted-foreground">••••••••</span>
              </TableCell>
              <TableCell class="text-sm text-muted-foreground">
                {{ new Date(v.updated_at).toLocaleString() }}
              </TableCell>
              <TableCell class="text-right">
                <div class="flex items-center justify-end gap-1">
                  <Button variant="ghost" size="icon" @click="toggleReveal(v.name)">
                    <Eye v-if="!revealed.has(v.name)" class="size-4" />
                    <EyeOff v-else class="size-4" />
                  </Button>
                  <Button variant="ghost" size="sm" @click="openEdit(v)">
                    {{ t('common.edit') }}
                  </Button>
                  <Button variant="ghost" size="icon" @click="onDelete(v.name, 'plain')">
                    <Trash2 class="size-4" />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </CardContent>
    </Card>

    <!-- Secrets -->
    <Card>
      <CardHeader>
        <div class="space-y-1">
          <CardTitle class="flex items-center gap-2">
            <Key class="size-5" />
            {{ t('repo.variables.secretsTitle') }} ({{ secrets.length }})
          </CardTitle>
          <CardDescription>{{ t('repo.variables.secretsSubtitle') }}</CardDescription>
        </div>
      </CardHeader>
      <CardContent>
        <p v-if="secrets.length === 0 && !loading" class="text-sm text-muted-foreground">
          {{ t('repo.variables.noSecrets') }}
        </p>
        <Table v-else>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t('repo.variables.colName') }}</TableHead>
              <TableHead>{{ t('repo.variables.colStatus') }}</TableHead>
              <TableHead>{{ t('repo.variables.colUpdated') }}</TableHead>
              <TableHead class="w-12 text-right" />
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="s in secrets" :key="s.name">
              <TableCell class="font-mono text-sm">{{ s.name }}</TableCell>
              <TableCell>
                <Badge variant="secondary">{{ t('repo.variables.secretSet') }}</Badge>
              </TableCell>
              <TableCell class="text-sm text-muted-foreground">
                {{ new Date(s.updated_at).toLocaleString() }}
              </TableCell>
              <TableCell class="text-right">
                <div class="flex items-center justify-end gap-1">
                  <Button variant="ghost" size="sm" @click="openEditSecret(s)">
                    {{ t('common.edit') }}
                  </Button>
                  <Button variant="ghost" size="icon" @click="onDelete(s.name, 'secret')">
                    <Trash2 class="size-4" />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </CardContent>
    </Card>

    <!-- Hint card -->
    <Card>
      <CardHeader>
        <CardDescription class="text-xs">
          {{ t('repo.variables.hint') }}
        </CardDescription>
      </CardHeader>
    </Card>

    <!-- Create / Edit dialog -->
    <Dialog v-model:open="dialogOpen">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {{ dialogMode === 'create' ? t('repo.variables.create') : t('repo.variables.edit') }}
          </DialogTitle>
          <DialogDescription>
            {{ dialogMode === 'create' ? t('repo.variables.createDescription') : t('repo.variables.editDescription') }}
          </DialogDescription>
        </DialogHeader>
        <div class="space-y-4">
          <div class="space-y-2">
            <Label for="var-name">{{ t('repo.variables.colName') }}</Label>
            <Input
              id="var-name"
              v-model="form.name"
              autocomplete="off"
              :placeholder="t('repo.variables.namePlaceholder')"
            />
          </div>
          <div class="space-y-2">
            <Label for="var-value">{{ t('repo.variables.colValue') }}</Label>
            <Input
              id="var-value"
              v-model="form.value"
              autocomplete="off"
              :type="form.kind === 'secret' ? 'password' : 'text'"
              :placeholder="dialogMode === 'edit' && form.kind === 'secret' ? t('repo.variables.secretEditPlaceholder') : t('repo.variables.valuePlaceholder')"
            />
            <p v-if="dialogMode === 'edit' && form.kind === 'secret'" class="text-xs text-muted-foreground">
              {{ t('repo.variables.secretEditHint') }}
            </p>
          </div>
          <div class="space-y-2">
            <Label>{{ t('repo.variables.kind') }}</Label>
            <Select v-model="form.kind" :disabled="dialogMode === 'edit'">
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="plain">{{ t('repo.variables.kindPlain') }}</SelectItem>
                <SelectItem value="secret">{{ t('repo.variables.kindSecret') }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <p v-if="formError" class="text-sm text-destructive">{{ formError }}</p>
        </div>
        <DialogFooter>
          <Button variant="outline" @click="dialogOpen = false">
            {{ t('common.cancel') }}
          </Button>
          <Button :disabled="submitting" @click="onSubmit">
            {{ submitting ? t('common.saving') : (dialogMode === 'create' ? t('repo.variables.create') : t('common.save')) }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
