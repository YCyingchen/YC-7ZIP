// 设置面板验收：更新检查、壁纸、应用内更新日志。
//
//   node tools/settings-test.cjs http://192.168.1.9:8092

const fs = require('fs');
const path = require('path');
const os = require('os');

const playwrightPath = process.env.PW_MODULE;
if (!playwrightPath) {
  console.error('请先设置 PW_MODULE 指向 playwright 模块目录');
  process.exit(2);
}
const { chromium } = require(playwrightPath);

const BASE = (process.argv[2] || 'http://192.168.1.9:8092').replace(/\/+$/, '');
const OUT = process.env.UI_OUT || path.join(os.tmpdir(), 'yc7zip-settings');
fs.mkdirSync(OUT, { recursive: true });

let pass = 0;
const failures = [];
function check(name, ok, detail = '') {
  if (ok) { pass++; console.log(`  \x1b[32m[PASS]\x1b[0m ${name}`); }
  else { failures.push(`${name}${detail ? ' -> ' + detail : ''}`); console.log(`  \x1b[31m[FAIL]\x1b[0m ${name}${detail ? ' -> ' + detail : ''}`); }
}
function section(t) { console.log(`\n\x1b[1;36m== ${t}\x1b[0m`); }

(async () => {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await context.newPage();
  const pageErrors = [];
  const failedRequests = [];
  page.on('pageerror', (e) => pageErrors.push(e.message));
  page.on('requestfailed', (r) => failedRequests.push(`${r.method()} ${r.url()}`));

  try {
    section('1. 打开设置面板');
    await page.goto(BASE + '/', { waitUntil: 'networkidle', timeout: 30000 });
    await page.waitForSelector('#settings-toggle', { timeout: 15000 });
    await page.click('#settings-toggle');
    await page.waitForSelector('#settings-modal:not([hidden])', { timeout: 10000 });
    check('设置面板打开', true);

    const current = await page.textContent('#update-current');
    check('显示当前版本', /zip\d{4}\.\d{3}/.test(current), current);
    check('显示发布通道', /test|stable/.test(current), current);
    check('显示部署方式', /裸二进制|容器|飞牛应用包|binary|container|package/.test(current), current);

    section('2. 应用内更新日志');
    await page.waitForFunction(
      () => document.querySelectorAll('#changelog-inline .release').length > 0,
      { timeout: 15000 }
    );
    const relCount = await page.locator('#changelog-inline .release').count();
    check('渲染出多个版本', relCount >= 2, `版本数 ${relCount}`);
    const changelogText = await page.textContent('#changelog-inline');
    check('含最新版本号', /zip2609\.002/.test(changelogText));
    check('含小节标题', /新增/.test(changelogText));
    const itemCount = await page.locator('#changelog-inline .release-items li').count();
    check('渲染出条目', itemCount >= 5, `条目数 ${itemCount}`);
    await page.screenshot({ path: path.join(OUT, '01-设置-更新日志.png'), fullPage: true });

    section('3. 检查更新');
    await page.click('#btn-update-check');
    await page.waitForFunction(
      () => {
        const el = document.querySelector('#update-result');
        return el && !el.hidden && el.textContent.trim().length > 0;
      },
      { timeout: 45000 }
    );
    const updateText = await page.textContent('#update-result');
    check('检查有结果反馈', updateText.trim().length > 0, updateText.slice(0, 80));
    const kind = await page.getAttribute('#update-result', 'data-kind');
    // 还没有打过任何 Release，所以预期是"没找到适用发布"这类提示，不该是崩溃
    check('结果是可读的提示而非异常', !/undefined|NaN|HTTP 5/.test(updateText), updateText.slice(0, 60));
    console.log(`     结果类型=${kind || 'info'}：${updateText.slice(0, 90)}`);

    section('4. 壁纸');
    // 先用 API 直接设一张，再回来验证界面渲染（模拟"从 NAS 选"的结果）
    const set = await page.evaluate(async () => {
      const res = await fetch('api/wallpaper', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ source_path: '/vol5/1000/空间4/YC-7ZIP/_uitest/media/风景-横向.jpg' }),
      });
      return { status: res.status, body: await res.json().catch(() => ({})) };
    });
    check('通过 NAS 路径设置壁纸', set.status === 200, JSON.stringify(set.body).slice(0, 120));
    check('返回 has_wallpaper', set.body.has_wallpaper === true, JSON.stringify(set.body));

    await page.reload({ waitUntil: 'networkidle' });
    await page.waitForTimeout(800);
    const wpState = await page.evaluate(() => ({
      hasClass: document.body.classList.contains('has-wallpaper'),
      image: getComputedStyle(document.documentElement).getPropertyValue('--wp-image').trim(),
      scrim: getComputedStyle(document.documentElement).getPropertyValue('--wp-scrim').trim(),
    }));
    check('页面应用了壁纸类', wpState.hasClass);
    check('注入了壁纸 URL', wpState.image.includes('/api/wallpaper'), wpState.image);
    check('注入了暗化 scrim', /rgba\(/.test(wpState.scrim), wpState.scrim);

    await page.click('#settings-toggle');
    await page.waitForSelector('#settings-modal:not([hidden])', { timeout: 10000 });
    const previewBg = await page.getAttribute('#wallpaper-preview', 'style');
    check('预览区显示壁纸', /wallpaper/.test(previewBg || ''), previewBg);

    // 调暗化滑块，背景 scrim 应当跟着变
    await page.locator('#wp-dim').fill('80');
    await page.locator('#wp-dim').dispatchEvent('input');
    await page.waitForTimeout(300);
    const scrim80 = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue('--wp-scrim').trim());
    check('暗化滑块生效', /0\.8/.test(scrim80), scrim80);

    await page.locator('#wp-blur').fill('20');
    await page.locator('#wp-blur').dispatchEvent('input');
    await page.waitForTimeout(300);
    const blur = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue('--wp-blur').trim());
    check('模糊滑块生效', blur === '20px', blur);

    await page.screenshot({ path: path.join(OUT, '02-设置-壁纸.png'), fullPage: true });
    await page.screenshot({ path: path.join(OUT, '03-带壁纸的主界面.png') });

    section('5. 清除壁纸');
    await page.click('#btn-wallpaper-clear');
    await page.waitForTimeout(800);
    const cleared = await page.evaluate(() => document.body.classList.contains('has-wallpaper'));
    check('清除后壁纸层移除', cleared === false);

    section('6. 控制台');
    check('无 JS 运行时异常', pageErrors.length === 0, pageErrors.join(' | '));
    check('无失败请求', failedRequests.length === 0, failedRequests.join(' | '));
  } catch (err) {
    failures.push(`执行中断：${err.message}`);
    console.log(`  \x1b[31m[FAIL]\x1b[0m 执行中断：${err.message}`);
    try { await page.screenshot({ path: path.join(OUT, 'error.png'), fullPage: true }); } catch {}
  } finally {
    await browser.close();
  }

  console.log('\n' + '='.repeat(46));
  if (!failures.length) {
    console.log(`\x1b[32m全部通过：${pass} 项\x1b[0m\n截图：${OUT}`);
    process.exit(0);
  }
  console.log(`\x1b[31m通过 ${pass} 项，失败 ${failures.length} 项\x1b[0m`);
  failures.forEach((f) => console.log(`  - ${f}`));
  process.exit(1);
})();
