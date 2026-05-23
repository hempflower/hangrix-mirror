import { test, expect } from '@playwright/test'
import { uniqueName, register, login } from './helpers'

/**
 * auth.spec (P0)
 *
 * Smoke-tests the authentication flow:
 * - Unauthenticated users are redirected to /login.
 * - A new account can be registered.
 * - An existing account can log in.
 * - After login the user lands on the home page.
 */

test.describe('auth', () => {
  test('unauthenticated user is redirected to login', async ({ page }) => {
    await page.goto('/')
    await expect(page).toHaveURL(/\/login/)
    await expect(page.locator('h1, h2, h3').first()).toBeVisible()
    // The login form should have username + password fields.
    await expect(page.locator('input[type="text"]')).toBeVisible()
    await expect(page.locator('input[type="password"]')).toBeVisible()
  })

  test('register a new account', async ({ page }) => {
    const username = uniqueName('e2euser')
    const password = 'testpass123'

    await register(page, username, `${username}@test.local`, password)

    // After registration we should be on the home page (not login).
    await expect(page).not.toHaveURL(/\/login/)
    await expect(page).not.toHaveURL(/\/register/)
  })

  test('login with existing account then logout', async ({ page }) => {
    const username = uniqueName('e2elogin')
    const password = 'testpass123'

    // First register the account.
    await register(page, username, `${username}@test.local`, password)

    // Log out — the logout action lives inside a user dropdown menu in
    // the sidebar footer. The user menu button is wrapped in a
    // DropdownMenuTrigger (as-child) whose data-slot attribute overwrites
    // the SidebarMenuButton's data-slot via Vue fallthrough $attrs.
    // Use aria-haspopup="menu" (added by reka-ui DropdownMenuTrigger) to
    // locate it instead.
    await page.goto('/')
    const userMenuTrigger = page.locator('[data-slot="sidebar-footer"] [aria-haspopup="menu"]').first()
    await userMenuTrigger.click()

    // The dropdown menu content is teleported to the top level.
    const logoutItem = page.getByRole('menuitem', { name: /log\s*out|退出登录/i })
    await logoutItem.waitFor({ state: 'visible', timeout: 5_000 })
    await logoutItem.click()

    // Should now be back on login.
    await expect(page).toHaveURL(/\/login/, { timeout: 10_000 })

    // Log back in.
    await login(page, username, password)
    await expect(page).not.toHaveURL(/\/login/)
  })
})
