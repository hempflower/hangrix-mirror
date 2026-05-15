import { watchEffect } from 'vue'

// Crumb is the rendered shape: a label plus an optional click target.
export interface Crumb {
  label: string
  to?: string
}

const KEY = 'breadcrumbs'

// useBreadcrumbsState exposes the shared ref so AppHeader can read it.
// Pages should call setBreadcrumbs() instead.
export function useBreadcrumbsState() {
  return useState<Crumb[]>(KEY, () => [])
}

// setBreadcrumbs registers a page's crumb chain. Call it from <script
// setup> with a supplier that returns the array. The supplier runs inside
// watchEffect, so any reactive deps (locale, params, fetched data) tracked
// inside re-trigger the update without manual watches.
//
// Pages do NOT need to clear on unmount — the next page calls
// setBreadcrumbs() and overwrites the state. Leaving the previous chain
// visible during route transitions avoids a one-frame "empty header"
// flicker.
export function setBreadcrumbs(supplier: () => Crumb[]) {
  const state = useBreadcrumbsState()
  watchEffect(() => {
    state.value = supplier()
  })
}
