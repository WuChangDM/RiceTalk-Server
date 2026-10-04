import { test, expect } from '@playwright/test'
import {
  ADMIN_EMAIL, ADMIN_PASSWORD,
  backendAvailable, backendSkipReason,
  expectAdminShell, expectLoginForm, loginErrorBox, openAdminApp, submitLogin,
} from './helpers'

// 重写说明（旧版 bootstrap.spec.ts 的问题）：
// 旧用例只断言 `body` 含 "RidgeRiceTalk" —— 而登录页的 <h1> 同样写着
// 「RidgeRiceTalk 管理端」，所以登录成功、登录失败、甚至压根没登录时该断言都成立，
// 完全没有区分度。下面改为断言「登录后才存在的元素」与「登录失败时的错误提示」。

test.describe('管理端登录', () => {
  // 不依赖后端：未登录状态由前端渲染（getAdminBootstrapStatus 失败时也走登录页）。
  test('未登录时展示登录表单，登录后才有的元素不存在', async ({ page }) => {
    await openAdminApp(page)

    await expectLoginForm(page)
    // 这三条是「区分度」所在：登录页有品牌名，但没有侧边栏/退出登录
    await expect(page.getByRole('button', { name: /退出登录/ })).toHaveCount(0)
    await expect(page.getByText('频道管理', { exact: true })).toHaveCount(0)
    await expect(page.getByRole('heading', { name: '服务器设置' })).toHaveCount(0)
  })

  test('凭证错误时给出错误提示且不进入管理端', async ({ page, request }) => {
    test.skip(!(await backendAvailable(request)), backendSkipReason())

    await submitLogin(page, ADMIN_EMAIL, 'definitely-not-the-right-password')

    const error = loginErrorBox(page)
    await expect(error).toBeVisible({ timeout: 15000 })
    await expect(error).not.toBeEmpty()
    // 仍然停在登录页：侧边栏 / 退出登录都不能出现
    await expect(page.getByRole('button', { name: /退出登录/ })).toHaveCount(0)
    await expect(page.getByText('频道管理', { exact: true })).toHaveCount(0)
  })

  test('凭证正确时进入管理端，登录后独有元素出现', async ({ page, request }) => {
    test.skip(!(await backendAvailable(request)), backendSkipReason())

    await submitLogin(page, ADMIN_EMAIL, ADMIN_PASSWORD)

    // expectAdminShell 断言的是「只有登录后才有」的元素（含登录页输入框消失），
    // 而不是品牌名。
    await expectAdminShell(page)
    await expect(page.getByRole('heading', { name: '服务器设置' })).toBeVisible()
  })
})
