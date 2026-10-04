import { chromium } from 'playwright';

const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });

await page.goto('http://localhost:5173/');
await page.fill('input[placeholder="邮箱"]', 'user@example.com');
await page.fill('input[placeholder="密码"]', 'password');
await page.click('.auth-btn');
await page.waitForTimeout(2000);

await page.dblclick('text=大厅');
await page.waitForTimeout(2500);

// 展开麦克风滑条（未静音）
await page.click('.vc-split-arrow');
await page.waitForTimeout(500);
await page.screenshot({ path: '../../voice-slider-normal.png' });

// 点击麦克风静音
await page.click('.vc-split-main');
await page.waitForTimeout(500);
await page.screenshot({ path: '../../voice-slider-muted.png' });

// 再点击取消静音
await page.click('.vc-split-main');
await page.waitForTimeout(500);
await page.screenshot({ path: '../../voice-slider-unmute.png' });

await browser.close();
