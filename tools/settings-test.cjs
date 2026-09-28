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

// 点「检查更新」并等它真的跑完。
//
// 不能只等"结果框里有字"：面板一打开就会先把服务端缓存的上一轮结果画出来，
// 那个条件立刻成立，于是读到的是上一轮的文字（表现为"渠道少了一条"这种假失败）。
// 先等按钮进入"检查中"，再等它恢复，才是真的完成了这一轮。
async function checkAndWait(page) {
  await page.click('#btn-update-check');
  await page.waitForFunction(
    () => {
      const btn = document.querySelector('#btn-update-check');
      return !!(btn && btn.disabled);
    },
    { timeout: 15000 }
  );
  await page.waitForFunction(
    () => {
      const btn = document.querySelector('#btn-update-check');
      return !!(btn && !btn.disabled);
    },
    { timeout: 60000 }
  );
}

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
    // 跟当前版本比，而不是写死一个版本号：写死的话每次升版本这条都会红，
    // 而它真正要断言的是"更新日志与运行中的版本对得上"
    const currentVersion = ((await page.textContent('#update-current')) || '').match(/zip\d{4}\.\d{3}/);
    check(
      '更新日志含当前版本号',
      !!currentVersion && changelogText.includes(currentVersion[0]),
      `当前版本 ${currentVersion ? currentVersion[0] : '未识别'}`
    );
    check('含小节标题', /新增/.test(changelogText));
    const itemCount = await page.locator('#changelog-inline .release-items li').count();
    check('渲染出条目', itemCount >= 5, `条目数 ${itemCount}`);
    await page.screenshot({ path: path.join(OUT, '01-设置-更新日志.png'), fullPage: true });

    section('3. 更新渠道与检查');
    await page.waitForFunction(
      () => document.querySelectorAll('#update-source option').length > 0,
      { timeout: 15000 }
    );
    const sources = await page.$$eval('#update-source option', (els) =>
      els.map((e) => ({ value: e.value, text: e.textContent.trim() })));
    check('选择器有「自动」', sources.some((o) => o.value === 'auto'), JSON.stringify(sources));
    check('选择器列出自建源', sources.some((o) => o.value === 'self'), JSON.stringify(sources));
    check('选择器列出 GitHub', sources.some((o) => o.value === 'github'), JSON.stringify(sources));
    check('默认走自动', (await page.inputValue('#update-source')) === 'auto');

    await checkAndWait(page);
    const updateText = await page.textContent('#update-result');
    check('检查有结果反馈', updateText.trim().length > 0, updateText.slice(0, 80));
    const kind = await page.getAttribute('#update-result', 'data-kind');
    check('结果是可读的提示而非异常', !/undefined|NaN|HTTP 5/.test(updateText), updateText.slice(0, 60));
    // 两条渠道都要点名：只报一句笼统的"检查失败"，用户没法判断该修哪一条
    check('逐条报告渠道结果', /自建源/.test(updateText) && /GitHub/.test(updateText), updateText.slice(0, 160));
    check('两条渠道都没塌', !/所有更新源都不可用/.test(updateText), updateText.slice(0, 160));
    const updateHead = await page.textContent('#update-current');
    check('标明结果来自哪条渠道', /自建源|GitHub/.test(updateHead), updateHead);
    console.log(`     结果类型=${kind || 'info'}：${updateText.slice(0, 90)}`);
    await page.screenshot({ path: path.join(OUT, '01b-设置-更新渠道.png'), fullPage: true });

    // 只查一条渠道：既要能强制指定，也要保证选择不会被检查结果重置回「自动」
    await page.selectOption('#update-source', 'self');
    await checkAndWait(page);
    const selfOnly = await page.textContent('#update-result');
    check('限定单条渠道时只报那一条', /自建源/.test(selfOnly) && !/GitHub/.test(selfOnly), selfOnly.slice(0, 140));
    check('选择未被检查结果重置', (await page.inputValue('#update-source')) === 'self');
    await page.selectOption('#update-source', 'auto');

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
