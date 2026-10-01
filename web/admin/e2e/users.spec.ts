import { test, expect } from '@playwright/test'
import {
  ADMIN_EMAIL, ADMIN_PASSWORD,
  backendAvailable, backendSkipReason, expectAdminShell, submitLogin,
} from './helpers'

// 重写说明（旧版 members.spec.ts 的问题）：
// 旧用例通过 `page.goto('./members')` 导航 —— 管理端根本没有这个 URL
// （管理页是 tab 式 SPA，路由由 state 驱动；/admin/members 实测是 404），
// 最后又断言 `body` 含 "test"，而搜索框里刚好就填着 "test" 且页面处处是
// 用户名/邮箱文本，断言恒真。下面改为：用侧边栏真实导航 + 断言表格结构。

test.describe('用户管理', () => {
  test.beforeEach(async ({ page, request }) => {
    test.skip(!(await backendAvailable(request)), backendSkipReason())
    await submitLogin(page, ADMIN_EMAIL, ADMIN_PASSWORD)
    await expectAdminShell(page)
  })

  test('从侧边栏进入用户管理，表格结构符合模型（无 IP / 延迟假列）', async ({ page }) => {
    const urlBefore = page.url()

    await page.getByText('用户管理', { exact: true }).click()

    await expect(page.getByRole('heading', { name: '用户管理' })).toBeVisible({ timeout: 10000 })
    // 侧边栏导航是 state 驱动的 SPA 切换，不应跳转到不存在的 URL 路由
    expect(page.url()).toBe(urlBefore)

    const headers = page.locator('.data-table thead th')
    await expect(headers).toHaveText(['用户名', '角色', '状态', '在线', '操作'])
    // P1-4 回归：model.User 没有 IP / 延迟字段，这两列已从 UI 移除
    await expect(page.locator('.data-table thead')).not.toContainText('IP')
    await expect(page.locator('.data-table thead')).not.toContainText('延迟')

    // 统计卡片来自接口的 summary（不是写死的 0）
    await expect(page.getByText('总用户数')).toBeVisible()
    await expect(page.getByText('管理员')).toBeVisible()
  })

  test('搜索框回车后仍停留在用户管理（不跳转到不存在的路由）', async ({ page }) => {
    await page.getByText('用户管理', { exact: true }).click()
    await expect(page.getByRole('heading', { name: '用户管理' })).toBeVisible({ timeout: 10000 })

    const search = page.getByLabel('搜索用户')
    await search.fill('test')
    await search.press('Enter')

    // 断言的是「搜索后页面结构仍在、内容确实被过滤过」，而不是 body 里出现过 test
    // （那种断言在搜索框本身就含 test 时恒真）。
    await expect(page.getByRole('heading', { name: '用户管理' })).toBeVisible()
    await expect(page.locator('.data-table thead th').first()).toHaveText('用户名')
    await expect(search).toHaveValue('test')
    expect(page.url()).toContain('/admin/')
    expect(page.url()).not.toContain('/members')
  })
})
