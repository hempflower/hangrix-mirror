<script setup lang="ts">
// CodeMirror-backed YAML editor. The wrapper keeps the API minimal:
// v-model carries the document, `minHeight` controls the visible
// area. Everything else (syntax highlighting, history, auto-indent)
// comes from `basicSetup` so callers don't need to know CodeMirror's
// extension surface.
//
// Theme: hand-rolled VS Code "Dark+" palette. The default
// defaultHighlightStyle that ships with basicSetup uses a high-
// contrast scheme on a transparent background — readable on white
// but illegible against this app's dark surfaces. We swap it for an
// editor + highlight pair that matches what users already expect
// from any VS Code-derived UI.
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { EditorView, basicSetup } from 'codemirror'
import { Compartment, EditorState } from '@codemirror/state'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { tags as t } from '@lezer/highlight'
import { yaml } from '@codemirror/lang-yaml'

const props = withDefaults(defineProps<{
  modelValue: string
  minHeight?: string
  // maxHeight caps the editor's visible area. Beyond this, the
  // .cm-scroller takes over so the page layout stays stable instead
  // of being pushed around by long pasted documents. Defaults to
  // minHeight so unspecified callers get a fixed-size box.
  maxHeight?: string
}>(), {
  minHeight: '20rem',
})

const effectiveMaxHeight = () => props.maxHeight ?? props.minHeight

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()

const hostEl = ref<HTMLDivElement | null>(null)
const view = shallowRef<EditorView | null>(null)

// updatingFromProp prevents the listener from echoing prop-driven
// dispatches back to v-model (would otherwise cause an infinite
// update cycle when the parent uses `agentsYAML.value = …` to reset).
let updatingFromProp = false

const themeCompartment = new Compartment()

// VS Code Dark+ palette. Field names follow the CodeMirror selectors
// they paint; comments reference the equivalent token in Dark+ so
// future colour tweaks stay grounded.
const VSCODE_DARK = {
  background: '#1e1e1e',
  foreground: '#d4d4d4',
  caret: '#aeafad',
  // Brighter than VS Code's own #264f78 — the official tone reads
  // almost invisible at this app's contrast. We trade fidelity for
  // legibility.
  selection: '#3a6ea5',
  selectionMatch: '#4a4d51',
  lineHighlight: '#2a2d2e',

  comment: '#6a9955',         // // line comments
  string: '#ce9178',          // "quoted" / 'plain' scalars
  number: '#b5cea8',          // 42 / 3.14
  atom: '#569cd6',            // true / false / null
  keyword: '#569cd6',          // structural keywords (rare in yaml)
  property: '#9cdcfe',         // mapping keys
  operator: '#d4d4d4',         // : - , …
  punctuation: '#d4d4d4',
}

function buildHighlightStyle() {
  return HighlightStyle.define([
    { tag: t.comment, color: VSCODE_DARK.comment, fontStyle: 'italic' },
    { tag: t.lineComment, color: VSCODE_DARK.comment, fontStyle: 'italic' },
    { tag: t.string, color: VSCODE_DARK.string },
    { tag: t.special(t.string), color: VSCODE_DARK.string },
    { tag: t.number, color: VSCODE_DARK.number },
    { tag: t.bool, color: VSCODE_DARK.atom },
    { tag: t.null, color: VSCODE_DARK.atom },
    { tag: t.atom, color: VSCODE_DARK.atom },
    { tag: t.keyword, color: VSCODE_DARK.keyword },
    // Mapping keys parse as either propertyName or definition(propertyName)
    // depending on the YAML grammar; cover both so they always paint.
    { tag: t.propertyName, color: VSCODE_DARK.property },
    { tag: t.definition(t.propertyName), color: VSCODE_DARK.property },
    { tag: t.operator, color: VSCODE_DARK.operator },
    { tag: t.punctuation, color: VSCODE_DARK.punctuation },
    { tag: t.separator, color: VSCODE_DARK.punctuation },
    { tag: t.meta, color: VSCODE_DARK.comment },
  ])
}

function buildTheme() {
  return EditorView.theme({
    '&': {
      height: '100%',
      minHeight: props.minHeight,
      maxHeight: effectiveMaxHeight(),
      fontSize: '13px',
      color: VSCODE_DARK.foreground,
      backgroundColor: VSCODE_DARK.background,
    },
    '.cm-scroller': {
      fontFamily:
        'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace',
      lineHeight: '1.5',
      overflow: 'auto',
    },
    '.cm-content': {
      caretColor: VSCODE_DARK.caret,
      color: VSCODE_DARK.foreground,
    },
    '.cm-activeLine': {
      backgroundColor: VSCODE_DARK.lineHighlight,
    },
    '.cm-cursor, .cm-dropCursor': {
      borderLeftColor: VSCODE_DARK.caret,
    },
    // The drawSelection extension from basicSetup paints its own
    // background colour with a fairly specific selector — match its
    // shape (and outrank with !important) so our colour actually
    // shows instead of the muted default.
    '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionLayer .cm-selectionBackground, .cm-content ::selection': {
      backgroundColor: VSCODE_DARK.selection + ' !important',
    },
    '.cm-selectionMatch': {
      backgroundColor: VSCODE_DARK.selectionMatch,
    },
    // basicSetup ships line numbers + a fold gutter. Hide the whole
    // gutter strip — we want the editor to read like a styled
    // textarea, not an IDE pane.
    '.cm-gutters': {
      display: 'none',
    },
  }, { dark: true })
}

onMounted(() => {
  if (!hostEl.value) return
  const state = EditorState.create({
    doc: props.modelValue,
    extensions: [
      basicSetup,
      yaml(),
      syntaxHighlighting(buildHighlightStyle()),
      themeCompartment.of(buildTheme()),
      EditorView.updateListener.of((update) => {
        if (!update.docChanged) return
        if (updatingFromProp) return
        emit('update:modelValue', update.state.doc.toString())
      }),
    ],
  })
  view.value = new EditorView({ state, parent: hostEl.value })
})

watch(
  () => props.modelValue,
  (next) => {
    const v = view.value
    if (!v) return
    if (v.state.doc.toString() === next) return
    updatingFromProp = true
    try {
      v.dispatch({
        changes: { from: 0, to: v.state.doc.length, insert: next },
      })
    }
    finally {
      updatingFromProp = false
    }
  },
)

onBeforeUnmount(() => {
  view.value?.destroy()
  view.value = null
})
</script>

<template>
  <div
    ref="hostEl"
    class="overflow-hidden rounded-md border focus-within:ring-2 focus-within:ring-ring"
    :style="{ minHeight, maxHeight: maxHeight ?? minHeight }"
  />
</template>
