import { test, expect } from '@playwright/test'
import { uniqueName, ensureLoggedIn, createRepo } from './helpers'

/**
 * workflows.spec (P1)
 *
 * Smoke-tests the workflow runs page:
 * - Navigate to the workflows page for a repo.
 * - Verify the page renders (list or empty state).
 * - Verify status filter tabs are present and clickable.
 * - Open the dispatch dialog, verify form fields, and submit.
 *
 * NOTE: The dispatch endpoint may return an error if no workflow
 * definition exists in the repo — this is expected and the test should
 * gracefully handle it. The goal is to verify the UI flow, not that a
 * workflow actually runs.
 */

const PASSWORD = 'testpass123'

test.describe('workflows', () => {
  let owner = ''
  const repoName = uniqueName('e2ewfrepo')

  test.beforeAll(async ({ browser }) => {
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    const username = uniqueName('e2ewfowner')
    owner = username
    await ensureLoggedIn(page, username, PASSWORD)
    await createRepo(page, repoName)
    await ctx.close()
  })

  test('navigate to workflows page and verify it loads', async ({ page }) => {
    await ensureLoggedIn(page, owner, PASSWORD)
    await page.goto(`/${owner}/${repoName}/workflows`)
    await page.getByRole('heading').first().waitFor({ state: 'visible', timeout: 15_000 })

    // Page heading should be visible.
    await expect(page.getByRole('heading').first()).toBeVisible()

    // The filter tabs should be present (reka-ui TabsTrigger renders as <button role="tab">).
    const tabs = page.locator('[data-slot="tabs-trigger"]')
    await expect(tabs.first()).toBeVisible({ timeout: 5_000 })

    // Either the list or the empty state should be visible.
    const emptyState = page.getByText(/No workflow runs|暂无.*[wW]orkflow|empty/i)
    const listItems = page.locator('ul > li')
    await expect(emptyState.or(listItems.first()).first()).toBeVisible({ timeout: 10_000 })
  })

  test('filter tabs are clickable', async ({ page }) => {
    await ensureLoggedIn(page, owner, PASSWORD)
    await page.goto(`/${owner}/${repoName}/workflows`)
    // reka-ui TabsTrigger renders as <button role="tab">.
    await page.waitForSelector('[data-slot="tabs-trigger"]', { timeout: 15_000 })

    // Click through each status filter tab (reka-ui TabsTrigger exposes role="tab").
    const tabNames = ['Pending', 'Running', 'Success', 'Failed', 'Cancelled']
    for (const name of tabNames) {
      const tab = page.getByRole('tab', { name: new RegExp(name, 'i') })
      if (await tab.isVisible().catch(() => false)) {
        await tab.click()
        // Wait for any loading to settle.
        await page.waitForTimeout(500)
      }
    }

    // Back to "All".
    const allTab = page.getByRole('tab', { name: /All|全部/i })
    if (await allTab.isVisible().catch(() => false)) {
      await allTab.click()
    }

    // The page should still be functional — no crash.
    await expect(page.getByRole('heading').first()).toBeVisible()
  })

  test('dispatch dialog opens and has expected form fields', async ({ page }) => {
    await ensureLoggedIn(page, owner, PASSWORD)
    await page.goto(`/${owner}/${repoName}/workflows`)
    await page.waitForSelector('[data-slot="tabs-trigger"]', { timeout: 15_000 })

    // Click the dispatch button.
    const dispatchBtn = page.getByRole('button', { name: /Dispatch|手动触发|触发/i }).first()
    await dispatchBtn.click()

    // The dialog should appear.
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible({ timeout: 5_000 })

    // Dialog should have a title.
    await expect(dialog.getByRole('heading').first()).toBeVisible()

    // There should be a workflow name input.
    const nameInput = dialog.locator('#dispatch-workflow-name')
    await expect(nameInput).toBeVisible()
    await nameInput.fill('ci')

    // There should be a ref input.
    const refInput = dialog.locator('#dispatch-ref')
    await expect(refInput).toBeVisible()
    await refInput.fill('main')

    // Register the dialog handler BEFORE clicking submit — dispatch may
    // trigger window.alert synchronously on success. Use once() so the
    // handler doesn't linger and interfere with other tests.
    page.once('dialog', async (d) => {
      await d.accept()
    })

    // Submit the dispatch — it may succeed or fail depending on backend
    // state, but we just verify the dialog closes or an error is shown.
    const submitBtn = dialog.getByRole('button', {
      name: /Run workflow|执行|Dispatch|Submit|提交|触发/i,
    }).last()
    await submitBtn.click()

    // Either the dialog closes (success + alert) or shows an error.
    // Wait for either outcome.
    await page.waitForTimeout(2000)

    // If still open, cancel it.
    if (await dialog.isVisible().catch(() => false)) {
      const cancelBtn = dialog.getByRole('button', { name: /Cancel|取消/i })
      if (await cancelBtn.isVisible().catch(() => false)) {
        await cancelBtn.click()
      }
    }

    // Dialog should now be closed.
    await expect(dialog).not.toBeVisible({ timeout: 5_000 })
  })
})
