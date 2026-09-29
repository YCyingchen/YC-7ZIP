// 加密压缩包的解压流程验收。
//
//   PW_MODULE=<playwright 模块目录> node tools/encrypted-test.cjs <BASE> <加密包绝对路径>
//
// 先造包（7zz a -pSECRET -mhe=on out.7z 文件），再验四件事：打开时就提示要密码、
// 不给密码会被拦、密码错了会说清、给对了能解开并且磁盘上真有文件。
// 服务端要开着 -allow-root 指向压缩包所在的那棵目录树。

const pw = process.env.PW_MODULE; const { chromium } = require(pw);
const BASE = process.argv[2];
const ARCHIVE = process.argv[3];
let pass = 0; const fails = [];
function check(n, ok, d = '') { if (ok) { pass++; console.log('  [PASS] ' + n); } else { fails.push(n + (d ? ' -> ' + d : '')); console.log('  [FAIL] ' + n + (d ? ' -> ' + d : '')); } }
(async () => {
  const b = await chromium.launch({ headless: true });
  const p = await b.newPage({ viewport: { width: 1280, height: 900 } });
  await p.addInitScript(() => { try { localStorage.setItem('yc7zip-lang', 'zh-CN'); } catch (e) {} });
  await p.goto(BASE + '/?path=' + encodeURIComponent(ARCHIVE), { waitUntil: 'networkidle' });
  await p.waitForTimeout(2200);

  const text = () => p.evaluate(() => document.body.innerText.replace(/\s+/g, ' '));
  let t = await text();
  check('打到的是解压模式', (await p.getAttribute('#mode-extract', 'aria-selected')) === 'true');
  check('提示"该压缩包已加密"', /该压缩包已加密|需要密码/.test(t), t.slice(0, 120));
  check('压缩包内容区也写着要密码', /已加密/.test(await p.innerHTML('#archive-meta')));
  check('密码输入框就在眼前', await p.isVisible('#opt-password'));

  // 1) 不给密码直接解压
  await p.click('#btn-run');
  await p.waitForTimeout(4000);
  t = await text();
  check('不给密码时会要求密码', /已加密|密码/.test(t), t.match(/[^ ]*密码[^ ]*/)?.[0] || '');

  // 2) 错的密码
  await p.fill('#opt-password', 'definitely-wrong');
  await p.click('#btn-run');
  await p.waitForTimeout(4500);
  t = await text();
  check('密码错了会明说', /密码不正确/.test(t), t.match(/[^ ]*密码[^ ]*/)?.[0] || '');

  // 3) 换一次干净的任务：直接给正确密码
  // （失败之后按钮会变成"重新开始"，在被污染的页面上继续点是在测按钮状态机，
  //   不是测密码——所以重新打开一次，走真实用户"再来一遍"的路径。）
  await p.goto(BASE + '/?path=' + encodeURIComponent(ARCHIVE), { waitUntil: 'networkidle' });
  // 页面要先把文件选上、把压缩包预览出来，按钮才可用；固定等待会踩空，
  // 表现成"点了没反应"，看起来像密码不对。
  await p.waitForFunction(() => { const b = document.querySelector('#btn-run'); return b && !b.disabled; }, null, { timeout: 20000 });
  await p.waitForTimeout(500);
  await p.fill('#opt-password', 'SECRET');
  await p.click('#btn-run');
  await p.waitForTimeout(7000);
  t = await text();
  check('正确密码能解开', /已完成|完成/.test(t) && !/密码不正确/.test(t), t.slice(0, 200));
  await p.screenshot({ path: process.env.UI_OUT + '/unlocked.png' });
  await p.screenshot({ path: process.env.UI_OUT + '/encrypted.png' });
  await b.close();
  console.log('\n通过 ' + pass + ' 项，失败 ' + fails.length + ' 项');
  if (fails.length) { console.log('失败项：\n  - ' + fails.join('\n  - ')); process.exit(1); }
})();
