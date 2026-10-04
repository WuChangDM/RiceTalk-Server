import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 2 : 0,
  reporter: 'list',
  use: {
    baseURL: process.env.BASE_URL || 'http://localhost:5174/admin/',
    trace: 'on-first-retry',
    ignoreHTTPSErrors: process.env.IGNORE_HTTPS_ERRORS === '1' || !!process.env.CI,
  },
  // 自己拉起 vite dev server（node_modules/.bin 为空，所以直接调 vite.js）。
  // 已经手动跑着 dev server 时复用它，避免打断正在调试的实例。
  // 说明：dev server 只负责前端；需要登录的用例仍要求后端（管理端口 9090）
  // 可达，否则用 test.skip(条件, 原因) 明确跳过而不是假绿。
  webServer: process.env.BASE_URL
    ? undefined
    : {
        command: 'node node_modules/vite/bin/vite.js --port 5174 --strictPort',
        url: 'http://localhost:5174/admin/',
        reuseExistingServer: !process.env.CI,
        timeout: 120_000,
      },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
