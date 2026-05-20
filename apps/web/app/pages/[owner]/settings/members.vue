<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Plus, Trash2 } from 'lucide-vue-next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
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
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
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
import type { MemberListResp, PublicMember, PublicOrg } from '~/types/org'

const { t } = useI18n()
const route = useRoute()

setBreadcrumbs(() => {
  const owner = String(route.params.owner ?? '')
  return [
    { label: owner, to: `/${owner}` },
    { label: t('repo.settingsLink'), to: `/${owner}/settings` },
    { label: t('org.members.title') },
  ]
})
const router = useRouter()
const { user, refresh: refreshUser } = useCurrentUser()

const orgName = computed(() => String(route.params.owner ?? ''))
useHead({ title: () => `${orgName.value} · ${t('org.members.title')} - ${t('app.name')}` })

const org = ref<PublicOrg | null>(null)
const members = ref<PublicMember[]>([])
const loading = ref(false)
const loadError = ref<string | null>(null)

const addOpen = ref(false)
const addUsername = ref('')
const addRole = ref<'owner' | 'member'>('member')
const addError = ref<string | null>(null)
const adding = ref(false)

const ownerCount = computed(() => members.value.filter(m => m.role === 'owner').length)

const isAdmin = computed(() => user.value?.role === 'admin')
const isOrgOwner = computed(() => {
  if (!user.value) return false
  return members.value.some(m => m.user_id === user.value!.id && m.role === 'owner')
})
const canManage = computed(() => isAdmin.value || isOrgOwner.value)

async function load() {
  if (!orgName.value) return
  loading.value = true
  loadError.value = null
  try {
    if (!user.value) await refreshUser()
    org.value = await $fetch<PublicOrg>(`/api/orgs/${orgName.value}`, { credentials: 'include' })
    const resp = await $fetch<MemberListResp>(`/api/orgs/${orgName.value}/members`, { credentials: 'include' })
    members.value = resp.items
  } catch (e: any) {
    loadError.value = e?.data?.error ?? t('org.loadFailed')
  } finally {
    loading.value = false
  }
}

async function onAdd() {
  if (!org.value) return
  addError.value = null
  adding.value = true
  try {
    await $fetch(`/api/orgs/${org.value.name}/members`, {
      method: 'POST',
      credentials: 'include',
      body: { username: addUsername.value.trim(), role: addRole.value },
    })
    addOpen.value = false
    addUsername.value = ''
    addRole.value = 'member'
    await load()
  } catch (e: any) {
    addError.value = e?.data?.error ?? t('org.members.addFailed')
  } finally {
    adding.value = false
  }
}

async function onRoleChange(member: PublicMember, newRole: 'owner' | 'member') {
  if (!org.value || newRole === member.role) return
  try {
    await $fetch(`/api/orgs/${org.value.name}/members/${member.username}`, {
      method: 'PATCH',
      credentials: 'include',
      body: { role: newRole },
    })
    await load()
  } catch (e: any) {
    loadError.value = e?.data?.error ?? t('org.members.roleFailed')
  }
}

async function onRemove(member: PublicMember) {
  if (!org.value) return
  try {
    await $fetch(`/api/orgs/${org.value.name}/members/${member.username}`, {
      method: 'DELETE',
      credentials: 'include',
    })
    if (user.value && member.user_id === user.value.id) {
      router.replace('/')
      return
    }
    await load()
  } catch (e: any) {
    loadError.value = e?.data?.error ?? t('org.members.removeFailed')
  }
}

function disableRoleSelect(m: PublicMember) {
  // Block demoting the last owner UI-side; backend would 409 anyway.
  return !canManage.value || (m.role === 'owner' && ownerCount.value <= 1)
}

function disableRemove(m: PublicMember) {
  // A user can always remove themselves; otherwise canManage gates it.
  if (user.value && m.user_id === user.value.id && m.role !== 'owner') return false
  if (m.role === 'owner' && ownerCount.value <= 1) return true
  return !canManage.value
}

watch(orgName, load)
onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ org?.display_name || org?.name || orgName }}
      </h1>
      <p class="text-sm text-muted-foreground">
        {{ t('org.members.subtitle') }}
      </p>
    </header>

    <p v-if="loadError" class="text-sm text-destructive">{{ loadError }}</p>

    <template v-if="org">
      <nav class="flex items-center gap-2 border-b">
        <NuxtLink
          :to="`/${org.name}/settings`"
          class="border-b-2 border-transparent px-3 py-2 text-sm text-muted-foreground hover:text-foreground"
        >
          {{ t('org.settings.general') }}
        </NuxtLink>
        <NuxtLink
          :to="`/${org.name}/settings/members`"
          class="border-b-2 border-primary px-3 py-2 text-sm font-medium"
        >
          {{ t('org.settings.members') }}
        </NuxtLink>
      </nav>

      <Card>
        <CardHeader class="flex flex-row items-center justify-between gap-2">
          <CardTitle>{{ t('org.members.title') }} ({{ members.length }})</CardTitle>
          <Button v-if="canManage" size="sm" @click="addOpen = true">
            <Plus class="size-4" />
            {{ t('org.members.add') }}
          </Button>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{{ t('common.username') }}</TableHead>
                <TableHead>{{ t('org.role') }}</TableHead>
                <TableHead>{{ t('org.addedAt') }}</TableHead>
                <TableHead class="w-12 text-right" />
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="m in members" :key="m.user_id">
                <TableCell>
                  <NuxtLink :to="`/${m.username}`" class="hover:underline">
                    {{ m.username }}
                  </NuxtLink>
                </TableCell>
                <TableCell>
                  <Select
                    :model-value="m.role"
                    :disabled="disableRoleSelect(m)"
                    @update:model-value="(v) => onRoleChange(m, v as 'owner' | 'member')"
                  >
                    <SelectTrigger class="w-32">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="owner">{{ t('org.roleOwner') }}</SelectItem>
                      <SelectItem value="member">{{ t('org.roleMember') }}</SelectItem>
                    </SelectContent>
                  </Select>
                </TableCell>
                <TableCell class="text-sm text-muted-foreground">
                  {{ new Date(m.added_at).toLocaleString() }}
                </TableCell>
                <TableCell class="text-right">
                  <Button
                    variant="ghost"
                    size="icon"
                    :disabled="disableRemove(m)"
                    @click="onRemove(m)"
                  >
                    <Trash2 class="size-4" />
                  </Button>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Dialog v-model:open="addOpen">
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{{ t('org.members.add') }}</DialogTitle>
            <DialogDescription>{{ t('org.members.addDescription') }}</DialogDescription>
          </DialogHeader>
          <div class="space-y-3">
            <div class="space-y-1">
              <Label for="add-username" class="text-sm">{{ t('common.username') }}</Label>
              <Input id="add-username" v-model="addUsername" autocomplete="off" />
            </div>
            <div class="space-y-2">
              <Label class="text-sm">{{ t('org.role') }}</Label>
              <RadioGroup :model-value="addRole" class="gap-2" @update:model-value="(v) => addRole = v as 'owner' | 'member'">
                <div class="flex items-center gap-2">
                  <RadioGroupItem id="role-member" value="member" />
                  <Label for="role-member" class="text-sm">{{ t('org.roleMember') }}</Label>
                </div>
                <div class="flex items-center gap-2">
                  <RadioGroupItem id="role-owner" value="owner" />
                  <Label for="role-owner" class="text-sm">{{ t('org.roleOwner') }}</Label>
                </div>
              </RadioGroup>
            </div>
            <p v-if="addError" class="text-sm text-destructive">{{ addError }}</p>
          </div>
          <DialogFooter>
            <Button variant="outline" @click="addOpen = false">{{ t('common.cancel') }}</Button>
            <Button :disabled="!addUsername.trim() || adding" @click="onAdd">
              {{ adding ? t('common.saving') : t('org.members.add') }}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </template>
  </div>
</template>
