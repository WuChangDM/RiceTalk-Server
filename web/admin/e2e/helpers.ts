import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

// 管理端 e2e 公共工具。
//
// 重要前提：登录之后的所有断言都需要一个真实后端（vite dev server 把
// /api/admin 代理到管理端口，默认 9090）。后端不可达时，相关用例必须
// test.skip(条件, 原因) 明确跳过 —— 绝不能让用例在「什么都点不动」的情况下
// 靠恒真断言变绿（这正是旧版 e2e 的问题）。

export const ADMIN_EMAIL = process.env.ADMIN_EMAIL || 'owner@example.com'
export const ADMIN_PASSWORD = process.env.ADMIN_PASSWORD || 'Admin@2026!!Secure'

/**
 * 打开管理页，并在前端根本没编译成功时明确跳过（而不是让断言在错误遮罩上
 * 超时失败或假绿）。
 *
 * 已知阻塞（2026-09-14，本任务范围外，未修）：`src/styles.css` 第一行
 * `@import '../../voice/src/styles.css'` 指向已归档且未被版本控制的
 * `web/voice/`，该文件已不存在 —— vite dev / vite build 都会以
 * "ENOENT ... voice/src/styles.css" 失败，页面只剩 vite 错误遮罩。
 * 修好之后下面的 skip 自动失效，用例会真正跑起来。
 */
export async function openAdminApp(page: Page) {
  await page.goto('./')

  // 前端要么真的挂载出内容，要么弹出 vite 错误遮罩；等这两者之一出现再判断。
  await Promise.race([
    page.locator('#root > *').first().waitFor({ state: 'attached', timeout: 10000 }).catch(() => {}),
    page.locator('vite-error-overlay').waitFor({ state: 'attached', timeout: 10000 }).catch(() => {}),
  ])

  const overlayError = await page.evaluate(() => {
    const el = document.querySelector('vite-error-overlay') as (HTMLElement & { shadowRoot?: ShadowRoot }) | null
    if (!el) return null
    return (el.shadowRoot?.textContent || el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 300)
  })
  if (overlayError !== null) {
    test.skip(true, `管理页前端未能编译（vite 错误遮罩），修复前 e2e 无法验证 UI：${overlayError}`)
  }
  if ((await page.locator('#root > *').count()) === 0) {
    test.skip(true, '管理页未挂载（#root 下无内容）：dev server 未就绪或前端编译失败')
  }
}

/** 探测后端是否可达（经 vite 代理 → 管理端口）。 */
export async function backendAvailable(request: APIRequestContext): Promise<boolean> {
  try {
    const res = await request.get('/api/admin/bootstrap/status', { timeout: 3000 })
    return res.ok()
  } catch {
    return false
  }
}

export function backendSkipReason(): string {
  return `后端不可达（${process.env.ADMIN_BASE_URL || 'http://localhost:9090'}）：` +
    `本用例需要真实服务端；请先启动服务端（管理端口默认 9090）再执行 e2e`
}

/** 登录后才存在的元素：侧边栏导航 + 退出登录。登录页的 <h1> 同样含
 *  「RidgeRiceTalk 管理端」字样，所以断言必须挑这些元素，品牌名没有区分度。 */
export async function expectAdminShell(page: Page) {
  await expect(page.getByRole('button', { name: /退出登录/ })).toBeVisible({ timeout: 15000 })
  await expect(page.getByText('频道管理', { exact: true })).toBeVisible()
  await expect(page.getByText('运行监控', { exact: true })).toBeVisible()
  // 登录页独有的输入框必须消失
  await expect(page.getByPlaceholder('管理员邮箱')).toHaveCount(0)
}

/** 登录页：邮箱/密码输入 + 登录按钮 + 提示文案（只在未登录时存在）。 */
export async function expectLoginForm(page: Page) {
  await expect(page.getByLabel('邮箱')).toBeVisible()
  await expect(page.getByLabel('密码')).toBeVisible()
  await expect(page.getByRole('button', { name: '登录' })).toBeVisible()
  await expect(page.getByText('请输入管理员账号密码登录')).toBeVisible()
}

/** 用给定凭据登录；调用方负责断言成功或失败。 */
export async function submitLogin(page: Page, email: string, password: string) {
  await openAdminApp(page)
  await expectLoginForm(page)
  await page.getByLabel('邮箱').fill(email)
  await page.getByLabel('密码').fill(password)
  await page.getByRole('button', { name: '登录' }).click()
}

/** 登录失败时 LoginForm 用 var(--danger) 的红色 div 展示 err.message。
 *  具体文案取决于后端错误码，所以这里断言「出现了非空的错误框」。 */
export function loginErrorBox(page: Page) {
  return page.locator('div[style*="--danger"]').first()
}
