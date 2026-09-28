// 多视口验收：同一个页面在若干典型尺寸下都不能出问题。
//
// 飞牛的应用窗口是可自由缩放的，所以不能只测一个桌面尺寸。这里挑的是
// 实际会见到的几类：超宽、常规桌面、笔记本、被拖条的小窗、平板、手机。
//
//   node tools/viewport-test.cjs http://192.168.1.9:8092

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
const OUT = process.env.UI_OUT || path.join(os.tmpdir(), 'yc7zip-viewport');
fs.mkdirSync(OUT, { recursive: true });

// 名称、宽、高、说明
const VIEWPORTS = [
  ['超宽 2560x1400', 2560, 1400],
  ['桌面 1920x1080', 1920, 1080],
  ['笔记本 1440x900', 1440, 900],
  ['矮窗口 1280x620', 1280, 620],
  ['很矮 1100x480', 1100, 480],
  ['平板 834x1112', 834, 1112],
  ['手机 390x844', 390, 844],
];

let pass = 0;
const failures = [];

function check(name, ok, detail = '') {
  if (ok) {
    pass++;
    console.log(`  \x1b[32m[PASS]\x1b[0m ${name}`);
  } else {
    failures.push(`${name}${detail ? ' -> ' + detail : ''}`);
    console.log(`  \x1b[31m[FAIL]\x1b[0m ${name}${detail ? ' -> ' + detail : ''}`);
  }
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ acceptDownloads: true });
  const page = await context.newPage();

  const pageErrors = [];
  page.on('pageerror', (e) => pageErrors.push(e.message));

  try {
    for (const [label, width, height] of VIEWPORTS) {
      console.log(`\n\x1b[1;36m== ${label}\x1b[0m`);
      await page.setViewportSize({ width, height });
      await page.goto(BASE + '/', { waitUntil: 'networkidle', timeout: 30000 });
      await page.waitForSelector('#browser-list, #browser-gallery', { timeout: 15000 });
      await page.waitForTimeout(400);

      const m = await page.evaluate(() => {
        const doc = document.documentElement;
        const list = document.querySelector('#browser-list');
        const listBox = list && !list.hidden ? list.getBoundingClientRect() : null;
        const actionbar = document.querySelector('.actionbar');
        const runBtn = document.querySelector('#btn-run');
        const runBox = runBtn ? runBtn.getBoundingClientRect() : null;
        const abBox = actionbar ? actionbar.getBoundingClientRect() : null;
        // 主按钮有没有被吸底操作栏盖住
        let covered = false;
        if (runBox && abBox) {
          const cx = runBox.left + runBox.width / 2;
          const cy = runBox.top + runBox.height / 2;
          const hit = document.elementFromPoint(cx, cy);
          covered = !(hit && runBtn.contains(hit));
        }
        return {
          overflowX: doc.scrollWidth - doc.clientWidth,
          docHeight: doc.scrollHeight,
          listHeight: listBox ? Math.round(listBox.height) : 0,
          listVisible: !!listBox,
          runCovered: covered,
          // 首屏能不能看到"开始"按钮所在的操作栏
          actionbarInView: abBox ? abBox.top < window.innerHeight : false,
        };
      });

      check('无横向溢出', m.overflowX <= 1, `溢出 ${m.overflowX}px`);
      check('文件列表可见且有高度', m.listVisible && m.listHeight > 120,
        `高 ${m.listHeight}px`);
      check('主按钮未被遮挡', !m.runCovered);
      check('操作栏在首屏内', m.actionbarInView);

      await page.screenshot({ path: path.join(OUT, `${label.replace(/[^\w]+/g, '_')}.png`), fullPage: true });
    }

    console.log('\n\x1b[1;36m== 控制台\x1b[0m');
    check('无 JS 运行时异常', pageErrors.length === 0, pageErrors.join(' | '));
  } catch (err) {
    failures.push(`执行中断：${err.message}`);
    console.log(`\n  \x1b[31m[FAIL]\x1b[0m 执行中断：${err.message}`);
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
  console.log(`截图：${OUT}`);
  process.exit(1);
})();
