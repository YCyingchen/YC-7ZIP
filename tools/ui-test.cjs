// YC-7ZIP — 浏览器端验收（NAS 主流程）
//
// 用真实 Chromium 打开部署在飞牛 NAS 上的 YC-7ZIP，走一遍"在文件管理器里
// 右键打开"之后用户会走的流程：浏览 NAS 文件 → 分卷压缩写回 NAS →
// 选中间某一卷解压 → 检查控制台。
//
// 用法：
//   $env:PW_MODULE="$env:TEMP\pw\node_modules\playwright"
//   node tools/ui-test.cjs http://192.168.1.9:8090

const fs = require('fs');
const path = require('path');
const os = require('os');

const playwrightPath = process.env.PW_MODULE;
if (!playwrightPath) {
  console.error('请先设置 PW_MODULE 指向 playwright 模块目录');
  process.exit(2);
}
const { chromium } = require(playwrightPath);

// 末尾去掉斜杠，这样拼接 `/api/...` 不会出现双斜杠
const BASE = (process.argv[2] || 'http://192.168.1.9:8090').replace(/\/+$/, '');
const ROOT = process.env.UI_ROOT || '/vol5/1000/空间4/YC-7ZIP/_uitest';
const OUT = process.env.UI_OUT || path.join(os.tmpdir(), 'yc7zip-ui');
fs.mkdirSync(OUT, { recursive: true });

let pass = 0;
let fail = 0;
const failures = [];

function check(name, ok, detail = '') {
  if (ok) {
    pass++;
    console.log(`  [PASS] ${name}`);
  } else {
    fail++;
    failures.push(`${name}${detail ? ' -> ' + detail : ''}`);
    console.log(`  [FAIL] ${name}${detail ? ' -> ' + detail : ''}`);
  }
}

function section(title) { console.log(`\n== ${title}`); }

// 直接问服务端要事实，避免把界面显示当成唯一真相
async function browse(page, dir) {
  const res = await page.request.get(`${BASE}/api/browse?path=${encodeURIComponent(dir)}`);
  return res.json();
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    acceptDownloads: true,
  });
  const page = await context.newPage();

  const consoleErrors = [];
  const pageErrors = [];
  const failedRequests = [];
  page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text()); });
  page.on('pageerror', (e) => pageErrors.push(e.message));
  page.on('requestfailed', (r) => failedRequests.push(`${r.method()} ${r.url()} ${r.failure()?.errorText || ''}`));

  try {
    section('1. 加载与引擎状态');
    await page.goto(BASE + '/', { waitUntil: 'networkidle', timeout: 30000 });
    check('页面标题正确', (await page.title()).includes('YC-7ZIP'), await page.title());

    await page.waitForFunction(() => {
      const d = document.querySelector('#engine-pill .dot');
      return d && d.dataset.state !== 'loading';
    }, { timeout: 20000 });
    check('引擎已就绪', (await page.getAttribute('#engine-pill .dot', 'data-state')) === 'ok');
    check('离线提示已隐藏', await page.isHidden('#offline-banner'));

    await page.waitForSelector('#browser-list .browser-row', { timeout: 15000 });
    const rootRows = await page.locator('#browser-list .browser-row').count();
    check('列出可用根目录', rootRows >= 1, `数量 ${rootRows}`);
    check('默认来源为 NAS 文件', (await page.textContent('#tab-server')).length > 0
      && await page.locator('#tab-server.is-active').count() === 1);

    const rarDisabled = await page.locator('.fmt', { hasText: 'RAR' }).first().isDisabled();
    check('RAR 磁贴被禁用（仅解压）', rarDisabled);
    await page.screenshot({ path: path.join(OUT, '01-浏览NAS.png'), fullPage: true });

    section('2. 进入目录并多选');
    // ?path 指向目录时应当直接进入，而不是要求用户自己一层层点
    await page.goto(`${BASE}/?path=${encodeURIComponent(ROOT)}`, { waitUntil: 'networkidle', timeout: 30000 });
    await page.waitForSelector('#browser-list .browser-row', { timeout: 20000 });
    check('?path 指向目录时自动进入', (await page.textContent('#crumbs')).includes('_uitest'),
      await page.textContent('#crumbs'));

    await page.locator('#browser-list .fname', { hasText: 'src' }).first().click(); // 目录：进入
    await page.waitForTimeout(600);
    const inSrc = await page.textContent('#crumbs');
    check('可进入下一层目录', inSrc.includes('/src'), inSrc);

    // 勾选全部三个条目
    const boxes = page.locator('#browser-list .browser-row input.fcheck');
    check('目录内有 3 个条目', await boxes.count() === 3, `实际 ${await boxes.count()}`);
    for (let i = 0; i < await boxes.count(); i++) await boxes.nth(i).check();
    await page.waitForSelector('#source-panel:not([hidden])', { timeout: 8000 });
    const srcCount = await page.textContent('#source-count');
    check('已选面板显示 3 项', /3/.test(srcCount), srcCount);
    await page.screenshot({ path: path.join(OUT, '02-多选.png'), fullPage: true });

    section('3. 分卷压缩并写回 NAS');
    await page.locator('.fmt', { hasText: '7Z' }).first().click();
    await page.fill('#opt-name', 'uitest-bundle');
    await page.selectOption('#opt-volume', '1m');
    await page.waitForTimeout(300);
    check('分卷面板给出示例名', (await page.textContent('#volume-body')).includes('uitest-bundle.7z.001'),
      await page.textContent('#volume-body'));

    // 输出目录留空会落到来源父目录，这里显式指定 out/
    const outDir = `${ROOT}/out`;
    await page.fill('#opt-output-dir', outDir);
    check('输出方式为写入 NAS', await page.isChecked('#out-server'));

    await page.fill('#opt-output-dir', outDir);
    await page.click('#btn-run');
    await page.waitForSelector('#result-panel:not([hidden])', { timeout: 180000 });
    const summary = await page.textContent('#result-summary');
    check('结果面板出现', summary.length > 0, summary);

    const listedRest = await page.locator('#result-list li').count();
    check('结果列出写出的分卷', listedRest >= 2, `条目 ${listedRest}`);
    check('结果里没有下载按钮（写盘模式）', await page.isHidden('#btn-download-all'));

    // 用服务端事实核对：分卷确实落到磁盘上
    const outEntries = await browse(page, outDir);
    const parts = (outEntries.entries || []).filter((e) => e.name.startsWith('uitest-bundle.7z.'));
    check('磁盘上出现多个分卷', parts.length >= 2, parts.map((p) => p.name).join(', '));
    check('第一卷命名为 .001', parts.some((p) => p.name === 'uitest-bundle.7z.001'),
      parts.map((p) => p.name).join(', '));
    await page.screenshot({ path: path.join(OUT, '03-分卷压缩完成.png'), fullPage: true });

    section('4. 选中分卷中的中间一卷解压');
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.click('#mode-extract');
    await page.waitForTimeout(500);
    check('已切到解压模式', (await page.getAttribute('#mode-extract', 'aria-selected')) === 'true');
    check('压缩格式面板已隐藏', await page.isHidden('#format-panel'));

    // 用面包屑的"上级"回到 _uitest，再进 out 目录挑一卷
    await page.locator('.crumb-up').first().click();
    await page.waitForTimeout(500);
    await page.locator('#browser-list .fname', { hasText: 'out' }).first().click();
    await page.waitForTimeout(600);
    const inOut = await page.textContent('#crumbs');
    check('已进入输出目录', inOut.includes('/out'), inOut);

    const target = parts.find((p) => /\.003$/.test(p.name))
      || parts.find((p) => /\.002$/.test(p.name))
      || parts[0];
    await page.locator('#browser-list .fname', { hasText: target.name }).first().click();
    await page.waitForSelector('#volume-panel:not([hidden])', { timeout: 30000 });

    const volumeText = await page.textContent('#volume-body');
    check('识别为分卷压缩包', volumeText.includes('分卷'), volumeText);
    check('报告分卷总数', /\d+\s*卷/.test(volumeText), volumeText);
    check('选中中间卷不报缺失', !volumeText.includes('缺少'), volumeText);

    await page.waitForSelector('#archive-panel:not([hidden])', { timeout: 60000 });
    const archiveCount = await page.textContent('#archive-count');
    check('从中间卷读出了目录', /[1-9]/.test(archiveCount), archiveCount);
    await page.screenshot({ path: path.join(OUT, '04-分卷解压预览.png'), fullPage: true });

    const extractDir = `${ROOT}/restored`;
    await page.fill('#opt-output-dir', extractDir);
    await page.click('#btn-run');
    await page.waitForSelector('#result-panel:not([hidden])', { timeout: 180000 });
    await page.waitForFunction(() => {
      const s = document.querySelector('#status-text');
      return s && /完成|Finished/.test(s.textContent);
    }, { timeout: 180000 });

    const restored = await browse(page, extractDir);
    const names = (restored.entries || []).map((e) => e.name);
    check('分卷解压结果落盘', names.includes('blob.bin'), names.join(', '));
    const payload = (restored.entries || []).find((e) => e.name === 'blob.bin');
    check('解出的文件大小正确', payload && payload.size === 3000000, payload ? String(payload.size) : 'missing');
    await page.screenshot({ path: path.join(OUT, '05-分卷解压完成.png'), fullPage: true });

    section('5. 界面细节');
    await page.click('#lang-toggle');
    await page.waitForTimeout(250);
    check('语言可切到英文', /Compress|Extract/i.test(await page.textContent('#btn-run-label')),
      await page.textContent('#btn-run-label'));
    await page.click('#lang-toggle');
    await page.waitForTimeout(250);

    const before = await page.getAttribute('html', 'data-theme');
    await page.click('#theme-toggle');
    await page.waitForTimeout(250);
    check('主题可切换', before !== (await page.getAttribute('html', 'data-theme')));
    await page.screenshot({ path: path.join(OUT, '06-浅色主题.png'), fullPage: true });
    await page.click('#theme-toggle');

    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(400);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth
    );
    check('移动端无横向溢出', overflow <= 1, `溢出 ${overflow}px`);
    await page.screenshot({ path: path.join(OUT, '07-移动端.png'), fullPage: true });

    section('6. 被 fnOS 用 ?path 唤起');
    const deep = `${ROOT}/out/${target.name}`;
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`${BASE}/?path=${encodeURIComponent(deep)}`, { waitUntil: 'networkidle', timeout: 30000 });    await page.waitForSelector('#browser-list .browser-row', { timeout: 20000 });
    await page.waitForTimeout(800);
    check('自动切到解压模式', (await page.getAttribute('#mode-extract', 'aria-selected')) === 'true');
    check('自动定位到文件所在目录', (await page.textContent('#crumbs')).includes('out'));
    const pickedCount = await page.textContent('#source-count');
    check('自动预选了该文件', /1/.test(pickedCount), pickedCount);
    await page.screenshot({ path: path.join(OUT, '08-右键唤起.png'), fullPage: true });

    section('7. 控制台与网络');
    check('无 JS 运行时异常', pageErrors.length === 0, pageErrors.join(' | '));
    check('无控制台错误', consoleErrors.length === 0, consoleErrors.join(' | '));
    check('无失败请求', failedRequests.length === 0, failedRequests.join(' | '));
  } catch (err) {
    fail++;
    failures.push(`执行中断：${err.message}`);
    console.log(`  [FAIL] 执行中断：${err.message}`);
    try { await page.screenshot({ path: path.join(OUT, 'error.png'), fullPage: true }); } catch { /* ignore */ }
  } finally {
    await browser.close();
  }

  console.log('\n==============================================');
  if (fail === 0) {
    console.log(`全部通过：${pass} 项`);
    console.log(`截图：${OUT}`);
    process.exit(0);
  }
  console.log(`通过 ${pass} 项，失败 ${fail} 项`);
  failures.forEach((f) => console.log(`  - ${f}`));
  console.log(`截图：${OUT}`);
  process.exit(1);
})();
