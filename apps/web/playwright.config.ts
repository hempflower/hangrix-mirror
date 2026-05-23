import { defineConfig, devices } from '@playwright/test'

const BASE_URL = process.env.E2E_BASE_URL || 'http://localhost:3000'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: process.env.CI ? 'github' : 'list',
  timeout: 60_000,
  expect: { timeout: 10_000 },

  use: {
    baseURL: BASE_URL,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    // Don't fail on mixed content / self-signed certs in local dev.
    ignoreHTTPSErrors: true,
  },

  // Optionally start the Nuxt dev server. By default e2e expects the
  // server to already be running; set E2E_START_SERVER=1 to let Playwright
  // manage it. The backend (port 8080) must still be running separately.
  webServer: process.env.E2E_START_SERVER
    ? {
        command: 'pnpm --filter web dev',
        url: BASE_URL,
        reuseExistingServer: !process.env.CI,
        timeout: 120_000,
      }
    : undefined,

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
