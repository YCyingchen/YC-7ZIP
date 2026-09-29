// 地址栏与搜索的验收。
//
//   node tools/nav-test.cjs http://192.168.1.9:8092
//
// 这两件事是"一层层点太慢"的直接解法：粘一个路径进去就能跳；粘贴的若是文件，
// 就退到它的上级并按名字选中——那一步就把"找到那个文件"做完了。搜索则分两层：
// 输入时只在当前目录里筛（不发请求），回车才去子目录里翻。

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
const OUT = process.env.UI_OUT || path.join(os.tmpdir(), 'yc7zip-nav');
fs.mkdirSync(OUT, { recursive: true });

// 素材目录：ui-test 用的那批 fixture 就在这儿
const DIR = '/vol5/1000/空间4/YC-7ZIP/_uitest';
const FILE = DIR + '/media/风景-横向.jpg';

let pass = 0;
const failures = [];
function check(name, ok, detail = '') {
  if (ok) { pass++; console.log(`  \x1b[32m[PASS]\x1b[0m ${name}`); }
  else { failures.push(`${name}${detail ? ' -> ' + detail : ''}`); console.log(`  \x1b[31m[FAIL]\x1b[0m ${name}${detail ? ' -> ' + detail : ''}`); }
}
function section(t) { console.log(`\n\x1b[1;36m== ${t}\x1b[0m`); }

(async () => {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  // 断言是按中文写的，先把界面语言钉死：容器里的浏览器默认 en-US，界面会跟着
  // 变英文，于是"失败"的是断言而不是功能（history-test 同样处理）。
  await page.addInitScript(() => {
    try { localStorage.setItem('yc7zip-lang', 'zh-CN'); } catch (e) { /* 隐私模式 */ }
  });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('requestfailed', (r) => errors.push(`${r.method()} ${r.url()}`));

  const addressBar = () => page.inputValue('#path-input').catch(() => '');
  const listText = () => page.evaluate(() => (document.querySelector('#browser-list').innerText || '').replace(/\s+/g, ' '));

  try {
    section('1. 地址栏');
    await page.goto(BASE + '/', { waitUntil: 'networkidle' });
    await page.waitForTimeout(900);
    check('地址栏已填当前路径', /^\//.test(await addressBar()), await addressBar());

    await page.fill('#path-input', DIR);
    await page.click('#path-go');
    await page.waitForTimeout(1200);
    check('粘目录路径能跳过去', (await addressBar()).includes('空间4'), await addressBar());
    check('跳到的是那个目录', (await listText()).length > 0, (await listText()).slice(0, 70));

    await page.fill('#path-input', FILE);
    await page.click('#path-go');
    // 等结果真的出来，而不是固定等 1.5 秒——页面变重之后固定等待会抢跑，
    // 表现成"功能坏了"，其实只是断言太早。
    await page.waitForFunction(
      () => /已选择\s*[1-9]\s*项/.test(document.body.innerText),
      null,
      { timeout: 8000 },
    ).catch(() => {});
    const picked = await page.evaluate(() => {
      const m = document.body.innerText.replace(/\s+/g, ' ').match(/已选择\s*(\d+)\s*项/);
      return m ? Number(m[1]) : 0;
    });
    check('粘文件路径：退到上级并选中它', picked === 1, `已选择 ${picked} 项`);
    check('地址栏停在文件所在目录', (await addressBar()).includes('media'), await addressBar());
    await page.screenshot({ path: path.join(OUT, '01-地址栏.png'), fullPage: true });

    section('2. 搜索');
    await page.fill('#path-input', DIR);
    await page.click('#path-go');
    await page.waitForTimeout(1000);
    const before = (await listText()).length;
    await page.fill('#search-input', '风景');
    await page.waitForTimeout(400);
    check('输入时先在当前目录里筛', (await listText()).length < before, `${before} → ${(await listText()).length}`);

    await page.press('#search-input', 'Enter');
    await page.waitForTimeout(2000);
    const note = (await page.textContent('#search-note').catch(() => '')) || '';
    check('回车后给出搜索说明', /找到|match/.test(note), note.slice(0, 80));

    const rows = await page.evaluate(() => {
      const list = document.querySelector('#browser-list');
      return {
        names: Array.from(list.querySelectorAll('.fname')).map((e) => e.textContent.trim()),
        wheres: Array.from(list.querySelectorAll('.fwhere')).map((e) => e.textContent.trim()),
      };
    });
    check('搜索有结果', rows.names.length > 0, rows.names.slice(0, 4).join(' '));
    check('命中期望的文件', rows.names.join(' ').includes('风景'), rows.names.join(' '));
    check('每行标出"在哪一层"', rows.wheres.length === rows.names.length, JSON.stringify(rows.wheres));
    await page.screenshot({ path: path.join(OUT, '02-搜索.png'), fullPage: true });

    const clicked = await page.evaluate(() => {
      const w = document.querySelector('#browser-list .fwhere');
      if (!w) return false;
      w.click();
      return true;
    });
    await page.waitForTimeout(1500);
    check('点"在哪一层"会跳过去', clicked);
    check('跳过去之后搜索被清掉（旧结果不成立）', (await page.inputValue('#search-input')) === '');

    section('3. 清除搜索');
    await page.fill('#search-input', '风景');
    await page.press('#search-input', 'Enter');
    await page.waitForTimeout(1500);
    check('搜索时清除按钮可见', await page.isVisible('#search-clear'));
    await page.click('#search-clear');
    await page.waitForTimeout(600);
    check('清除按钮清掉搜索框', (await page.inputValue('#search-input')) === '');
    check('清除后搜索说明也收起来', await page.isHidden('#search-note'));

    section('4. 控制台');
    check('无 JS 运行时异常', errors.length === 0, errors.join(' | ').slice(0, 160));
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
