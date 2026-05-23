import { test, expect } from '@playwright/test'
import { uniqueName, ensureLoggedIn, createRepo, createIssue, postComment } from './helpers'

/**
 * repo-issue.spec (P0)
 *
 * Smoke-tests the core repo + issue workflow:
 * - Create a repository.
 * - Create an issue.
 * - Verify the issue detail page renders with all tabs.
 * - Switch tabs (conversation → commits → diff → contributions → agents).
 * - Post a comment and verify it appears in the timeline.
 */

const PASSWORD = 'testpass123'

test.describe('repo + issue workflow', () => {
  let owner = ''
  const repoName = uniqueName('e2erepo')
  let issueNumber = 0

  test.beforeAll(async ({ browser }) => {
    // Provision a fresh user for this suite.
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    const username = uniqueName('e2erepoowner')
    owner = username
    await ensureLoggedIn(page, username, PASSWORD)
    await ctx.close()
  })

  test('create a repository', async ({ page }) => {
    await ensureLoggedIn(page, owner, PASSWORD)
    await createRepo(page, repoName, {
      description: 'E2E smoke test repository',
    })

    // We should land on the repo detail page.
    await expect(page).toHaveURL(new RegExp(`/${owner}/${repoName}`))
    // The repo name should be visible.
    await expect(page.getByRole('heading').first()).toBeVisible()
  })

  test('create an issue in the repository', async ({ page }) => {
    await ensureLoggedIn(page, owner, PASSWORD)

    const title = `E2E smoke test issue ${uniqueName('')}`
    issueNumber = await createIssue(page, owner, repoName, title, 'This is a test issue body for e2e.')

    expect(issueNumber).toBeGreaterThan(0)

    // We should be on the issue detail page.
    await expect(page).toHaveURL(new RegExp(`/${owner}/${repoName}/issues/${issueNumber}`))
    // The issue title should be visible.
    await expect(page.getByRole('heading').first()).toContainText(title)
  })

  test('issue detail page renders all required tabs', async ({ page }) => {
    await ensureLoggedIn(page, owner, PASSWORD)
    await page.goto(`/${owner}/${repoName}/issues/${issueNumber}`)

    // Wait for the page to load (heading may be h1/h2/h3 depending on layout).
    await page.getByRole('heading').first().waitFor({ state: 'visible', timeout: 15_000 })

    // Verify the 5 tabs are present. reka-ui TabsTrigger renders as <button role="tab">;
    // use data-slot for counting and getByRole('tab') for interaction.
    const tabs = page.locator('[data-slot="tabs-trigger"]')
    await expect(tabs).toHaveCount(5)

    // Verify individual tabs by their label text (reka-ui TabsTrigger exposes role="tab").
    await expect(page.getByRole('tab', { name: /Conversation|对话/i })).toBeVisible()
    await expect(page.getByRole('tab', { name: /Commits|提交/i })).toBeVisible()
    await expect(page.getByRole('tab', { name: /Diff|差异/i })).toBeVisible()
    await expect(page.getByRole('tab', { name: /Contributions|贡献/i })).toBeVisible()
    await expect(page.getByRole('tab', { name: /Agents|智能体/i })).toBeVisible()
  })

  test('switch between issue tabs and verify URL updates', async ({ page }) => {
    await ensureLoggedIn(page, owner, PASSWORD)
    await page.goto(`/${owner}/${repoName}/issues/${issueNumber}`)
    await page.getByRole('heading').first().waitFor({ state: 'visible', timeout: 15_000 })

    // Default tab is "conversation" — no ?tab= in URL.
    await expect(page).not.toHaveURL(/tab=/)

    // Click commits tab.
    await page.getByRole('tab', { name: /Commits|提交/i }).click()
    await expect(page).toHaveURL(/tab=commits/)
    // The commits tab should show empty state or content.
    await expect(page.locator('[data-slot="tabs-content"]')).toBeVisible()

    // Click diff tab.
    await page.getByRole('tab', { name: /Diff|差异/i }).click()
    await expect(page).toHaveURL(/tab=diff/)

    // Click contributions tab.
    await page.getByRole('tab', { name: /Contributions|贡献/i }).click()
    await expect(page).toHaveURL(/tab=contributions/)

    // Click agents tab.
    await page.getByRole('tab', { name: /Agents|智能体/i }).click()
    await expect(page).toHaveURL(/tab=agents/)

    // Back to conversation.
    await page.getByRole('tab', { name: /Conversation|对话/i }).click()
    await expect(page).not.toHaveURL(/tab=/)
  })

  test('post a comment and verify it appears in the timeline', async ({ page }) => {
    await ensureLoggedIn(page, owner, PASSWORD)
    await page.goto(`/${owner}/${repoName}/issues/${issueNumber}`)
    await page.getByRole('heading').first().waitFor({ state: 'visible', timeout: 15_000 })

    // Make sure we are on the conversation tab.
    const conversationTab = page.getByRole('tab', { name: /Conversation|对话/i })
    await conversationTab.click()

    const commentText = `E2E comment ${Date.now()}`

    // Find the comment textarea inside the compose card.
    // The issue detail page has a MentionTextarea at the bottom.
    const textarea = page.locator('textarea').last()
    await textarea.waitFor({ state: 'visible', timeout: 10_000 })
    await textarea.fill(commentText)

    // Click the submit button.
    const submitBtn = page.getByRole('button', { name: /Comment|评论|提交/i }).last()
    await submitBtn.click()

    // Wait for the comment to appear in the timeline.
    // Comments render inside Card components with the text content.
    await expect(page.locator('.text-sm', { hasText: commentText }).first()).toBeVisible({
      timeout: 15_000,
    })
  })
})
