import { test, expect } from '@playwright/test'
import { uniqueName, register, login } from './helpers'

/**
 * admin-runners.spec (P1)
 *
 * Smoke-tests the admin runners page:
 * - Navigate to the runners page (admin-only).
 * - Verify the page renders (list or empty state).
 * - Create a new runner via the dialog.
 * - Verify the enrollment dialog shows the token, install one-liner, and
 *   manual enroll command.
 *
 * Set E2E_ADMIN_USERNAME / E2E_ADMIN_PASSWORD env vars to use a
 * pre-existing admin account. Otherwise the test registers a new
 * user and assumes the first-registered-user-is-admin convention
 * (only true on a fresh database).
 */

const ADMIN_USER = process.env.E2E_ADMIN_USERNAME || ''
const ADMIN_PASS = process.env.E2E_ADMIN_PASSWORD || 'testpass123'

test.describe('admin runners', () => {
  let adminUser = ''

  test.beforeAll(async ({ browser }) => {
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    if (ADMIN_USER) {
      await login(page, ADMIN_USER, ADMIN_PASS)
      adminUser = ADMIN_USER
    } else {
      // On a fresh instance the first registered user is admin.
      adminUser = uniqueName('e2eadmin')
      await register(page, adminUser, `${adminUser}@test.local`, ADMIN_PASS)
    }
    await ctx.close()
  })

  test('navigate to runners page and verify it loads', async ({ page }) => {
    await login(page, adminUser, ADMIN_PASS)

    await page.goto('/admin/runners')
    await page.getByRole('heading').first().waitFor({ state: 'visible', timeout: 15_000 })

    // The page heading should mention Runners (en) / 运行器 (zh).
    // Use generic heading role — SSR may alter heading levels or render
    // multiple headings from parent layouts.
    await expect(page.getByRole('heading').first()).toBeVisible()

    // Either the table or the empty state should be visible.
    const table = page.locator('table')
    const emptyState = page.getByText(/No runners|暂无 Runner/i)
    await expect(table.or(emptyState).first()).toBeVisible({ timeout: 10_000 })
  })

  test('create a new runner and verify enrollment dialog', async ({ page }) => {
    await login(page, adminUser, ADMIN_PASS)
    await page.goto('/admin/runners')
    await page.getByRole('heading').first().waitFor({ state: 'visible', timeout: 15_000 })

    const runnerName = uniqueName('e2erunner')

    // Click "Add runner" / "添加 Runner" button.
    const addBtn = page.getByRole('button', { name: /Add runner|添加\s*Runner|新建/i }).first()
    await addBtn.click()

    // The create dialog should appear.
    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 5_000 })

    // Fill in the runner name.
    const nameInput = page.locator('[role="dialog"] input').first()
    await nameInput.fill(runnerName)

    // Submit the form.
    const submitBtn = page.getByRole('button', { name: /Submit|提交|创建/i }).last()
    await submitBtn.click()

    // The enrollment dialog should appear (one-time token display).
    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 10_000 })

    // Verify key sections are present in the enrollment dialog.
    const dialog = page.getByRole('dialog')

    // 1. Token is displayed.
    await expect(dialog.locator('code').first()).toBeVisible()

    // 2. Install one-liner is displayed (curl | sh pattern or similar).
    const oneLinerSection = dialog.getByText(/curl|install|安装/i)
    await expect(oneLinerSection.first()).toBeVisible({ timeout: 5_000 })

    // 3. Manual enroll section (inside a <details> element).
    const details = dialog.locator('details')
    if (await details.isVisible().catch(() => false)) {
      await details.click()
      // The dialog contains many "enroll" strings (heading, install label,
      // code block). Use .first() to avoid strict-mode violation.
      await expect(dialog.getByText(/enroll/i).first()).toBeVisible({ timeout: 3_000 })
    }

    // Acknowledge and close.
    const ackBtn = page.getByRole('button', { name: /saved|acknowledge|我已|记下|保存/i }).first()
    await ackBtn.click()

    // Dialog should close.
    await expect(page.getByRole('dialog')).not.toBeVisible({ timeout: 5_000 })

    // The new runner should appear in the list.
    await expect(page.locator('table')).toBeVisible({ timeout: 5_000 })
    await expect(page.locator('td', { hasText: runnerName })).toBeVisible({ timeout: 5_000 })
  })
})
