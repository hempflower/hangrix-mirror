import type { FileDiff, DiffStatus } from '~/types/repo'

/**
 * Parse a unified diff (from `git diff`) into per-file FileDiff objects.
 *
 * The input is the raw patch_text from the patch detail API. It follows the
 * standard `git diff` format where each file is introduced by a
 * `diff --git a/<old> b/<new>` header.
 *
 * File status is inferred from the header lines:
 * - `--- /dev/null` → added
 * - `+++ /dev/null` → deleted
 * - both non-/dev/null → modified (or renamed if paths differ)
 */
export function parseUnifiedDiffToFileDiffs(patchText: string): FileDiff[] {
  if (!patchText) return []

  // Split on `diff --git` headers. The regex captures the whole header
  // including the leading newline so we can reconstruct the full per-file
  // patch blob for each file.
  const FILE_HEADER_RE = /^diff --git a\/(.+) b\/(.+)$/gm

  const files: FileDiff[] = []
  let lastIndex = 0
  let match: RegExpExecArray | null

  // collect all file boundaries
  const boundaries: { oldPath: string; newPath: string; start: number; end: number }[] = []

  while ((match = FILE_HEADER_RE.exec(patchText)) !== null) {
    boundaries.push({
      oldPath: match[1]!,
      newPath: match[2]!,
      start: match.index,
      end: -1, // placeholder
    })
    if (boundaries.length > 1) {
      boundaries[boundaries.length - 2]!.end = match.index
    }
  }

  if (boundaries.length === 0) return []

  // last boundary goes to end of string
  boundaries[boundaries.length - 1]!.end = patchText.length

  for (const b of boundaries) {
    const chunk = patchText.slice(b.start, b.end).trimEnd()

    // Determine status from the `---` / `+++` lines inside the chunk.
    let status: DiffStatus = 'modified'
    if (chunk.includes('\n--- /dev/null')) {
      status = 'added'
    } else if (chunk.includes('\n+++ /dev/null')) {
      status = 'deleted'
    } else if (b.oldPath !== b.newPath) {
      status = 'renamed'
    }

    files.push({
      old_path: status === 'added' ? '' : b.oldPath,
      new_path: status === 'deleted' ? '' : b.newPath,
      status,
      patch: chunk,
      binary: false,
    })
  }

  return files
}
