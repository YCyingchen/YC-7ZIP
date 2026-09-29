// 历史记录的验收。
//
//   PW_MODULE=<playwright 模块目录> node tools/history-test.cjs http://127.0.0.1:18095
//
// 历史是"事后回看"：压缩/解压完成后自动留下一条，面板里能看到来源、输出、
// 用时，还能顺着记录回到现场（打开输出目录）。这里既验记录渲染得对不对，
// 也验真跑一次任务会不会自动记上——后者才是这个功能的主链路。
//
// 需要服务端以 -allow-root 指向素材根目录启动（默认 /tmp/smoke，可用 HIST_ROOT 改）。

const fs = require('fs');
const path = require('path');
const os = require('os');

const playwrightPath = process.env.PW_MODULE;
if (!playwrightPath) {
  console.error('请先设置 PW_MODULE 指向 playwright 模块目录');
  process.exit(2);
}
const { chromium } = require(playwrightPath);

const BASE = (process.argv[2] || 'http://127.0.0.1:18095').replace(/\/+$/, '');
const ROOT = process.env.HIST_ROOT || '/tmp/smoke';
const OUT = process.env.UI_OUT || path.join(os.tmpdir(), 'yc7zip-history');
fs.mkdirSync(OUT, { recursive: true });

let pass = 0;
const failures = [];
function check(name, ok, detail = '') {
  if (ok) { pass++; console.log('  \x1b[32m[PASS]\x1b[0m ' + name); }
  else { failures.push(name + (detail ? ' -> ' + detail : '')); console.log('  \x1b[31m[FAIL]\x1b[0m ' + name + (detail ? ' -> ' + detail : '')); }
}
function section(t) { console.log('\n\x1b[1;36m== ' + t + '\x1b[0m'); }

(async () => {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  // 断言是按中文写的，先把语言钉死：容器的浏览器语言是 en-US，界面上那些
  // 文案会跟着变，否则失败的是断言而不是功能。
  await page.addInitScript(() => {
    try { localStorage.setItem('yc7zip-lang', 'zh-CN'); } catch (e) { /* 隐私模式 */ }
  });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('dialog', (d) => d.accept());

  // 页面里的 fetch 走同源，顺带把 CSRF 那层也过一遍
  const api = (p, opts) => page.evaluate(async ([p, opts]) => {
    const r = await fetch(p, opts || {});
    return r.text();
  }, [p, opts]);

  const items = () => page.$$eval('.history-item', (els) => els.map((e) => ({
    id: e.dataset.id,
    text: (e.innerText || '').replace(/\s+/g, ' '),
    kind: (e.querySelector('.history-kind') || {}).textContent || '',
    status: (e.querySelector('.history-status') || {}).textContent || '',
  })));

  const waitJob = async (id) => {
    let status = '';
    for (let i = 0; i < 80; i++) {
      status = JSON.parse(await api('/api/jobs/' + id)).status;
      if (status === 'done' || status === 'error') break;
      await page.waitForTimeout(300);
    }
    return status;
  };

  try {
    section('0. 准备');
    await page.goto(BASE + '/', { waitUntil: 'networkidle' });
    await api('/api/history', { method: 'DELETE' });

    const job = JSON.parse(await api('/api/jobs?kind=compress', { method: 'POST' }));
    await api('/api/jobs/' + job.id + '/run', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        source_paths: [ROOT + '/in/a.txt', ROOT + '/in/b.txt'],
        output_mode: 'server',
        output_dir: ROOT + '/out',
        format: '7z',
        name: 'ui-history',
      }),
    });
    check('压缩任务跑完', await waitJob(job.id) === 'done');

    section('1. 面板渲染');
    await page.click('#history-toggle');
    await page.waitForTimeout(800);
    check('顶栏按钮能打开历史面板', await page.isVisible('#history-modal'));
    let list = await items();
    check('任务结束后自动留下一条记录', list.length === 1, JSON.stringify(list));
    check('标出这是压缩', list.length === 1 && list[0].kind.includes('压缩'), list[0] && list[0].kind);
    check('标出结果成功', list.length === 1 && list[0].status.includes('成功'), list[0] && list[0].status);
    check('记下了来源路径', list.length === 1 && list[0].text.includes('/in/a.txt'), list[0] && list[0].text.slice(0, 120));
    check('记下了产物名', list.length === 1 && list[0].text.includes('ui-history.7z'));
    check('记下了用时', list.length === 1 && /用时/.test(list[0].text));
    check('顶部显示条数', (await page.innerText('#history-count')).includes('1'));
    await page.screenshot({ path: path.join(OUT, '01-历史面板.png') });

    section('2. 顺着记录回到现场');
    await page.click('.history-item [data-act="dir"]');
    await page.waitForTimeout(1600);
    check('点了「打开输出目录」后面板收起', !(await page.isVisible('#history-modal')));
    const addr = await page.inputValue('#path-input');
    check('列表跳到了输出目录', String(addr).endsWith('/out'), addr);

    section('3. 删除');
    await page.click('#history-toggle');
    await page.waitForTimeout(700);
    await page.click('.history-item [data-act="delete"]');
    await page.waitForTimeout(1000);
    list = await items();
    check('删掉那条后就空了', list.length === 0, String(list.length));
    check('空态有说明文案', (await page.innerText('#history-body')).includes('还没有记录'));

    section('4. 解压也记一条');
    const j2 = JSON.parse(await api('/api/jobs?kind=extract', { method: 'POST' }));
    await api('/api/jobs/' + j2.id + '/run', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        source_paths: [ROOT + '/out/ui-history.7z'],
        output_mode: 'server',
        output_dir: ROOT + '/out2',
      }),
    });
    check('解压任务跑完', await waitJob(j2.id) === 'done');
    await page.click('#history-close');
    await page.click('#history-toggle');
    await page.waitForTimeout(800);
    list = await items();
    check('解压完成也留下记录', list.length === 1 && list[0].kind.includes('解压'), JSON.stringify(list.map((x) => x.kind)));
    check('解压记录里是解出来的文件', list.length === 1 && list[0].text.includes('a.txt'));
    check('解压记录能回到压缩包所在的目录', await page.$('.history-item [data-act="source"]') !== null);
    await page.screenshot({ path: path.join(OUT, '02-解压记录.png') });

    section('5. 清空');
    await page.click('#history-clear');
    await page.waitForTimeout(1000);
    check('清空后列表为空', (await items()).length === 0);

    section('6. 没有脚本错误');
    check('没有 pageerror / 请求失败', errors.length === 0, errors.slice(0, 3).join(' | '));
  } catch (e) {
    check('未捕获异常', false, e.message);
  } finally {
    await browser.close();
  }

  console.log('\n\x1b[1m通过 ' + pass + ' 项，失败 ' + failures.length + ' 项\x1b[0m');
  if (failures.length) {
    console.log('失败项：\n  - ' + failures.join('\n  - '));
    process.exit(1);
  }
})();
