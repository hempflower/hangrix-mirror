import { Page, expect } from '@playwright/test'

/** Generate a short unique suffix so tests don't collide on resource names. */
export function uniqueName(prefix: string): string {
  const ts = Date.now().toString(36)
  const rand = Math.random().toString(36).slice(2, 6)
  return `${prefix}-${ts}${rand}`
}

/**
 * Register a new user. Returns the credentials used.
 * Expects to be on the login (or any page that redirects to /login when
 * unauthenticated) — navigates to /register first.
 */
export async function register(
  page: Page,
  username: string,
  email: string,
  password: string,
): Promise<void> {
  await page.goto('/register')
  await page.waitForSelector('input[type="text"]') // username field
  await page.fill('input[type="text"]', username)
  await page.fill('input[type="email"]', email)
  await page.fill('input[type="password"]', password)
  await page.click('button[type="submit"]')
  // After successful registration we should land on the home page.
  await page.waitForURL((url) => {
		return !url.pathname.startsWith('/register') && !url.pathname.startsWith('/login')
	}, { timeout: 15_000 })
}

/**
 * Log in as an existing user. Navigates to /login first.
 */
export async function login(
  page: Page,
  username: string,
  password: string,
): Promise<void> {
  await page.goto('/login')
  await page.waitForSelector('input[type="text"]')
  await page.fill('input[type="text"]', username)
  await page.fill('input[type="password"]', password)
  await page.click('button[type="submit"]')
  await page.waitForURL((url) => {
		return !url.pathname.startsWith('/login')
	}, { timeout: 15_000 })
}

/**
 * Ensure we are logged in with the given credentials. If the user does not
 * exist yet, register first; otherwise log in.
 *
 * IMPORTANT: this is a best-effort helper. In practice, most CI setups
 * should pre-provision a test account and pass credentials via env vars.
 */
export async function ensureLoggedIn(
  page: Page,
  username: string,
  password: string,
): Promise<void> {
  // Try logging in first. login() throws (waitForURL timeout) if
  // credentials are wrong, so we must catch it to reach the register path.
  try {
    await login(page, username, password)
  } catch {
    // login() threw — account doesn't exist or wrong password.
  }
  // If we're still on /login, the account doesn't exist — register.
  if (page.url().includes('/login')) {
    await register(page, username, `${username}@test.local`, password)
  }
}

/** Create a repo from the UI. Expects to already be logged in. */
export async function createRepo(
  page: Page,
  repoName: string,
  options?: { description?: string; visibility?: 'public' | 'private'; initReadme?: boolean },
): Promise<void> {
  await page.goto('/repos/new')
  // The checkbox is a shadcn-vue Checkbox (reka-ui), which renders as
  // <button role="checkbox">, not <input>. Use the id without element tag.
  // The Checkbox id may not reach the DOM (reka-ui forwards props but
  // not 'id'). Wait for the repo-name input instead.
  await page.waitForSelector('input[autocomplete="off"]', { timeout: 10_000 })

  // Fill repo name
  await page.fill('input[autocomplete="off"]', repoName)

  if (options?.description) {
    // The description input is the second autocomplete=off input.
    const inputs = page.locator('input[autocomplete="off"]')
    await inputs.nth(1).fill(options.description)
  }

  if (options?.visibility === 'public') {
    await page.click('#visibility-public')
  }

  if (options?.initReadme !== undefined) {
    // The init-readme checkbox is a reka-ui Checkbox rendered as
    // <button role="checkbox">. Its initial value from the form schema
    // is true, but we explicitly toggle to match the caller's intent.
    const cb = page.locator('#init-readme')
    const isChecked = await cb.getAttribute('aria-checked')
    const wantChecked = String(options.initReadme)
    if (isChecked !== wantChecked) {
      await cb.click()
    }
  }

  await page.click('button[type="submit"]')
  // After creation we navigate to the repo detail page.
  await page.waitForURL(/\/[^/]+\/[^/]+$/, { timeout: 15_000 })
}

/** Create an issue via the UI. Expects to be on the repo page. */
export async function createIssue(
  page: Page,
  owner: string,
  repoName: string,
  title: string,
  body?: string,
): Promise<number> {
  await page.goto(`/${owner}/${repoName}/issues/new`)
  // The issue title is the first textbox on the create page (textarea for
  // body comes second). Use semantic role locator instead of a CSS attribute
  // selector like [autofocus] which may not survive SSR hydration.
  const titleInput = page.getByRole('textbox').first()
  await titleInput.waitFor({ state: 'visible', timeout: 10_000 })
  await titleInput.fill(title)
  if (body) {
    await page.getByRole('textbox').last().fill(body)
  }
  await page.click('button[type="submit"]')
  // After creation we land on the issue detail page.
  await page.waitForURL(/\/issues\/\d+/, { timeout: 15_000 })
  const match = page.url().match(/\/issues\/(\d+)/)
  if (!match) throw new Error('Could not extract issue number from URL')
  return Number(match[1])
}

/** Post a comment on the current issue detail page. */
export async function postComment(page: Page, body: string): Promise<void> {
  // The comment textarea is inside the compose card at the bottom.
  const textarea = page.locator('textarea[placeholder]').last()
  await textarea.waitFor({ state: 'visible', timeout: 10_000 })
  await textarea.fill(body)
  await page.click('button:has-text("Comment")')
  // Wait for the comment card to appear with the body text.
  await expect(page.locator('.text-sm', { hasText: body }).first()).toBeVisible({ timeout: 10_000 })
}
