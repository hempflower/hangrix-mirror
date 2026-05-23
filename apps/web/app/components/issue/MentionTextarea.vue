<script setup lang="ts">
// MentionTextarea wraps a plain <textarea> with an `@`-triggered
// autocomplete popover for agent roles. The dropdown opens once the
// caret is positioned after an `@` token that isn't otherwise glued to
// a word character on the left (`a@b` is an email-shape, not a
// mention); it stays open until the user accepts a suggestion, escapes
// out, or moves the caret away from the trigger.
//
// Suggestions are agent role keys from the host yaml. Picking one
// rewrites the `@<filter>` prefix to `@agent-<role-key> ` (with a
// trailing space so the user can keep typing).

import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { Bot } from 'lucide-vue-next'
import { cn } from '@/utils/utils'

interface MentionAgent {
  role_key: string
}

const props = withDefaults(
  defineProps<{
    modelValue: string
    suggestions: MentionAgent[]
    placeholder?: string
    rows?: number | string
    class?: string
  }>(),
  { rows: 8 },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()

const textareaRef = ref<HTMLTextAreaElement | null>(null)

// Mention trigger state. triggerStart is the index of the `@` that
// opened the dropdown (kept stable so we know what to replace on
// accept); filter is the substring between `@` and the caret.
const open = ref(false)
const triggerStart = ref(-1)
const filter = ref('')
const activeIndex = ref(0)

// Popover position. Computed once at open time and refreshed on caret
// movement; rendered as fixed-position relative to the viewport.
const popoverTop = ref(0)
const popoverLeft = ref(0)

const filtered = computed<MentionAgent[]>(() => {
  const q = filter.value.toLowerCase()
  // The mention grammar is `@agent-<key>`. Allow matching either by the
  // bare key ("backend") or by typing the `agent-` prefix
  // ("agent-backend") so users don't have to delete what they typed if
  // they were on autopilot.
  const stripped = q.startsWith('agent-') ? q.slice(6) : q
  if (!stripped) return props.suggestions.slice(0, 8)
  const matches = props.suggestions.filter((s) =>
    s.role_key.toLowerCase().includes(stripped),
  )
  return matches.slice(0, 8)
})

watch(filtered, () => {
  // Keep activeIndex in bounds whenever the candidate list narrows.
  if (activeIndex.value >= filtered.value.length) {
    activeIndex.value = Math.max(0, filtered.value.length - 1)
  }
})

function closeDropdown() {
  open.value = false
  triggerStart.value = -1
  filter.value = ''
  activeIndex.value = 0
}

// Detect whether the caret sits inside a fresh mention. We walk back
// from the caret looking for an unbroken run of role-key chars; the run
// must be preceded by `@`, and that `@` must itself be at the start of
// the string or after whitespace / punctuation (so we don't catch
// `user@host`).
function detectMention(value: string, caret: number): { start: number; filter: string } | null {
  if (caret <= 0) return null
  let i = caret - 1
  while (i >= 0) {
    const ch = value[i]!
    if (ch === '@') break
    if (!isMentionChar(ch)) return null
    i--
  }
  if (i < 0 || value[i] !== '@') return null
  const before = i > 0 ? value[i - 1]! : ''
  if (before && !isBoundaryChar(before)) return null
  return { start: i, filter: value.slice(i + 1, caret) }
}

function isMentionChar(ch: string) {
  return /[A-Za-z0-9_-]/.test(ch)
}

function isBoundaryChar(ch: string) {
  // Treat whitespace and common punctuation as a mention-eligible
  // boundary. Specifically excludes alphanumerics + `@` so `foo@bar`
  // and `me@@you` never trigger.
  return /[\s(\[{,;:!?'"`]/.test(ch)
}

function onInput(e: Event) {
  const el = e.target as HTMLTextAreaElement
  emit('update:modelValue', el.value)
  refreshMentionState(el)
}

function onKeydown(e: KeyboardEvent) {
  if (!open.value) return
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    if (filtered.value.length === 0) return
    activeIndex.value = (activeIndex.value + 1) % filtered.value.length
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    if (filtered.value.length === 0) return
    activeIndex.value =
      (activeIndex.value - 1 + filtered.value.length) % filtered.value.length
  } else if (e.key === 'Enter' || e.key === 'Tab') {
    const pick = filtered.value[activeIndex.value]
    if (pick) {
      e.preventDefault()
      acceptSuggestion(pick)
    }
  } else if (e.key === 'Escape') {
    e.preventDefault()
    closeDropdown()
  }
}

function onSelectionChange() {
  const el = textareaRef.value
  if (!el) return
  refreshMentionState(el)
}

function refreshMentionState(el: HTMLTextAreaElement) {
  const caret = el.selectionStart ?? 0
  const m = detectMention(el.value, caret)
  if (!m) {
    if (open.value) closeDropdown()
    return
  }
  triggerStart.value = m.start
  filter.value = m.filter
  if (!open.value) {
    open.value = true
    activeIndex.value = 0
  }
  void nextTick(() => positionPopover(el))
}

// Positioning uses a hidden mirror div trick: we duplicate the
// textarea's text + styling, place a sentinel <span> where the trigger
// `@` lives, and read its bounding box. This is the standard approach
// for caret-anchored popovers on textareas (textareas have no native
// per-character DOM positions).
let mirror: HTMLDivElement | null = null
function ensureMirror(): HTMLDivElement {
  if (mirror) return mirror
  mirror = document.createElement('div')
  mirror.style.position = 'fixed'
  mirror.style.top = '0'
  mirror.style.left = '0'
  mirror.style.visibility = 'hidden'
  mirror.style.whiteSpace = 'pre-wrap'
  mirror.style.wordWrap = 'break-word'
  mirror.style.overflow = 'hidden'
  mirror.style.pointerEvents = 'none'
  mirror.style.zIndex = '-1'
  document.body.appendChild(mirror)
  return mirror
}

const mirroredStyleProps = [
  'boxSizing', 'width', 'height', 'paddingTop', 'paddingRight',
  'paddingBottom', 'paddingLeft', 'borderTopWidth', 'borderRightWidth',
  'borderBottomWidth', 'borderLeftWidth', 'fontFamily', 'fontSize',
  'fontWeight', 'fontStyle', 'letterSpacing', 'lineHeight', 'textTransform',
  'tabSize',
] as const

function positionPopover(el: HTMLTextAreaElement) {
  const target = triggerStart.value
  if (target < 0) return
  const div = ensureMirror()
  const style = window.getComputedStyle(el)
  for (const prop of mirroredStyleProps) {
    // CSSStyleDeclaration is read-only on its index signature; assignment
    // by named property is the supported path.
    ;(div.style as any)[prop] = (style as any)[prop]
  }
  // Mirror the textarea content up to the trigger, then place a sentinel
  // at the trigger position itself. innerText would collapse multiple
  // spaces — textContent + whiteSpace:pre-wrap preserves them.
  const before = el.value.slice(0, target)
  const after = el.value.slice(target)
  div.textContent = before
  const span = document.createElement('span')
  span.textContent = after || '.'
  div.appendChild(span)
  const taRect = el.getBoundingClientRect()
  const spanRect = span.getBoundingClientRect()
  const lineHeight = parseFloat(style.lineHeight) || parseFloat(style.fontSize) * 1.2
  // The mirror is anchored at viewport (0, 0); we offset to the textarea's
  // position, then add the span's offset relative to the mirror, minus
  // the textarea's scroll so the popover follows the visible caret.
  popoverLeft.value = taRect.left + (spanRect.left - parseFloat(style.paddingLeft || '0')) - el.scrollLeft
  popoverTop.value = taRect.top + (spanRect.top - parseFloat(style.paddingTop || '0')) - el.scrollTop + lineHeight + 4
}

function acceptSuggestion(s: MentionAgent) {
  const el = textareaRef.value
  if (!el || triggerStart.value < 0) return
  const value = props.modelValue
  const caret = el.selectionStart ?? value.length
  const replaced = `@agent-${s.role_key} `
  const next = value.slice(0, triggerStart.value) + replaced + value.slice(caret)
  emit('update:modelValue', next)
  closeDropdown()
  void nextTick(() => {
    if (!textareaRef.value) return
    const pos = triggerStart.value + replaced.length
    textareaRef.value.focus()
    textareaRef.value.setSelectionRange(pos, pos)
  })
}

function insertAtCursor(text: string) {
  const el = textareaRef.value
  if (!el) return
  const caret = el.selectionStart ?? props.modelValue.length
  const next = props.modelValue.slice(0, caret) + text + props.modelValue.slice(caret)
  emit('update:modelValue', next)
  void nextTick(() => {
    if (!textareaRef.value) return
    const pos = caret + text.length
    textareaRef.value.focus()
    textareaRef.value.setSelectionRange(pos, pos)
  })
}

defineExpose({ insertAtCursor })


onBeforeUnmount(() => {
  if (mirror) {
    mirror.remove()
    mirror = null
  }
})
</script>

<template>
  <div class="relative">

    <textarea
      ref="textareaRef"
      :value="modelValue"
      :rows="rows"
      :placeholder="placeholder"
      :class="cn('border-input placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive dark:bg-input/30 flex field-sizing-content min-h-16 w-full rounded-md border bg-transparent px-3 py-2 text-base shadow-xs transition-[color,box-shadow] outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-50 md:text-sm', props.class)"
      @input="onInput"
      @keydown="onKeydown"
      @click="onSelectionChange"
      @keyup="onSelectionChange"
      @blur="closeDropdown"
    />
    <Teleport to="body">
      <div
        v-if="open && filtered.length > 0"
        class="z-50 max-h-64 w-64 overflow-y-auto rounded-md border bg-popover p-1 text-popover-foreground shadow-md"
        :style="{ position: 'fixed', top: `${popoverTop}px`, left: `${popoverLeft}px` }"
        @mousedown.prevent
      >
        <button
          v-for="(s, i) in filtered"
          :key="s.role_key"
          type="button"
          class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-accent hover:text-accent-foreground"
          :class="i === activeIndex ? 'bg-accent text-accent-foreground' : ''"
          @mouseenter="activeIndex = i"
          @mousedown.prevent="acceptSuggestion(s)"
        >
          <Bot class="size-4 shrink-0 text-muted-foreground" />
          <span class="font-mono text-xs">@agent-{{ s.role_key }}</span>
        </button>
      </div>
    </Teleport>
  </div>
</template>
