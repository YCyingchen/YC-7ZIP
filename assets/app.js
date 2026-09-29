/* ==========================================================================
   YC-7ZIP — 前端逻辑
   单文件、零依赖。

   主入口是"浏览 NAS 上的文件"：这个应用在飞牛 fnOS 里由文件管理器的
   「打开方式」唤起，fnOS 会在 URL 后面追加 ?path=<绝对路径>，所以启动时
   要能直接吃下这个参数并跳到对应的操作。
   ========================================================================== */
(() => {
  'use strict';

  // ------------------------------------------------------------- 文案表

  const I18N = {
    'zh-CN': {
      tagline: 'NAS 上的压缩与解压', connecting: '连接中…', connected: '7-Zip {v} 就绪',
      engineMissing: '未检测到 7-Zip',
      offlineBanner: '未连接到 YC-7ZIP 服务端 — 当前为界面预览。',
      noAllowRoots: '服务端未开放任何目录：启动时用 -allow-root 指定要操作的目录，才能直接读写 NAS 上的文件。',
      modeCompress: '压缩', modeExtract: '解压',

      srcServer: 'NAS 文件', srcUpload: '本机上传', refresh: '刷新',
      up: '上级', rootLabel: '可用目录', emptyDir: '（空目录）', loading: '读取中…',
      dropTitle: '拖入文件或文件夹到这里', dropTitleArchive: '拖入压缩包到这里',
      dropSub: '也可以点击选择，支持超大文件与整个目录',
      dropSubArchive: '支持 7z / ZIP / RAR / TAR / GZ / XZ / ISO 等格式',
      pickFiles: '选择文件', pickDir: '选择文件夹',

      sourceTitle: '已选择', clearAll: '清空',
      archiveTitle: '压缩包内容', selectAll: '全选', selectNone: '全不选',
      pickOneArchive: '请在左侧选择一个压缩包',

      formatTitle: '压缩格式', optionsTitle: '参数',
      optName: '输出文件名', optLevel: '压缩级别',
      levelFast: '更快', levelBalanced: '均衡', levelMax: '最小体积',
      optPassword: '密码', show: '显示', hide: '隐藏', optEncryptNames: '同时加密文件名',
      optVolume: '分卷大小', volumeNone: '不分卷（单个文件）',
      volumeHint: '分卷后生成 name.7z.001、name.7z.002 …，解压时选中任意一卷即可',
      optPreserve: '保留目录结构', optOverwrite: '覆盖同名文件',
      advanced: '高级选项', optThreads: '线程数', threadsHint: '0 表示由 7-Zip 自动决定',
      optSolid: '固实压缩（7z，体积更小但速度更慢）',
      extractNote: 'RAR 只能解压。RAR 的压缩算法为私有授权，任何第三方软件都无法生成 .rar 文件。',

      outputTitle: '输出位置', outServer: '写入 NAS 目录', outDownload: '下载到本机',
      browseBtn: '选择…', outputSameDir: '与来源相同的目录',

      volumeTitle: '分卷', volumeSet: '这是一个分卷压缩包', volumeCount: '共 {n} 卷',
      volumeFirst: '第一卷', volumeMissing: '缺少 {n} 个分卷', volumeMissingList: '缺少：{list}',
      volumePlain: '单个文件，不是分卷',

      ready: '准备就绪', startCompress: '开始压缩', startExtract: '开始解压',
      startOver: '重新开始', newTask: '新建任务',
      working: '处理中', cancel: '取消', cancelled: '已取消',
      progressHint: '文件正在服务端处理，请勿关闭页面。',
      stageUpload: '上传中', stagePacking: '打包中', stageCompressing: '压缩中', stageDone: '已完成',
      resultTitle: '完成', downloadAll: '下载全部（打包为 ZIP）', downloadResult: '下载结果',
      writtenTo: '已写入 {n} 项到', savedTo: '结果保留 {m} 分钟，超时自动清理',

      noFiles: '请先选择要压缩的文件', noArchive: '请先选择一个压缩包',
      needsPassword: '该压缩包已加密，请输入密码', wrongPassword: '密码不正确，请重试',
      done: '已完成', failed: '处理失败', offlineAction: '服务端未连接，无法执行',
      errorTitle: '出错了', copyError: '复制报错信息', errorDetail: '环境信息（报错时一并贴给对方）',
      errorCopied: '报错信息已复制', errorCopyFailed: '复制失败，请手动选中上面的文字',
      reportIssue: '在 GitHub 提交 Issue', repo: '项目仓库 / 反馈问题',
      settingsTitle: '设置', updateTitle: '版本与更新', wallpaperTitle: '壁纸', changelogTitle: '更新日志',
      checkUpdate: '检查更新', applyUpdate: '在线更新', localUpdate: '本地更新', checking: '检查中…', updating: '更新中…',
      currentVersion: '当前版本', channelLabel: '通道', deployMethod: '部署方式',
      methodBinary: '裸二进制', methodContainer: '容器', methodPackage: '飞牛应用包',
      alreadyLatest: '已是最新版本（{v}）', newVersionFound: '发现新版本：',
      selfUpdateOk: '可以在线更新：下载后会校验 SHA256 并原子替换，旧的会备份为 .bak。',
      confirmUpdate: '确定要下载并替换当前程序吗？完成后服务会自动重启。',
      updateInstalled: '已更新到', restarting: '进程正在重启，稍后刷新页面即可。',
      updateSourceLabel: '更新渠道', sourceAuto: '自动（全部渠道）',
      sourceSelf: '自建源', sourceGitHub: 'GitHub',
      sourceResult: '各渠道本次结果', sourceFailed: '这条不通',
      wallpaperHint: '上传一张图片即可。暗化是保证文字可读的关键。',
      wallpaperNone: '未设置壁纸', wallpaperUpload: '上传图片',
      wallpaperClear: '清除', wallpaperDim: '暗化', wallpaperBlur: '模糊', wallpaperFit: '铺满方式',
      wallpaperPanel: '面板透明', wallpaperPanelHint: '调高后页面上的面板会透出壁纸（默认 8% 那点透）；文字可读性靠上面的「暗化」保证。',
      wallpaperEnabled: '启用壁纸', wallpaperSaved: '壁纸已更新', wallpaperCleared: '壁纸已清除',
      fitCover: '铺满裁切', fitContain: '完整显示', fitTile: '平铺',
      changelogUnavailable: '暂时读不到更新日志',
      taskDoneHint: '已完成，点击查看结果', taskIdle: '无任务',
      filterAll: '全部', filterImage: '图片', filterVideo: '视频', filterArchive: '压缩包', filterDoc: '文档',
      viewList: '列表', viewGrid: '图览', noMatch: '当前筛选下没有匹配的文件',
      preview: '预览', selectThis: '选中这个', deselectThis: '取消选中', openOriginal: '新窗口打开',
      kindImage: '图片', kindVideo: '视频', kindArchive: '压缩包', kindDoc: '文档', kindOther: '文件',
      pickedHint: '点击缩略图选中／取消；右上角按钮看大图',
      itemsCount: '{n} 项', totalSize: '总大小', packedSize: '压缩后', ratio: '压缩率',
      enterDir: '进入目录', selectDir: '选择此目录',
      pickerTitle: '选择输出目录', pickerConfirm: '用此目录', close: '关闭',

      pathPlaceholder: '/vol1/1000/…', pathGo: '前往',
      pathNotAllowed: '这个位置不在允许访问的范围内',
      searchPlaceholder: '搜索文件…', searchGo: '搜索', searchClear: '清除搜索',
      searchHint: '回车搜所有子目录；输入时先在当前目录里筛',
      searchResultTitle: '在 {path} 下找到 {n} 项',
      searchTruncated: '结果很多，只列了前 {n} 项',
      searchEmpty: '没找到匹配的文件',
      searchAtRoot: './',
    },
    en: {
      tagline: 'Compress & extract on your NAS', connecting: 'Connecting…', connected: '7-Zip {v} ready',
      engineMissing: '7-Zip not found',
      offlineBanner: 'Not connected to a YC-7ZIP server — interface preview only.',
      noAllowRoots: 'No directories are exposed: start the server with -allow-root to work on files directly on the NAS.',
      modeCompress: 'Compress', modeExtract: 'Extract',

      srcServer: 'NAS files', srcUpload: 'Upload', refresh: 'Refresh',
      up: 'Up', rootLabel: 'Allowed roots', emptyDir: '(empty)', loading: 'Loading…',
      dropTitle: 'Drop files or folders here', dropTitleArchive: 'Drop an archive here',
      dropSub: 'Or click to browse — large files and whole folders are fine',
      dropSubArchive: 'Supports 7z / ZIP / RAR / TAR / GZ / XZ / ISO and more',
      pickFiles: 'Choose files', pickDir: 'Choose folder',

      sourceTitle: 'Selected', clearAll: 'Clear',
      archiveTitle: 'Archive contents', selectAll: 'All', selectNone: 'None',
      pickOneArchive: 'Pick an archive on the left',

      formatTitle: 'Format', optionsTitle: 'Options',
      optName: 'Output name', optLevel: 'Compression level',
      levelFast: 'Faster', levelBalanced: 'Balanced', levelMax: 'Smallest',
      optPassword: 'Password', show: 'Show', hide: 'Hide', optEncryptNames: 'Also encrypt file names',
      optVolume: 'Split volume size', volumeNone: 'No split (single file)',
      volumeHint: 'Splitting produces name.7z.001, name.7z.002 …; select any part to extract',
      optPreserve: 'Preserve folder structure', optOverwrite: 'Overwrite existing files',
      advanced: 'Advanced', optThreads: 'Threads', threadsHint: '0 lets 7-Zip decide',
      optSolid: 'Solid block (7z only, smaller but slower)',
      extractNote: 'RAR is extract-only. The RAR compressor is proprietary, so no third-party tool can create .rar files.',

      outputTitle: 'Output', outServer: 'Write to a NAS folder', outDownload: 'Download to this device',
      browseBtn: 'Browse…', outputSameDir: 'Same folder as the source',

      volumeTitle: 'Volumes', volumeSet: 'This is a split archive', volumeCount: '{n} parts',
      volumeFirst: 'First part', volumeMissing: '{n} parts missing', volumeMissingList: 'Missing: {list}',
      volumePlain: 'A single file, not a split archive',

      ready: 'Ready', startCompress: 'Start compressing', startExtract: 'Start extracting',
      startOver: 'Start over', newTask: 'New task',
      working: 'Working', cancel: 'Cancel', cancelled: 'Cancelled',
      progressHint: 'The server is processing your files. Keep this page open.',
      stageUpload: 'Uploading', stagePacking: 'Packing', stageCompressing: 'Compressing', stageDone: 'Finished',
      resultTitle: 'Finished', downloadAll: 'Download all (as ZIP)', downloadResult: 'Download result',
      writtenTo: '{n} item(s) written to', savedTo: 'Results are kept for {m} minutes',

      noFiles: 'Choose the files you want to compress first', noArchive: 'Choose an archive first',
      needsPassword: 'This archive is encrypted — enter the password', wrongPassword: 'Wrong password, please try again',
      done: 'Finished', failed: 'Failed', offlineAction: 'Server not connected',
      errorTitle: 'Something went wrong', copyError: 'Copy error report',
      errorDetail: 'Environment (paste this along with the error)',
      errorCopied: 'Error report copied', errorCopyFailed: 'Copy failed — select the text above manually',
      reportIssue: 'Open a GitHub issue', repo: 'Repository / report a bug',
      settingsTitle: 'Settings', updateTitle: 'Version & updates', wallpaperTitle: 'Wallpaper', changelogTitle: 'Changelog',
      checkUpdate: 'Check for updates', applyUpdate: 'Update now', localUpdate: 'Update from file',
      checking: 'Checking…', updating: 'Updating…',
      currentVersion: 'Current', channelLabel: 'Channel', deployMethod: 'Deployed as',
      methodBinary: 'binary', methodContainer: 'container', methodPackage: 'fnOS package',
      alreadyLatest: 'Already up to date ({v})', newVersionFound: 'New version available:',
      selfUpdateOk: 'In-place update available: the download is verified with SHA256 and swapped atomically; the old binary is kept as .bak.',
      confirmUpdate: 'Download and replace the running binary? The service restarts afterwards.',
      updateInstalled: 'Updated to', restarting: 'The process is restarting — reload the page in a moment.',
      updateSourceLabel: 'Source', sourceAuto: 'Automatic (all sources)',
      sourceSelf: 'Self-hosted', sourceGitHub: 'GitHub',
      sourceResult: 'Source results', sourceFailed: 'unavailable',
      wallpaperHint: 'Upload an image. Dimming is what keeps the text readable.',
      wallpaperNone: 'No wallpaper set', wallpaperUpload: 'Upload image',
      wallpaperClear: 'Clear', wallpaperDim: 'Dim', wallpaperBlur: 'Blur', wallpaperFit: 'Scaling',
      wallpaperPanel: 'Panel opacity', wallpaperPanelHint: 'Higher values let the wallpaper show through the panels (8% by default); keep the text readable with Dim above.',
      wallpaperEnabled: 'Enable wallpaper', wallpaperSaved: 'Wallpaper updated', wallpaperCleared: 'Wallpaper cleared',
      fitCover: 'Cover', fitContain: 'Contain', fitTile: 'Tile',
      changelogUnavailable: 'Changelog unavailable',
      taskDoneHint: 'Done — click to view', taskIdle: 'No task',
      filterAll: 'All', filterImage: 'Images', filterVideo: 'Videos', filterArchive: 'Archives', filterDoc: 'Documents',
      viewList: 'List', viewGrid: 'Gallery', noMatch: 'Nothing matches the current filter',
      preview: 'Preview', selectThis: 'Select this', deselectThis: 'Deselect', openOriginal: 'Open original',
      kindImage: 'image', kindVideo: 'video', kindArchive: 'archive', kindDoc: 'doc', kindOther: 'file',
      pickedHint: 'Click a tile to select; use the corner button to preview',
      itemsCount: '{n} item(s)', totalSize: 'Total', packedSize: 'Packed', ratio: 'Ratio',
      enterDir: 'Open folder', selectDir: 'Use this folder',
      pickerTitle: 'Choose the output folder', pickerConfirm: 'Use this folder', close: 'Close',

      pathPlaceholder: '/vol1/1000/…', pathGo: 'Go',
      pathNotAllowed: 'That location is outside the allowed folders',
      searchPlaceholder: 'Find a file…', searchGo: 'Search', searchClear: 'Clear search',
      searchHint: 'Enter searches every subfolder; typing filters this folder',
      searchResultTitle: '{n} match(es) under {path}',
      searchTruncated: 'Too many matches — showing the first {n}',
      searchEmpty: 'No matching files',
      searchAtRoot: './',
    },
  };

  // 服务端不可用时用于界面预览的兜底格式表
  const FALLBACK_FORMATS = [
    { id: '7z', label: '7-Zip', extension: '.7z', capability: 1, supports_password: true, supports_volume: true, supports_level: true, note: '压缩率最高，支持 AES-256 加密与文件名加密' },
    { id: 'zip', label: 'ZIP', extension: '.zip', capability: 1, supports_password: true, supports_volume: true, supports_level: true, note: 'Windows / macOS 原生支持，兼容性最好' },
    { id: 'tar', label: 'TAR', extension: '.tar', capability: 1, supports_password: false, supports_volume: false, supports_level: false, note: '仅打包不压缩，保留 Unix 权限位' },
    { id: 'gzip', label: 'GZIP', extension: '.gz', capability: 1, supports_password: false, supports_volume: false, supports_level: true, note: '单文件压缩，常用于 .tar.gz' },
    { id: 'bzip2', label: 'BZIP2', extension: '.bz2', capability: 1, supports_password: false, supports_volume: false, supports_level: true, note: '老牌高压缩率算法，速度较慢' },
    { id: 'xz', label: 'XZ', extension: '.xz', capability: 1, supports_password: false, supports_volume: false, supports_level: true, note: 'Linux 发行版常用，压缩率高' },
    { id: 'zstd', label: 'Zstandard', extension: '.zst', capability: 1, supports_password: false, supports_volume: false, supports_level: true, note: '现代算法，速度与压缩率兼顾' },
    { id: 'rar', label: 'RAR', extension: '.rar', capability: 0, supports_password: false, supports_volume: false, supports_level: false, note: '仅可解压：RAR 压缩算法为私有授权，7-Zip 无法生成' },
    { id: 'iso', label: 'ISO', extension: '.iso', capability: 0, supports_password: false, supports_volume: false, supports_level: false, note: '光盘镜像，仅可解压' },
    { id: 'cab', label: 'CAB', extension: '.cab', capability: 0, supports_password: false, supports_volume: false, supports_level: false, note: 'Windows 安装包容器，仅可解压' },
    { id: 'wim', label: 'WIM', extension: '.wim', capability: 0, supports_password: false, supports_volume: false, supports_level: false, note: 'Windows 映像，仅可解压' },
    { id: 'cpio', label: 'CPIO', extension: '.cpio', capability: 0, supports_password: false, supports_volume: false, supports_level: false, note: 'initramfs 容器，仅可解压' },
  ];

  const CREATE_CAPABLE = 1;

  // 判定"点开就是压缩包"，用来决定 fnOS 带 ?path 唤起时该进哪个模式
  const ARCHIVE_SUFFIXES = [
    '.7z', '.zip', '.rar', '.tar', '.gz', '.tgz', '.bz2', '.xz', '.zst', '.zstd',
    '.iso', '.cab', '.wim', '.cpio', '.lzma', '.lz', '.arj', '.lzh', '.z',
    '.deb', '.rpm', '.dmg', '.img', '.apk', '.squashfs',
  ];

  // 分卷命名：name.7z.001 / name.part3.rar / name.r01 / name.z02。
  // 服务端引擎用同一套规则解析，两边必须一致，否则 ?path 指向 .002 时会进错模式。
  function isArchiveName(name) {
    const lower = String(name).toLowerCase();
    if (ARCHIVE_SUFFIXES.some((s) => lower.endsWith(s))) return true;

    const numeric = lower.match(/\.(\d{3,4})$/);
    if (numeric) {
      const base = lower.slice(0, -numeric[0].length);
      if (ARCHIVE_SUFFIXES.some((s) => base.endsWith(s))) return true;
    }
    if (/\.part\d{1,3}\.rar$/.test(lower)) return true;
    if (/\.r\d{2,3}$/.test(lower)) return true;
    if (/\.z\d{2,3}$/.test(lower)) return true;
    return false;
  }

  // 图览用它决定要不要去取缩略图，以及筛选器按什么归类
  const KIND_EXT = {
    image: ['jpg', 'jpeg', 'jpe', 'png', 'gif', 'webp', 'bmp', 'avif', 'heic', 'heif', 'tif', 'tiff', 'svg'],
    video: ['mp4', 'm4v', 'mov', 'webm', 'mkv', 'avi', 'wmv', 'flv', 'ts', 'mpg', 'mpeg'],
    doc: ['pdf', 'txt', 'md', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'csv', 'json', 'xml', 'log', 'ini', 'conf', 'yml', 'yaml'],
  };

  function kindOf(name) {
    const lower = String(name).toLowerCase();
    const ext = lower.includes('.') ? lower.slice(lower.lastIndexOf('.') + 1) : '';
    if (KIND_EXT.image.includes(ext)) return 'image';
    if (KIND_EXT.video.includes(ext)) return 'video';
    if (isArchiveName(lower)) return 'archive';
    if (KIND_EXT.doc.includes(ext)) return 'doc';
    return 'other';
  }

  function kindLabel(kind) {
    return t({ image: 'kindImage', video: 'kindVideo', archive: 'kindArchive', doc: 'kindDoc' }[kind] || 'kindOther');
  }

  function thumbURL(path, w) {
    return apiPath(`/api/thumb?path=${encodeURIComponent(path)}&w=${w || 320}`);
  }

  function rawURL(path) {
    return apiPath(`/api/raw?path=${encodeURIComponent(path)}`);
  }

  // --------------------------------------------------------------- 状态

  const state = {
    lang: 'zh-CN',
    mode: 'compress',
    source: 'server',
    online: false,
    health: null,
    formats: FALLBACK_FORMATS,
    format: '7z',

    // NAS 浏览
    browserRoots: [],
    browserPath: '',
    browserEntries: [],
    /** 视图：列表 / 图览 */
    view: 'list',
    /** 类型筛选：all / image / video / archive / doc */
    typeFilter: 'all',
    /** 搜索框里的字：非空时先在当前目录里即时筛一遍 */
    searchQuery: '',
    /** 递归搜索的结果；null 表示"没在搜"，此时列表就是当前目录 */
    searchResults: null,
    /** 递归搜索的元信息：搜索根、是否被截断、扫了多少条 */
    searchMeta: null,
    /** 搜索请求序号：回来晚的那次不再覆盖新的结果 */
    searchSeq: 0,
    /** 选中的 NAS 路径（压缩可多选，解压只有一个） */
    picked: new Map(),

    // 本机上传
    uploads: [],
    uploadArchive: null,

    // 解压
    archive: null,
    archivePath: '',
    selectedMembers: null,

    // 输出
    outputMode: 'server',
    outputDir: '',

    // 任务
    jobId: null,
    job: null,
    pollTimer: null,
    uploadXHR: null,
    result: null,
    /** 最近一次失败的完整信息，用于渲染与复制 */
    lastError: null,
    /** 项目仓库地址，由服务端下发，用于顶栏链接与 Issue 预填 */
    repoUrl: 'https://github.com/YCyingchen/YC-7ZIP',

    // 目录选择器用途
    pickerFor: 'source',
    pickerSelected: '',
  };

  const $ = (id) => document.getElementById(id);
  const t = (key, vars) => {
    let s = (I18N[state.lang] && I18N[state.lang][key]) || (I18N['zh-CN'][key]) || key;
    if (vars) for (const [k, v] of Object.entries(vars)) s = s.replaceAll(`{${k}}`, v);
    return s;
  };

  // ------------------------------------------------------------- 工具

  function humanBytes(n) {
    if (!n) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
    const v = n / Math.pow(1024, i);
    return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, (c) => (
      { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
    ));
  }

  function toast(message, kind = 'info', ms = 4500) {
    const el = document.createElement('div');
    el.className = 'toast';
    el.dataset.kind = kind;
    el.textContent = message;
    $('toast-host').appendChild(el);
    setTimeout(() => el.remove(), ms);
  }

  // 页面可能挂在 /app/yc7zip/ 之下（飞牛应用网关），所以所有请求都要拼接
  // 当前页面所在的前缀，而不是写死以 / 开头的绝对路径。
  const API_BASE = new URL('.', location.href).pathname;

  function apiPath(p) {
    return API_BASE + String(p).replace(/^\/+/, '');
  }

  async function api(path, options = {}, timeoutMs = 30000) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const res = await fetch(apiPath(path), { ...options, signal: controller.signal });
      const text = await res.text();
      let data = null;
      if (text) { try { data = JSON.parse(text); } catch { data = { raw: text }; } }
      if (!res.ok) {
        const err = new Error((data && data.error) || `HTTP ${res.status}`);
        err.status = res.status;
        err.data = data;
        throw err;
      }
      return data;
    } finally {
      clearTimeout(timer);
    }
  }

  function postJSON(path, body, timeoutMs) {
    return api(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {}),
    }, timeoutMs);
  }

  function fileIcon(isDir) {
    return isDir
      ? '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z"/>'
      : '<path d="M14 3v5h5M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8Z"/>';
  }

  function svgIcon(isDir, cls) {
    return `<svg class="fi ${isDir ? 'dir' : ''} ${cls || ''}" viewBox="0 0 24 24" fill="none" `
      + `stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">`
      + fileIcon(isDir) + '</svg>';
  }

  // ------------------------------------------------------------- 主题 / 语言
  function applyTheme(theme) {
    document.documentElement.dataset.theme = theme;
    try { localStorage.setItem('yc7zip-theme', theme); } catch { /* 隐私模式 */ }
  }

  function initTheme() {
    let saved = null;
    try { saved = localStorage.getItem('yc7zip-theme'); } catch { /* ignore */ }
    const prefersLight = window.matchMedia('(prefers-color-scheme: light)').matches;
    applyTheme(saved || (prefersLight ? 'light' : 'dark'));
    $('theme-toggle').addEventListener('click', () => {
      applyTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark');
    });
  }

  function initLang() {
    let saved = null;
    try { saved = localStorage.getItem('yc7zip-lang'); } catch { /* ignore */ }
    state.lang = saved || (navigator.language && navigator.language.startsWith('zh') ? 'zh-CN' : 'en');
    applyLang();
    $('lang-toggle').addEventListener('click', () => {
      state.lang = state.lang === 'zh-CN' ? 'en' : 'zh-CN';
      try { localStorage.setItem('yc7zip-lang', state.lang); } catch { /* ignore */ }
      applyLang();
    });
  }

  function applyLang() {
    document.documentElement.lang = state.lang;
    $('lang-toggle').textContent = state.lang === 'zh-CN' ? 'EN' : '中文';
    document.querySelectorAll('[data-i18n]').forEach((el) => { el.textContent = t(el.dataset.i18n); });
    // 地址栏与搜索框只有 placeholder，没有可见文字，所以要单独跟着语言走
    // （顺带把 aria-label 也设上，不然读屏软件读到的还是硬编码的中文）。
    document.querySelectorAll('[data-i18n-placeholder]').forEach((el) => {
      const text = t(el.dataset.i18nPlaceholder);
      el.placeholder = text;
      el.setAttribute('aria-label', text);
    });
    document.querySelectorAll('[data-i18n-title]').forEach((el) => { el.title = t(el.dataset.i18nTitle); });
    renderEnginePill();
    renderDropCopy();
    renderFormats();
    renderBrowser();
    renderPicked();
    renderArchive();
    renderVolume();
    renderResult();
    renderRunButton();
    $('repo-link').title = t('repo');
    // 设置面板里的更新渠道选择器也是按语言拼的，切语言要跟着重画
    if (settingsState.update) renderUpdateStatus(settingsState.update);
    // 任务指示器是常驻的：初始化与切语言都要把空闲态画出来。
    // 只在"本来就是空闲"时刷，免得正在跑的任务被切语言这一下弄没。
    // 注意 dataset.state 没设过时是 undefined，不是空串。
    const pillState = $('task-pill').dataset.state;
    if (!pillState || pillState === 'idle') {
      updateTaskPill(null);
    }
    if (!state.online) $('offline-banner').hidden = false;
  }

  // --------------------------------------------------------- 服务端状态

  async function checkHealth() {
    try {
      const health = await api('/api/health', {}, 8000);
      state.online = true;
      state.health = health;
      const roots = Array.isArray(health.allow_roots) ? health.allow_roots : [];
      // 存成与 /api/browse 的 roots 同形状的对象：根目录视图就是照它渲染的，
      // 直接放字符串会让每一行没有名字（只剩一个复选框和一个 0 B）。
      state.browserRoots = roots.map((p) => ({ name: p, path: p, is_dir: true, size: 0 }));
      if (health.repo_url) {
        state.repoUrl = trimSlash(health.repo_url);
        $('repo-link').href = state.repoUrl;
      }
      try {
        const data = await api('/api/formats', {}, 8000);
        if (data && Array.isArray(data.formats)) state.formats = data.formats;
      } catch { /* 用兜底表 */ }

      if (roots.length === 0) {
        // 没有开放目录时只能上传，直接切过去，避免给用户一个空浏览器
        $('offline-banner').hidden = false;
        $('offline-text').textContent = t('noAllowRoots');
        if (state.source === 'server' && state.picked.size === 0) setSource('upload');
      } else {
        $('offline-banner').hidden = true;
      }
      if (!state.browserPath) await loadDir(roots[0]);
    } catch {
      state.online = false;
      $('offline-banner').hidden = false;
      $('offline-text').textContent = t('offlineBanner');
      setSource('upload');
    }
    renderEnginePill();
    renderFormats();
    renderRunButton();
  }

  function renderEnginePill() {
    const dot = $('engine-pill').querySelector('.dot');
    const text = $('engine-text');
    const eng = state.health && state.health.engine;
    if (!state.online || !eng || !eng.path) {
      dot.dataset.state = 'error';
      text.textContent = t('engineMissing');
      $('engine-pill').title = state.online ? '' : t('offlineBanner');
      return;
    }
    dot.dataset.state = 'ok';
    text.textContent = t('connected', { v: eng.version || '' });
    const self = (state.health && state.health.version) || '';
    $('engine-pill').title = self
      ? `YC-7ZIP ${self}\n7-Zip: ${eng.path}`
      : `7-Zip: ${eng.path}`;
  }

  // ------------------------------------------------------------- 模式 / 来源

  function setMode(mode) {
    if (state.mode === mode) return;
    state.mode = mode;
    state.picked.clear();
    state.uploads = [];
    state.uploadArchive = null;
    state.archive = null;
    state.archivePath = '';
    state.selectedMembers = null;
    state.result = null;
    state.lastError = null;
    $('result-panel').hidden = true;
    $('error-panel').hidden = true;

    $('mode-compress').classList.toggle('is-active', mode === 'compress');
    $('mode-extract').classList.toggle('is-active', mode === 'extract');
    $('mode-compress').setAttribute('aria-selected', String(mode === 'compress'));
    $('mode-extract').setAttribute('aria-selected', String(mode === 'extract'));

    const compressing = mode === 'compress';
    $('format-panel').hidden = !compressing;
    $('field-level').hidden = !compressing;
    $('field-volume-size').hidden = !compressing;
    $('field-name').hidden = !compressing;
    $('extract-note').hidden = compressing;
    state.format = compressing && canCreate(state.format) ? state.format : (compressing ? '7z' : state.format);

    renderDropCopy();
    renderFormats();
    syncOptions();
    renderBrowser();
    renderPicked();
    renderArchive();
    renderVolume();
    renderRunButton();
    setStatus(t('ready'));
  }

  function setSource(source) {
    state.source = source;
    state.picked.clear();
    $('tab-server').classList.toggle('is-active', source === 'server');
    $('tab-upload').classList.toggle('is-active', source === 'upload');
    $('tab-server').setAttribute('aria-selected', String(source === 'server'));
    $('tab-upload').setAttribute('aria-selected', String(source === 'upload'));
    $('browser-wrap').hidden = source !== 'server';
    $('dropzone').hidden = source !== 'upload';
    // 上传的文件在服务端工作区里，写不回 NAS，所以只能下载
    if (source === 'upload') setOutputMode('download');
    renderPicked();
    renderRunButton();
  }

  function canCreate(id) {
    const f = state.formats.find((x) => x.id === id);
    return f && f.capability === CREATE_CAPABLE;
  }

  function currentFormat() {
    return state.formats.find((f) => f.id === state.format) || state.formats[0];
  }

  function setView(view) {
    state.view = view;
    $('view-list').classList.toggle('is-active', view === 'list');
    $('view-grid').classList.toggle('is-active', view === 'grid');
    try { localStorage.setItem('yc7zip-view', view); } catch { /* 隐私模式 */ }
    renderBrowser();
  }

  function renderDropCopy() {
    const extract = state.mode === 'extract';
    $('drop-title').textContent = t(extract ? 'dropTitleArchive' : 'dropTitle');
    $('drop-sub').textContent = t(extract ? 'dropSubArchive' : 'dropSub');
  }

  // --------------------------------------------------------- NAS 文件浏览

  // 跳转成功返回 true。地址栏要靠这个返回值判断"这个路径到底打没打开"，
  // 好在打不开时退回它的上级目录再去找文件名。
  async function loadDir(path) {
    if (!state.online) return false;
    $('browser-list').innerHTML = `<li class="muted-row">${escapeHtml(t('loading'))}</li>`;
    try {
      const data = await api(`/api/browse?path=${encodeURIComponent(path || '')}`, {}, 20000);
      if (!data.enabled) {
        $('browser-list').innerHTML = `<li class="muted-row">${escapeHtml(t('noAllowRoots'))}</li>`;
        state.browserEntries = [];
        renderCrumbs('');
        return false;
      }
      const requested = String(path || '');
      state.browserPath = data.path || '';
      state.browserEntries = data.path ? (data.entries || []) : [];
      // 服务端给的那份才是权威的（允许清单运行时会变）。之前只在"有 path"时
      // 才更新，于是退回根视图时用的还是初始化那一份，甚至还是字符串。
      if (data.roots && data.roots.length) state.browserRoots = data.roots;
      // 换了目录，上一次的搜索结果就不成立了（它在别处找出来的东西）
      clearSearch();
      syncPathInput();
      renderCrumbs(data.path || '', data.parent || '', data.roots || []);
      renderBrowser();
      if (requested && normPath(requested) !== normPath(state.browserPath)) {
        // 服务端把越界或读不了的路径退回成了"可用目录"那一层。不说明的话就是
        // 地址栏敲了回车、界面自己跳走了，看着像坏了。
        toast(t('pathNotAllowed'), 'warn');
      }
      return true;
    } catch (err) {
      $('browser-list').innerHTML = `<li class="muted-row">${escapeHtml(err.message)}</li>`;
      return false;
    }
  }

  // ---------------------------------------------- 地址栏与搜索

  // 路径比较用的归一化：结尾斜杠、Windows 的反斜杠都不该算"路径变了"
  function normPath(p) {
    return String(p || '').replace(/\\/g, '/').replace(/\/+$/, '');
  }

  function syncPathInput() {
    // 服务端回来的才是权威路径（它会把越界/不存在的路径回退掉、也会做规范化），
    // 所以每次跳转完都把地址栏对齐到它。
    const input = $('path-input');
    if (input) input.value = state.browserPath || '';
  }

  // 清掉搜索：结果、元信息、输入框里的字一起清，三者必须同步，
  // 否则会出现"框里还有字、列表却是完整目录"这种自相矛盾的画面。
  function clearSearch() {
    state.searchSeq++; // 让在途的搜索请求回来时作废
    state.searchQuery = '';
    state.searchResults = null;
    state.searchMeta = null;
    const input = $('search-input');
    if (input) input.value = '';
    syncSearchChrome();
  }

  function syncSearchChrome() {
    const clear = $('search-clear');
    if (clear) clear.hidden = !state.searchQuery;
    const note = $('search-note');
    if (!note) return;
    if (state.searchResults === null) {
      note.hidden = true;
      note.textContent = '';
      return;
    }
    const meta = state.searchMeta || {};
    const where = meta.path || t('rootLabel');
    let text = t('searchResultTitle', { n: String(state.searchResults.length), path: where });
    if (meta.truncated) text += ' · ' + t('searchTruncated', { n: String(state.searchResults.length) });
    note.hidden = false;
    note.textContent = text;
  }

  // 地址栏回车：先问服务端这是什么。
  //
  // 粘进来的多半是"某个文件的完整路径"（从文件管理器复制），所以打不开目录时
  // 退到它的上级、把文件名填进搜索框并选中——这一步就把"找到那个文件"做完了。
  async function gotoPath(raw) {
    const p = String(raw || '').trim();
    if (!p) { await loadDir(''); return; }
    let info = null;
    try {
      info = await api(`/api/inspect?path=${encodeURIComponent(p)}`, {}, 15000);
    } catch {
      toast(t('pathNotAllowed'), 'warn');
      return;
    }
    if (info && info.is_dir) {
      await loadDir(p);
      return;
    }
    const cut = p.replace(/[\\/][^\\/]*$/, '');
    const base = p.slice(cut.length).replace(/^[\\/]+/, '');
    if (!cut || !base) { await loadDir(p); return; }
    if (!await loadDir(cut)) { toast(t('pathNotAllowed'), 'warn'); return; }
    state.searchQuery = base;
    $('search-input').value = base;
    selectByName(base);
    renderBrowser();
  }

  // 按名字把当前目录里的那一个选中（地址栏粘文件路径时用）
  function selectByName(name) {
    const hit = (state.browserEntries || []).find(
      (e) => !e.is_dir && String(e.name).toLowerCase() === String(name).toLowerCase()
    );
    if (hit) togglePick(hit, true);
  }

  async function runSearch() {
    const q = state.searchQuery.trim();
    if (!q || !state.online) { state.searchResults = null; state.searchMeta = null; renderBrowser(); return; }
    const seq = ++state.searchSeq;
    try {
      const data = await api(
        `/api/search?path=${encodeURIComponent(state.browserPath || '')}&q=${encodeURIComponent(q)}`,
        {}, 30000
      );
      if (seq !== state.searchSeq) return; // 又敲了一次，这次结果已经过期
      state.searchResults = data.entries || [];
      state.searchMeta = {
        path: data.path || '', truncated: !!data.truncated, scanned: data.scanned || 0,
      };
    } catch (err) {
      if (seq !== state.searchSeq) return;
      toast(err.message, 'warn');
      state.searchResults = null;
      state.searchMeta = null;
    }
    renderBrowser();
  }

  function renderCrumbs(path, parent, roots) {
    const host = $('crumbs');
    host.innerHTML = '';
    if (!path) {
      const label = document.createElement('span');
      label.className = 'crumb is-current';
      label.textContent = t('rootLabel');
      host.appendChild(label);
      return;
    }
    const parts = path.split('/').filter(Boolean);
    const startIdx = hostsPrefix(parts, roots);

    const rootCrumb = document.createElement('button');
    rootCrumb.type = 'button';
    rootCrumb.className = 'crumb crumb-btn';
    // 这一跳的标签要写完整的根路径：允许的根常常在 /vol1/1000 这种深度，
    // 只写第一段（/vol1）既不是当前位置，也不在允许清单里
    rootCrumb.textContent = '/' + parts.slice(0, startIdx).join('/');
    rootCrumb.addEventListener('click', () => loadDir('/' + parts.slice(0, startIdx).join('/')));
    host.appendChild(rootCrumb);

    parts.slice(startIdx).forEach((part, i) => {
      const sep = document.createElement('span');
      sep.className = 'crumb-sep';
      sep.textContent = '/';
      host.appendChild(sep);
      const isLast = i === parts.slice(startIdx).length - 1;
      const el = document.createElement(isLast ? 'span' : 'button');
      if (!isLast) el.type = 'button';
      el.className = 'crumb' + (isLast ? ' is-current' : ' crumb-btn');
      el.textContent = part;
      if (!isLast) {
        el.addEventListener('click', () => loadDir('/' + parts.slice(0, startIdx + i + 1).join('/')));
      }
      host.appendChild(el);
    });

    // 返回上一层的入口。
    //
    // 允许清单里有多个目录时，从其中一个"上不去"——它的父目录不在清单里，
    // 服务端也不会给。但用户总得能换到另一个指定目录，所以在根这一层把入口
    // 指向「可用目录」那一层（空路径），而不是干脆没有入口。
    const rootPaths = (roots || []).map((r) => (typeof r === 'string' ? r : (r && r.path) || ''));
    const atRoot = rootPaths.indexOf(path) >= 0;
    const upTarget = parent ? parent : (atRoot && rootPaths.length > 1 ? '' : null);
    if (upTarget !== null) {
      const up = document.createElement('button');
      up.type = 'button';
      up.className = 'crumb crumb-up';
      up.textContent = '↑ ' + t(parent ? 'up' : 'rootLabel');
      up.addEventListener('click', () => loadDir(upTarget));
      host.appendChild(up);
    }
  }

  // 根目录通常比共享目录浅一层，面包屑要按根的前缀切分
  function hostsPrefix(parts, roots) {
    if (!roots || !roots.length) return Math.min(1, parts.length);
    for (const root of roots) {
      const rootParts = String(root.path || '').split('/').filter(Boolean);
      if (rootParts.length && rootParts.every((p, i) => parts[i] === p)) return rootParts.length;
    }
    return Math.min(1, parts.length);
  }

  // 类型筛选只作用于文件：目录要一直可见，否则没法往下走。
  // 搜索框里的字是另一回事——它是个"名字包含"过滤，目录也要一起筛，
  // 否则输了一串字、列表里还杵着几十个目录，等于没筛。
  function visibleEntries() {
    let rows;
    if (state.searchResults !== null) {
      rows = state.searchResults;
    } else {
      rows = state.browserPath ? state.browserEntries : state.browserRoots;
      const tokens = state.searchQuery.trim().toLowerCase().split(/\s+/).filter(Boolean);
      if (tokens.length) {
        rows = rows.filter((e) => {
          const name = String(e.name).toLowerCase();
          return tokens.every((tok) => name.includes(tok));
        });
      }
    }
    if (state.typeFilter === 'all') return rows;
    return rows.filter((e) => e.is_dir || kindOf(e.name) === state.typeFilter);
  }

  // 列表为空时说什么：搜索没结果和空目录不是一回事
  function emptyText() {
    if (state.searchResults !== null || state.searchQuery.trim()) return t('searchEmpty');
    return state.typeFilter === 'all' ? t('emptyDir') : t('noMatch');
  }

  function renderBrowser() {
    if (state.source !== 'server') return;
    const list = $('browser-list');
    const gallery = $('browser-gallery');
    const grid = state.view === 'grid';
    list.hidden = grid;
    gallery.hidden = !grid;

    syncSearchChrome();
    const rows = visibleEntries();
    if (grid) renderGallery(gallery, rows);
    else renderList(list, rows);
  }

  function renderList(list, rows) {
    list.innerHTML = '';
    if (!rows.length) {
      list.innerHTML = `<li class="muted-row">${escapeHtml(emptyText())}</li>`;
      return;
    }

    rows.forEach((entry) => {
      const li = document.createElement('li');
      li.className = 'browser-row';

      const box = document.createElement('input');
      box.type = 'checkbox';
      box.className = 'fcheck';
      box.checked = state.picked.has(entry.path);
      box.addEventListener('change', () => togglePick(entry, box.checked));
      // 解压只接受一个压缩包，多选没有意义
      box.disabled = state.mode === 'extract' && entry.is_dir;
      li.appendChild(box);

      li.insertAdjacentHTML('beforeend', svgIcon(entry.is_dir));

      const name = document.createElement('span');
      name.className = 'fname' + (entry.is_dir ? ' is-dir' : '');
      name.textContent = entry.name;
      name.title = entry.path;
      name.addEventListener('click', () => {
        if (entry.is_dir) loadDir(entry.path);
        else { togglePick(entry, !state.picked.has(entry.path)); renderBrowser(); }
      });
      li.appendChild(name);

      // 搜索结果来自不同子目录，光一个文件名分不清是哪一份。这一列写清它在
      // 搜索根的哪一层，点一下就跳到它所在的目录。
      if (state.searchResults !== null && entry.rel) {
        const where = document.createElement('span');
        where.className = 'fwhere';
        where.textContent = relDirLabel(entry.rel);
        where.title = entry.dir || '';
        where.addEventListener('click', (ev) => { ev.stopPropagation(); gotoPath(entry.dir); });
        li.appendChild(where);
      }

      if (!entry.is_dir) {
        const size = document.createElement('span');
        size.className = 'fsize';
        size.textContent = humanBytes(entry.size);
        li.appendChild(size);
      }
      list.appendChild(li);
    });
  }

  // relDirLabel 把"相对搜索根的路径"缩成它所在的目录。
  // 顶层文件没有斜杠，显示 ./ 比显示空白更清楚。
  function relDirLabel(rel) {
    const s = String(rel || '');
    const i = s.lastIndexOf('/');
    if (i < 0) return t('searchAtRoot');
    const dir = s.slice(0, i);
    return dir ? dir + '/' : t('searchAtRoot');
  }

  // renderGallery 画网格。缩略图由服务端缩放；服务端解不了的格式（webp/avif 等）
  // 会自动重定向到原文件，交给浏览器自己解码。
  function renderGallery(host, rows) {
    host.innerHTML = '';
    if (!rows.length) {
      host.innerHTML = `<div class="muted-row">${escapeHtml(emptyText())}</div>`;
      return;
    }

    const io = videoObserver();
    rows.forEach((entry) => {
      const kind = entry.is_dir ? 'dir' : kindOf(entry.name);
      const tile = document.createElement('div');
      tile.className = 'tile' + (state.picked.has(entry.path) ? ' is-picked' : '');
      tile.dataset.path = entry.path;

      const thumb = document.createElement('div');
      thumb.className = 'tile-thumb';

      if (entry.is_dir) {
        thumb.insertAdjacentHTML('beforeend', svgIcon(true));
      } else if (kind === 'image') {
        const img = document.createElement('img');
        img.loading = 'lazy';
        img.decoding = 'async';
        img.alt = entry.name;
        img.src = thumbURL(entry.path, 320);
        // 服务端生成失败时退回图标，而不是留一个破图
        img.addEventListener('error', () => {
          img.remove();
          thumb.insertAdjacentHTML('beforeend', svgIcon(false));
        });
        thumb.appendChild(img);
      } else if (kind === 'video') {
        const ph = document.createElement('div');
        ph.className = 'tile-video-ph';
        ph.insertAdjacentHTML('beforeend', svgIcon(false));
        thumb.appendChild(ph);
        // <video> 只在滚到可见时才挂上去，避免一次列目录就发几十个请求
        io.observe(ph, entry.path);
      } else {
        thumb.insertAdjacentHTML('beforeend', svgIcon(false));
      }

      const badge = document.createElement('span');
      badge.className = 'tile-badge';
      badge.textContent = '✓';
      thumb.appendChild(badge);

      if (kind === 'image' || kind === 'video') {
        const zoom = document.createElement('button');
        zoom.type = 'button';
        zoom.className = 'tile-zoom';
        zoom.title = t('preview');
        zoom.setAttribute('aria-label', t('preview'));
        zoom.innerHTML = '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" '
          + 'stroke-width="1.9" stroke-linecap="round"><path d="M3 9V4h5M21 9V4h-5M3 15v5h5M21 15v5h-5"/></svg>';
        zoom.addEventListener('click', (e) => { e.stopPropagation(); openLightbox(entry); });
        thumb.appendChild(zoom);

        const tag = document.createElement('span');
        tag.className = 'tile-kind';
        tag.textContent = kindLabel(kind);
        thumb.appendChild(tag);
      }

      tile.appendChild(thumb);

      const meta = document.createElement('div');
      meta.className = 'tile-meta';
      const name = document.createElement('span');
      name.className = 'tile-name';
      name.textContent = entry.name;
      name.title = entry.path;
      meta.appendChild(name);
      if (state.searchResults !== null && entry.rel) {
        const where = document.createElement('span');
        where.className = 'tile-dir';
        where.textContent = relDirLabel(entry.rel);
        where.title = entry.dir || '';
        meta.appendChild(where);
      }
      if (!entry.is_dir) {
        const size = document.createElement('span');
        size.className = 'tile-size';
        size.textContent = humanBytes(entry.size);
        meta.appendChild(size);
      }
      tile.appendChild(meta);

      tile.addEventListener('click', () => {
        if (entry.is_dir) { loadDir(entry.path); return; }
        togglePick(entry, !state.picked.has(entry.path));
        renderBrowser();
      });
      // 双击直接看大图
      tile.addEventListener('dblclick', () => {
        if (!entry.is_dir && (kind === 'image' || kind === 'video')) openLightbox(entry);
      });

      host.appendChild(tile);
    });
  }

  // 视频首帧交给浏览器出（preload=metadata），用 IntersectionObserver
  // 控制加载时机，省得一次列目录就并发拉几十个视频头。
  let videoIO = null;
  function videoObserver() {
    if (videoIO) return videoIO;
    videoIO = {
      observer: new IntersectionObserver((items) => {
        items.forEach((item) => {
          if (!item.isIntersecting) return;
          const el = item.target;
          videoIO.observer.unobserve(el);
          const path = el.dataset.path;
          const v = document.createElement('video');
          v.src = rawURL(path);
          v.preload = 'metadata';
          v.muted = true;
          v.playsInline = true;
          v.addEventListener('loadeddata', () => { el.innerHTML = ''; el.appendChild(v); });
          v.addEventListener('error', () => { /* 保留图标 */ });
          // 先把元素塞进去触发元数据加载，拿到帧后再清掉占位图标
          el.appendChild(v);
        });
      }, { root: null, rootMargin: '200px' }),
      observe(el, path) {
        el.dataset.path = path;
        videoIO.observer.observe(el);
      },
    };
    return videoIO;
  }

  // 大图查看：看图/看视频时能确认内容，不用先选中再回头取消
  function openLightbox(entry) {
    const box = $('lightbox');
    const body = $('lightbox-body');
    const kind = kindOf(entry.name);
    body.innerHTML = '';

    if (kind === 'video') {
      const v = document.createElement('video');
      v.src = rawURL(entry.path);
      v.controls = true;
      v.autoplay = true;
      v.playsInline = true;
      body.appendChild(v);
    } else {
      const img = document.createElement('img');
      img.src = rawURL(entry.path);
      img.alt = entry.name;
      img.addEventListener('error', () => {
        body.innerHTML = `<div class="muted-row">${escapeHtml(t('failed'))}</div>`;
      });
      body.appendChild(img);
    }

    $('lightbox-name').textContent = entry.name;
    $('lightbox-foot').textContent = `${humanBytes(entry.size)} · ${entry.path}`;

    const pick = $('lightbox-pick');
    const sync = () => {
      const on = state.picked.has(entry.path);
      pick.textContent = on ? t('deselectThis') : t('selectThis');
      pick.classList.toggle('btn-primary', !on);
      pick.classList.toggle('btn-ghost', on);
    };
    pick.onclick = () => {
      togglePick(entry, !state.picked.has(entry.path));
      sync();
      renderBrowser();
    };
    sync();

    $('lightbox-open').onclick = () => window.open(rawURL(entry.path), '_blank', 'noopener');
    box.hidden = false;
  }

  function closeLightbox() {
    $('lightbox').hidden = true;
    $('lightbox-body').innerHTML = '';
  }

  function togglePick(entry, on) {
    if (state.mode === 'extract') {
      // 单选：换一个压缩包
      state.picked.clear();
      state.archive = null;
      state.archivePath = '';
      state.selectedMembers = null;
      $('archive-panel').hidden = true;
    }
    if (on) {
      if (state.mode === 'extract') state.picked.set(entry.path, entry);
      else state.picked.set(entry.path, entry);
    } else {
      state.picked.delete(entry.path);
    }
    renderPicked();
    renderRunButton();
    if (state.mode === 'extract' && state.picked.size === 1) inspectAndPreview();
    renderBrowser();
  }

  function renderPicked() {
    const items = state.source === 'server'
      ? Array.from(state.picked.values()).map((e) => ({ name: e.path, size: e.size, dir: e.is_dir }))
      : state.uploads.map((f) => ({ name: f.webkitRelativePath || f.relPath || f.name, size: f.size, dir: false }));

    const panel = $('source-panel');
    if (!items.length) {
      panel.hidden = true;
      $('source-count').textContent = '';
      return;
    }
    panel.hidden = false;
    const total = items.reduce((sum, it) => sum + (it.size || 0), 0);
    $('source-count').textContent = t('itemsCount', { n: items.length })
      + (total ? ' · ' + humanBytes(total) : '');

    const list = $('source-list');
    list.innerHTML = '';
    items.forEach((item, index) => {
      const li = document.createElement('li');
      li.innerHTML = svgIcon(item.dir) +
        `<span class="fname" title="${escapeHtml(item.name)}">${escapeHtml(item.name)}</span>` +
        (item.size ? `<span class="fsize">${humanBytes(item.size)}</span>` : '');
      const rm = document.createElement('button');
      rm.className = 'file-remove';
      rm.type = 'button';
      rm.textContent = '×';
      rm.title = t('clearAll');
      rm.addEventListener('click', (e) => {
        e.stopPropagation();
        if (state.source === 'server') {
          const key = Array.from(state.picked.keys())[index];
          state.picked.delete(key);
          if (state.mode === 'extract') {
            state.archive = null;
            $('archive-panel').hidden = true;
          }
          renderBrowser();
        } else {
          state.uploads.splice(index, 1);
        }
        renderPicked();
        renderRunButton();
      });
      li.appendChild(rm);
      list.appendChild(li);
    });
  }

  // ------------------------------------------------------- 解压包信息与预览

  async function inspectAndPreview() {
    const entry = Array.from(state.picked.values())[0];
    if (!entry) return;
    state.archivePath = entry.path;

    // 先取分卷/格式信息，这一步对分卷压缩包尤其重要
    try {
      const info = await api(`/api/inspect?path=${encodeURIComponent(entry.path)}`, {}, 20000);
      state.inspect = info;
      renderVolume();
      if (info.reason) toast(info.reason, 'warn');
    } catch (err) {
      toast(err.message, 'error');
    }

    await previewArchive(entry.path, true);
  }

  async function previewArchive(path, ensureJobFirst) {
    if (!state.online) return;
    setStatus(t('loading'));
    try {
      let jobId = state.jobId;
      if (!jobId || ensureJobFirst) {
        await resetJob();
        jobId = await ensureJob();
      }
      const info = await postJSON(`/api/jobs/${jobId}/preview`, {
        password: $('opt-password').value,
        path: path || '',
      }, 300000);

      state.archive = info;
      state.selectedMembers = new Set(
        (info.entries || []).filter((e) => !e.is_dir).map((e) => e.path)
      );
      renderArchive();
      renderVolume();
      setStatus(t('ready'));
    } catch (err) {
      if (err.data && err.data.password_required) {
        const wrong = $('opt-password').value !== '';
        state.archive = { needs_password: true, entries: [], header_encrypted: true };
        renderArchive();
        setStatus(t(wrong ? 'wrongPassword' : 'needsPassword'));
        toast(t(wrong ? 'wrongPassword' : 'needsPassword'), 'warn');
      } else {
        toast(err.message, 'error');
        setStatus(t('failed'));
      }
    }
  }

  function renderArchive() {
    const panel = $('archive-panel');
    const info = state.archive;
    if (!info || state.mode !== 'extract') { panel.hidden = true; return; }
    panel.hidden = false;

    if (info.needs_password && (!info.entries || !info.entries.length)) {
      $('archive-count').textContent = '';
      $('archive-meta').innerHTML = `<span>${escapeHtml(t('needsPassword'))}</span>`;
      $('archive-list').innerHTML = '';
      return;
    }

    const entries = info.entries || [];
    $('archive-count').textContent = t('itemsCount', { n: entries.length });
    const ratio = info.total_size > 0 && info.physical_size > 0
      ? Math.round((1 - info.physical_size / info.total_size) * 100) : null;
    $('archive-meta').innerHTML =
      `<span>${escapeHtml(t('totalSize'))} <b>${humanBytes(info.total_size)}</b></span>` +
      (info.physical_size ? `<span>${escapeHtml(t('packedSize'))} <b>${humanBytes(info.physical_size)}</b></span>` : '') +
      (ratio != null && ratio > 0 ? `<span>${escapeHtml(t('ratio'))} <b>${ratio}%</b></span>` : '') +
      `<span>${escapeHtml(String(info.format || '').toUpperCase())}</span>`;

    const list = $('archive-list');
    list.innerHTML = '';
    const limit = 500;
    const long = entries.length > limit;
    entries.slice(0, limit).forEach((entry) => {
      const li = document.createElement('li');
      const checked = !long && state.selectedMembers && state.selectedMembers.has(entry.path);
      li.innerHTML =
        `<input class="fcheck" type="checkbox" ${checked ? 'checked' : ''} ${entry.is_dir || long ? 'disabled' : ''}>` +
        svgIcon(entry.is_dir) +
        `<span class="fname" title="${escapeHtml(entry.path)}">${escapeHtml(entry.path)}</span>` +
        `<span class="fsize">${entry.is_dir ? '' : humanBytes(entry.size)}</span>`;
      const box = li.querySelector('input');
      box.addEventListener('change', () => {
        if (!state.selectedMembers) state.selectedMembers = new Set();
        if (box.checked) state.selectedMembers.add(entry.path);
        else state.selectedMembers.delete(entry.path);
      });
      list.appendChild(li);
    });
    if (long) {
      const li = document.createElement('li');
      li.className = 'muted-row';
      li.textContent = `… 仅显示前 ${limit} 项，其余将一并解压`;
      list.appendChild(li);
      state.selectedMembers = null;
    }
  }

  // ------------------------------------------------------------- 分卷信息

  function renderVolume() {
    const panel = $('volume-panel');
    const info = state.inspect;
    const set = info && info.volume;

    if (state.mode === 'compress') {
      // 压缩侧展示分卷设置的结果，让用户知道会生成什么
      const size = $('opt-volume').value;
      if (!size) { panel.hidden = true; return; }
      const f = currentFormat() || {};
      panel.hidden = false;
      const name = ($('opt-name').value.trim() || 'archive') + (f.extension || '.7z');
      $('volume-body').innerHTML =
        `<p class="volume-line">${escapeHtml(t('volumeSet'))}</p>` +
        `<p class="volume-example"><b>${escapeHtml(name)}.001</b> <b>${escapeHtml(name)}.002</b> …</p>` +
        `<p class="hint">${escapeHtml(t('volumeHint'))}</p>`;
      return;
    }

    if (!set || !set.is_set) { panel.hidden = true; return; }
    panel.hidden = false;
    let html = `<p class="volume-line">${escapeHtml(t('volumeSet'))} · `
      + `${escapeHtml(t('volumeCount', { n: set.count }))}</p>`
      + `<p class="volume-example"><b>${escapeHtml(set.label)}</b></p>`;
    if (set.missing && set.missing.length) {
      html += `<p class="volume-warn">${escapeHtml(t('volumeMissingList', { list: set.missing.join('、') }))}</p>`;
    } else {
      html += `<p class="hint">选中任意一卷即可，会自动收集其余分卷。</p>`;
    }
    $('volume-body').innerHTML = html;
  }

  // ------------------------------------------------------------- 格式 / 参数

  function renderFormats() {
    const host = $('formats');
    host.innerHTML = '';
    state.formats.forEach((f) => {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'fmt' + (f.id === state.format ? ' is-active' : '');
      btn.setAttribute('role', 'radio');
      btn.setAttribute('aria-checked', String(f.id === state.format));
      btn.disabled = f.capability !== CREATE_CAPABLE;
      btn.title = f.note || '';
      btn.innerHTML =
        `<span class="fmt-id">${escapeHtml(f.id.toUpperCase())}</span>` +
        `<span class="fmt-label">${escapeHtml(f.label)}</span>` +
        (f.capability !== CREATE_CAPABLE ? '<span class="fmt-tag">仅解压</span>' : '');
      btn.addEventListener('click', () => {
        state.format = f.id;
        renderFormats();
        syncOptions();
        renderVolume();
        renderRunButton();
      });
      host.appendChild(btn);
    });
    const f = currentFormat();
    $('format-hint').textContent = f ? (f.note || '') : '';
    $('name-ext').textContent = f ? f.extension : '';
  }

  function syncOptions() {
    const f = currentFormat() || {};
    const pw = $('opt-password');
    pw.disabled = !f.supports_password;
    if (!f.supports_password) pw.value = '';
    $('opt-encrypt-names').disabled = !f.supports_password || f.id !== '7z';
    if ($('opt-encrypt-names').disabled) $('opt-encrypt-names').checked = false;
    $('field-volume-size').hidden = state.mode !== 'compress' || !f.supports_volume;
    $('wrap-solid').hidden = f.id !== '7z';
    $('opt-volume').disabled = state.mode !== 'compress' || !f.supports_volume;
  }

  function setOutputMode(mode) {
    state.outputMode = mode;
    $('out-server').checked = mode === 'server';
    $('out-download').checked = mode === 'download';
    const serverMode = mode === 'server';
    $('output-dir-wrap').hidden = !serverMode;
    if (serverMode && !state.outputDir) state.outputDir = defaultOutputDir();
    $('opt-output-dir').value = state.outputDir;
    if (!serverMode) $('opt-output-dir').value = state.outputDir;
  }

  // 输出目录的默认值：压缩放在来源的父目录，解压放在压缩包所在目录，
  // 这样含单一顶层目录的压缩包不会解出 photos/photos 这种嵌套。
  function defaultOutputDir() {
    if (state.mode === 'extract' && state.inspect && state.inspect.suggested_output_dir) {
      return state.inspect.suggested_output_dir;
    }
    const first = Array.from(state.picked.values())[0];
    if (first && state.mode === 'compress') {
      const p = first.path;
      const idx = p.lastIndexOf('/');
      if (idx > 0) return p.slice(0, idx);
    }
    const dir = state.browserPath;
    if (dir) return dir;
    return state.browserRoots.length ? state.browserRoots[0].path : '';
  }

  // ------------------------------------------------------------- 目录选择器

  async function openPicker(current) {
    state.pickerFor = 'output';
    state.pickerSelected = current || '';
    $('server-modal').hidden = false;
    await pickerLoad(state.pickerSelected || defaultOutputDir());
  }

  async function pickerLoad(path) {
    try {
      const data = await api(`/api/browse?path=${encodeURIComponent(path || '')}`, {}, 20000);
      if (!data.enabled) { toast(t('noAllowRoots'), 'warn'); $('server-modal').hidden = true; return; }
      state.pickerPath = data.path || '';
      $('server-path').textContent = data.path || t('rootLabel');

      const list = $('server-list');
      list.innerHTML = '';

      if (data.parent || (data.path && data.roots)) {
        const up = document.createElement('li');
        up.className = 'browser-row';
        up.innerHTML = '<span class="fname">↑ ..</span>';
        up.style.cursor = 'pointer';
        up.addEventListener('click', () => pickerLoad(data.parent || ''));
        list.appendChild(up);
      }

      const rows = data.path ? (data.entries || []) : (data.roots || []);
      rows.filter((e) => e.is_dir || !data.path).forEach((entry) => {
        const li = document.createElement('li');
        li.className = 'browser-row';
        li.innerHTML = svgIcon(true) + `<span class="fname is-dir">${escapeHtml(entry.name)}</span>`;
        li.style.cursor = 'pointer';
        li.addEventListener('click', () => pickerLoad(entry.path));
        list.appendChild(li);
      });
      if (!rows.length) {
        list.innerHTML = `<li class="muted-row">${escapeHtml(t('emptyDir'))}</li>`;
      }
      $('server-selected').textContent = state.pickerPath || t('rootLabel');
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  // ------------------------------------------------------------- 任务流程

  async function ensureJob() {
    if (state.jobId) return state.jobId;
    const job = await api(`/api/jobs?kind=${state.mode}`, { method: 'POST' });
    state.job = job;
    state.jobId = job.id;
    return state.jobId;
  }

  async function resetJob() {
    stopPolling();
    if (state.uploadXHR) { try { state.uploadXHR.abort(); } catch { /* ignore */ } state.uploadXHR = null; }
    if (state.jobId && state.online) {
      try { await api(`/api/jobs/${state.jobId}`, { method: 'DELETE' }, 8000); } catch { /* 过期即可 */ }
    }
    state.job = null;
    state.jobId = null;
  }

  function uploadFiles(files) {
    return new Promise((resolve, reject) => {
      const form = new FormData();
      files.forEach((f) => {
        form.append('relpath', f.webkitRelativePath || f.relPath || f.name);
        form.append('files', f, f.name);
      });
      const xhr = new XMLHttpRequest();
      state.uploadXHR = xhr;
      xhr.open('POST', apiPath(`/api/jobs/${state.jobId}/upload`));
      xhr.upload.addEventListener('progress', (e) => {
        if (!e.lengthComputable) { setProgress(null, t('stageUpload')); return; }
        setProgress((e.loaded / e.total) * 100,
          `${t('stageUpload')} ${humanBytes(e.loaded)} / ${humanBytes(e.total)}`);
      });
      xhr.addEventListener('load', () => {
        state.uploadXHR = null;
        if (xhr.status >= 200 && xhr.status < 300) { resolve(); return; }
        let msg = `HTTP ${xhr.status}`;
        try { msg = JSON.parse(xhr.responseText).error || msg; } catch { /* 保留状态码 */ }
        reject(new Error(msg));
      });
      xhr.addEventListener('error', () => { state.uploadXHR = null; reject(new Error('上传失败')); });
      xhr.addEventListener('abort', () => { state.uploadXHR = null; reject(new Error(t('cancelled'))); });
      xhr.send(form);
    });
  }

  async function run() {
    if (!state.online) { toast(t('offlineAction'), 'warn'); return; }

    const useServer = state.source === 'server';
    if (useServer && state.picked.size === 0) {
      toast(state.mode === 'extract' ? t('noArchive') : t('noFiles'), 'warn');
      return;
    }
    if (!useServer && state.uploads.length === 0) {
      toast(state.mode === 'extract' ? t('noArchive') : t('noFiles'), 'warn');
      return;
    }

    setBusy(true);
    state.lastError = null;
    renderError(null);
    showProgress(t('working'), 0, '');
    try {
      // 已预览过的服务端压缩包复用同一个任务，避免重复读盘
      const reuseJob = useServer && state.mode === 'extract' && state.jobId && state.archive;
      if (!reuseJob) {
        await resetJob();
        await ensureJob();
        if (!useServer) {
          setProgress(0, t('stageUpload'));
          await uploadFiles(state.mode === 'extract' ? [state.uploads[0]] : state.uploads);
        }
      }

      const body = buildRunBody(useServer);
      const job = await postJSON(`/api/jobs/${state.jobId}/run`, body, 60000);
      state.job = job;
      startPolling();
    } catch (err) {
      hideProgress();
      setBusy(false);
      // 同步返回的错误（400 校验失败、密码不对）也要留在页面上
      state.lastError = { message: err.message, diagnostics: '' };
      renderError(state.lastError);
      if (err.data && err.data.password_required) {
        const wrong = $('opt-password').value !== '';
        toast(t(wrong ? 'wrongPassword' : 'needsPassword'), 'error');
        setStatus(t(wrong ? 'wrongPassword' : 'needsPassword'));
      } else {
        toast(err.message.split('\n')[0], 'error', 7000);
        setStatus(t('failed'));
      }
    }
  }

  function buildRunBody(useServer) {
    const password = $('opt-password').value;
    const serverOutput = state.outputMode === 'server' && useServer;
    const common = {
      password,
      output_mode: serverOutput ? 'server' : 'download',
      output_dir: serverOutput ? $('opt-output-dir').value.trim() : '',
    };

    if (state.mode === 'compress') {
      const body = {
        ...common,
        format: state.format,
        level: Number($('opt-level').value),
        encrypt_names: $('opt-encrypt-names').checked,
        volume_size: $('opt-volume').value,
        name: $('opt-name').value.trim(),
        threads: Number($('opt-threads').value) || 0,
        solid: $('opt-solid').checked,
        // 目标同名时 7-Zip 无法更新分卷归档，必须由这个开关决定是替换还是报错
        overwrite: $('opt-overwrite').checked,
      };
      if (useServer) body.source_paths = Array.from(state.picked.keys());
      return body;
    }

    const body = {
      ...common,
      preserve_paths: $('opt-preserve').checked,
      overwrite: $('opt-overwrite').checked,
    };
    if (useServer) {
      body.source_paths = [state.archivePath];
    }
    const entries = (state.archive && state.archive.entries) || [];
    if (state.selectedMembers && state.selectedMembers.size > 0 && entries.length <= 500) {
      body.selected = Array.from(state.selectedMembers);
    }
    return body;
  }

  function startPolling() {
    stopPolling();
    let misses = 0;
    const tick = async () => {
      try {
        const job = await api(`/api/jobs/${state.jobId}`, {}, 15000);
        misses = 0;
        state.job = job;
        if (job.status === 'running' || job.status === 'pending') {
          const stage = t('working');
          setProgress(job.progress, job.stage === 'compressing' ? t('stageCompressing') : stage);
          state.pollTimer = setTimeout(tick, 500);
        } else {
          stopPolling();
          finishJob(job);
        }
      } catch (err) {
        if (++misses >= 5) {
          stopPolling();
          hideProgress();
          setBusy(false);
          toast(err.message, 'error');
          setStatus(t('failed'));
          return;
        }
        state.pollTimer = setTimeout(tick, 1500);
      }
    };
    tick();
  }

  function stopPolling() {
    if (state.pollTimer) { clearTimeout(state.pollTimer); state.pollTimer = null; }
  }

  function finishJob(job) {
    hideProgress();
    setBusy(false);
    if (job.status === 'done') {
      state.result = job;
      state.lastError = null;
      renderError(null);
      renderResult();
      setStatus(t('done'));
      updateTaskPill('done', t('taskDoneHint'));
      toast(t('done'), 'success');
      return;
    }
    if (job.status === 'cancelled') {
      setStatus(t('cancelled'));
      toast(t('cancelled'), 'warn');
      return;
    }

    const msg = job.message || t('failed');
    const needsPassword = /密码|password/i.test(msg);
    state.lastError = {
      message: msg,
      diagnostics: job.diagnostics || '',
    };
    renderError(state.lastError);
    updateTaskPill(needsPassword ? 'warn' : 'error', t(needsPassword ? 'wrongPassword' : 'failed'));
    setStatus(needsPassword ? t('wrongPassword') : msg.split('\n')[0]);
    // 详细报错已经留在页面上，toast 只做一次轻提示
    toast(needsPassword ? t('wrongPassword') : t('failed'), 'error', 6000);
  }

  // 失败时把完整报错留在页面上：toast 会消失，也复制不了。
  function renderError(err) {
    const panel = $('error-panel');
    if (!err) {
      panel.hidden = true;
      $('error-body').textContent = '';
      $('error-meta').textContent = '';
      return;
    }
    panel.hidden = false;
    $('error-body').textContent = err.message;
    $('error-meta').textContent = err.diagnostics || '';
    $('btn-error-report').href = issueURL(err);
    panel.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  }

  // 一键把完整报错与运行环境预填进 GitHub Issue，省掉"再复述一遍"的来回。
  function issueURL(err) {
    const repo = trimSlash(state.repoUrl || 'https://github.com/YCyingchen/YC-7ZIP');
    const headline = String(err.message || '').split('\n')[0].slice(0, 90) || '处理失败';
    const environment = [
      err.diagnostics || '',
      `version: ${(state.health && state.health.version) || ''}`,
      `browser: ${navigator.userAgent}`,
      `page: ${location.href}`,
    ].join('\n');

    const body = [
      '### 我做了什么',
      '',
      '（补充：选了哪些文件、点了哪个按钮）',
      '',
      '### 期望结果',
      '',
      '（补充）',
      '',
      '### 实际报错',
      '',
      '```',
      String(err.message || '').slice(0, 4000),
      '```',
      '',
      '### 环境信息',
      '',
      '```',
      environment.slice(0, 3000),
      '```',
    ].join('\n');

    const params = new URLSearchParams({ title: `[Bug] ${headline}`, body, labels: 'bug' });
    return `${repo}/issues/new?${params.toString()}`;
  }

  function trimSlash(s) { return String(s).replace(/\/+$/, ''); }

  async function copyError() {
    if (!state.lastError) return;
    const parts = [state.lastError.message];
    if (state.lastError.diagnostics) parts.push('', '--- 环境信息 ---', state.lastError.diagnostics);
    const text = parts.join('\n');
    try {
      await navigator.clipboard.writeText(text);
      toast(t('errorCopied'), 'success');
    } catch {
      // 非安全上下文（http + 非 localhost）没有剪贴板权限，退回手选
      toast(t('errorCopyFailed'), 'warn');
    }
  }

  // ------------------------------------------------------------- 结果

  function renderResult() {
    const job = state.result;
    const panel = $('result-panel');
    if (!job) { panel.hidden = true; return; }
    panel.hidden = false;

    const download = job.output_mode !== 'server';
    const count = download ? (job.files || []).length : (job.server_paths || []).length;
    const ttlMin = state.health && state.health.limits
      ? Math.round(state.health.limits.job_ttl_seconds / 60) : null;

    let summary = `<span><b>${count}</b> ${state.lang === 'zh-CN' ? '项输出' : 'output(s)'}</span>`
      + `<span>${escapeHtml(t('totalSize'))} <b>${humanBytes(job.total_bytes)}</b></span>`;
    if (job.volume_label) summary += `<span>${escapeHtml(job.volume_label)}</span>`;
    if (download && ttlMin) summary += `<span>${escapeHtml(t('savedTo', { m: ttlMin }))}</span>`;
    $('result-summary').innerHTML = summary;

    const list = $('result-list');
    list.innerHTML = '';

    if (download) {
      (job.files || []).forEach((f) => {
        const li = document.createElement('li');
        const a = document.createElement('a');
        a.href = apiPath(`/api/jobs/${job.id}/download?file=${encodeURIComponent(f.name)}`);
        a.textContent = f.name;
        a.title = f.name;
        a.setAttribute('download', '');
        li.innerHTML = svgIcon(false) ;
        li.appendChild(a);
        const size = document.createElement('span');
        size.className = 'fsize';
        size.textContent = humanBytes(f.size);
        li.appendChild(size);
        list.appendChild(li);
      });
    } else {
      const where = document.createElement('li');
      where.className = 'muted-row';
      where.textContent = `${t('writtenTo', { n: count })} ${job.output_dir || ''}`;
      list.appendChild(where);
      (job.server_paths || []).forEach((p) => {
        const li = document.createElement('li');
        const dir = !/\.[0-9a-z]{1,4}$/i.test(p) && !/\.(7z|zip|rar|tar|gz|xz|bz2|zst)$/i.test(p);
        li.innerHTML = svgIcon(dir) +
          `<span class="fname" title="${escapeHtml(p)}">${escapeHtml(p)}</span>`;
        list.appendChild(li);
      });
    }

    const all = $('btn-download-all');
    all.hidden = !download;
    if (download) {
      all.href = apiPath(`/api/jobs/${job.id}/download-all`);
      all.textContent = count === 1 ? t('downloadResult') : t('downloadAll');
    }
    panel.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  }

  // ------------------------------------------------------------- UI 状态

  function setStatus(text) { $('status-text').textContent = text; }

  function setBusy(busy) {
    $('btn-run').disabled = busy || !canRun();
    $('btn-spinner').hidden = !busy;
  }

  function canRun() {
    if (!state.online) return false;
    if (state.source === 'server') return state.picked.size > 0;
    if (state.mode === 'extract') return state.uploads.length > 0;
    return state.uploads.length > 0;
  }

  function renderRunButton() {
    $('btn-run-label').textContent = t(state.mode === 'compress' ? 'startCompress' : 'startExtract');
    $('btn-run').disabled = !canRun();
    $('btn-reset').hidden = !state.jobId;
  }

  function showProgress(title, pct, stage) {
    $('progress-layer').hidden = false;
    $('progress-title').textContent = title;
    setProgress(pct, stage);
  }

  function hideProgress() {
    $('progress-layer').hidden = true;
    // 只有既没有结果也没有错误时才复位（例如取消）
    if (!state.result && !state.lastError) updateTaskPill(null);
  }

  function setProgress(pct, stage) {
    const fill = $('progress-fill');
    if (pct == null) {
      fill.style.width = '35%';
      fill.style.opacity = '.5';
      $('progress-percent').textContent = '…';
    } else {
      fill.style.opacity = '1';
      fill.style.width = `${Math.max(0, Math.min(100, pct))}%`;
      $('progress-percent').textContent = `${Math.round(pct)}%`;
    }
    if (stage != null) $('progress-stage').textContent = stage;
    syncTaskPillFromLayer();
  }

  // 顶栏的常驻任务指示。进度弹层会盖住内容，关掉之后就没有别处能看进度了，
  // 所以状态要有一个不挡路的落点，并且能一键回到弹层。
  const TASK_GLYPHS = {
    running: '<span class="spin"></span>',
    // 空闲：虚线圆圈。任务指示是常驻的，没有任务时也得有个"这里能看任务"的样子
    idle: '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" '
      + 'stroke-width="2"><circle cx="12" cy="12" r="8" stroke-dasharray="3 3"/></svg>',
    done: '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" '
      + 'stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="m4 12 5 5L20 6"/></svg>',
    error: '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" '
      + 'stroke-width="2.2" stroke-linecap="round"><path d="M12 7v7M12 17.5h.01"/></svg>',
    warn: '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" '
      + 'stroke-width="2" stroke-linecap="round" stroke-linejoin="round">'
      + '<path d="M12 3 2 20h20L12 3Z"/><path d="M12 9v5M12 17.5h.01"/></svg>',
  };

  function updateTaskPill(state, text) {
    const pill = $('task-pill');
    // 常驻：没有任务时也占着这一格，显示「无任务」。
    // 之前这里直接 hidden，于是"现在到底有没有在跑"只有正在跑时才看得见——
    // 而"随时能瞄一眼"恰恰是当初要这个指示器的理由。
    if (!state) {
      pill.hidden = false;
      pill.dataset.state = 'idle';
      $('task-glyph').innerHTML = TASK_GLYPHS.idle;
      $('task-text').textContent = t('taskIdle');
      return;
    }
    pill.hidden = false;
    pill.dataset.state = state;
    $('task-glyph').innerHTML = TASK_GLYPHS[state] || TASK_GLYPHS.running;
    $('task-text').textContent = text || '';
  }

  // 从弹层当前内容反推顶栏指示
  function syncTaskPillFromLayer() {
    if ($('progress-layer').hidden) return;
    const pct = $('progress-percent').textContent;
    const stage = $('progress-stage').textContent;
    updateTaskPill('running', pct && pct !== '…' ? (pct + ' ' + stage).trim() : stage);
  }

  function onTaskPillClick() {
    const state = $('task-pill').dataset.state;
    // 空闲态什么都不做：它不是按钮，只是那一格的位置说明
    if (!state || state === 'idle') return;
    if (state === 'running') {
      // 回到进度弹层，随时能看当前阶段
      $('progress-layer').hidden = false;
      return;
    }
    if (state === 'error') {
      $('error-panel').scrollIntoView({ behavior: 'smooth', block: 'center' });
      return;
    }
    if (state === 'done' || state === 'warn') {
      $('result-panel').scrollIntoView({ behavior: 'smooth', block: 'center' });
    }
  }


  // ------------------------------------------------------------- 事件绑定

  function bindEvents() {
    $('mode-compress').addEventListener('click', () => setMode('compress'));
    $('mode-extract').addEventListener('click', () => setMode('extract'));
    $('tab-server').addEventListener('click', () => { if (state.browserRoots.length) setSource('server'); else toast(t('noAllowRoots'), 'warn'); });
    $('tab-upload').addEventListener('click', () => setSource('upload'));
    $('browser-refresh').addEventListener('click', () => loadDir(state.browserPath));

    // 地址栏：粘一个路径进去就能跳，不用一层层点
    $('path-go').addEventListener('click', () => gotoPath($('path-input').value));
    $('path-input').addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); gotoPath($('path-input').value); }
      // Esc 放弃这次输入，恢复成当前真实路径
      if (e.key === 'Escape') { e.preventDefault(); syncPathInput(); $('path-input').blur(); }
    });

    // 搜索：输入时先在当前目录里筛（不发请求，敲一下就有反应），
    // 回车才去子目录里翻（那才是要花时间的操作）。
    const searchInput = $('search-input');
    searchInput.addEventListener('input', () => {
      state.searchQuery = searchInput.value;
      state.searchResults = null;
      state.searchMeta = null;
      renderBrowser();
    });
    searchInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); runSearch(); }
      if (e.key === 'Escape') { e.preventDefault(); clearSearch(); renderBrowser(); }
    });
    $('search-go').addEventListener('click', () => runSearch());
    $('search-clear').addEventListener('click', () => {
      clearSearch();
      renderBrowser();
      $('search-input').focus();
    });

    $('view-list').addEventListener('click', () => setView('list'));
    $('view-grid').addEventListener('click', () => setView('grid'));
    $('type-filter').addEventListener('change', (e) => {
      state.typeFilter = e.target.value;
      renderBrowser();
    });
    $('lightbox-close').addEventListener('click', closeLightbox);
    $('lightbox').addEventListener('click', (e) => {
      if (e.target === $('lightbox')) closeLightbox();
    });

    const dropzone = $('dropzone');
    dropzone.addEventListener('click', () => $('file-input').click());
    dropzone.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); $('file-input').click(); }
    });
    $('pick-files').addEventListener('click', (e) => { e.stopPropagation(); $('file-input').click(); });
    $('pick-dir').addEventListener('click', (e) => { e.stopPropagation(); $('dir-input').click(); });
    $('file-input').addEventListener('change', (e) => { addUploads(e.target.files); e.target.value = ''; });
    $('dir-input').addEventListener('change', (e) => { addUploads(e.target.files); e.target.value = ''; });

    ['dragenter', 'dragover'].forEach((ev) => dropzone.addEventListener(ev, (e) => {
      e.preventDefault();
      dropzone.classList.add('is-over');
    }));
    ['dragleave', 'dragend'].forEach((ev) => dropzone.addEventListener(ev, () => dropzone.classList.remove('is-over')));
    dropzone.addEventListener('drop', async (e) => {
      e.preventDefault();
      dropzone.classList.remove('is-over');
      addUploads(await collectDroppedFiles(e.dataTransfer));
    });
    window.addEventListener('dragover', (e) => e.preventDefault());
    window.addEventListener('drop', (e) => e.preventDefault());

    $('clear-source').addEventListener('click', () => {
      state.picked.clear();
      state.uploads = [];
      state.archive = null;
      state.archivePath = '';
      $('archive-panel').hidden = true;
      $('volume-panel').hidden = true;
      updateTaskPill(null);
      renderBrowser();
      renderPicked();
      renderRunButton();
    });

    $('select-all').addEventListener('click', () => {
      if (!state.archive || !state.archive.entries) return;
      state.selectedMembers = new Set(state.archive.entries.filter((x) => !x.is_dir).map((x) => x.path));
      renderArchive();
    });
    $('select-none').addEventListener('click', () => {
      state.selectedMembers = new Set();
      renderArchive();
    });

    $('opt-level').addEventListener('input', (e) => { $('level-out').textContent = e.target.value; });
    $('toggle-password').addEventListener('click', () => {
      const input = $('opt-password');
      const showing = input.type === 'text';
      input.type = showing ? 'password' : 'text';
      $('toggle-password').textContent = t(showing ? 'show' : 'hide');
    });
    $('opt-volume').addEventListener('change', renderVolume);
    $('opt-name').addEventListener('input', renderVolume);
    $('out-server').addEventListener('change', () => setOutputMode('server'));
    $('out-download').addEventListener('change', () => setOutputMode('download'));
    $('output-pick').addEventListener('click', () => openPicker($('opt-output-dir').value));
    $('opt-output-dir').addEventListener('input', (e) => { state.outputDir = e.target.value; });

    $('btn-run').addEventListener('click', run);
    $('btn-error-copy').addEventListener('click', copyError);
    $('btn-error-copy2').addEventListener('click', copyError);
    $('task-pill').addEventListener('click', onTaskPillClick);
    $('btn-cancel').addEventListener('click', async () => {
      if (state.jobId) {
        try { await api(`/api/jobs/${state.jobId}/cancel`, { method: 'POST' }, 10000); } catch { /* 已结束 */ }
      }
      stopPolling();
      hideProgress();
      setBusy(false);
      setStatus(t('cancelled'));
    });
    $('btn-reset').addEventListener('click', async () => {
      await resetJob();
      $('result-panel').hidden = true;
      state.result = null;
      renderRunButton();
      setStatus(t('ready'));
    });
    $('btn-new').addEventListener('click', async () => {
      await resetJob();
      state.picked.clear();
      state.uploads = [];
      state.archive = null;
      state.archivePath = '';
      state.inspect = null;
      state.result = null;
      state.lastError = null;
      $('result-panel').hidden = true;
      $('error-panel').hidden = true;
      $('archive-panel').hidden = true;
      $('volume-panel').hidden = true;
      renderBrowser();
      renderPicked();
      renderRunButton();
      setStatus(t('ready'));
      window.scrollTo({ top: 0, behavior: 'smooth' });
    });

    $('server-close').addEventListener('click', () => { $('server-modal').hidden = true; });
    $('server-confirm').addEventListener('click', () => {
      const dir = state.pickerPath || state.pickerSelected;
      if (dir) {
        state.outputDir = dir;
        $('opt-output-dir').value = dir;
      }
      $('server-modal').hidden = true;
    });
    $('server-modal').addEventListener('click', (e) => {
      if (e.target === $('server-modal')) $('server-modal').hidden = true;
    });

    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        if (!$('lightbox').hidden) { closeLightbox(); return; }
        $('server-modal').hidden = true;
        if (!$('progress-layer').hidden) $('btn-cancel').click();
      }
    });
  }

  function addUploads(fileList) {
    const incoming = Array.from(fileList || []);
    if (!incoming.length) return;
    if (state.mode === 'extract') state.uploads = incoming.slice(0, 1);
    else state.uploads = state.uploads.concat(incoming);
    renderPicked();
    renderRunButton();
  }

  async function collectDroppedFiles(dataTransfer) {
    if (!dataTransfer) return [];
    const items = dataTransfer.items;
    if (!items || !items.length || typeof items[0].webkitGetAsEntry !== 'function') {
      return Array.from(dataTransfer.files || []);
    }
    const entries = [];
    for (const item of items) {
      const entry = item.webkitGetAsEntry();
      if (entry) entries.push(entry);
    }
    const out = [];
    async function walk(entry, prefix) {
      if (entry.isFile) {
        const file = await new Promise((resolve, reject) => entry.file(resolve, reject));
        file.relPath = prefix + entry.name;
        out.push(file);
        return;
      }
      if (entry.isDirectory) {
        const reader = entry.createReader();
        let batch;
        do {
          batch = await new Promise((resolve, reject) => reader.readEntries(resolve, reject));
          for (const child of batch) await walk(child, `${prefix}${entry.name}/`);
        } while (batch.length > 0);
      }
    }
    for (const entry of entries) await walk(entry, '');
    return out;
  }

  // ---------------------------------------------- 被 fnOS 用 ?path 唤起

  // 文件管理器右键 →「打开方式」时，fnOS 会追加 ?path=<绝对路径>。
  // 根据扩展名和实际类型决定进哪个模式，并预选该文件。
  async function openFromQuery() {
    const params = new URLSearchParams(location.search);
    const path = params.get('path');
    if (!path) return;
    const action = params.get('action');

    setSource('server');

    // 先问服务端这是什么：目录就直接进目录，文件才预选并决定模式。
    //
    // 问不到就到此为止，不要再按扩展名去猜。问不到只有两种可能：路径不在允许
    // 范围内（服务端现在不明说），或者它根本不存在——这两种情况都不该把它摆到
    // 界面上：让"已选择"里出现一个你不该看见的路径，比给一句报错更糟。
    let info = null;
    try {
      info = await api(`/api/inspect?path=${encodeURIComponent(path)}`, {}, 15000);
    } catch {
      await loadDir('');
      renderBrowser();
      return;
    }

    if (info && info.is_dir) {
      setMode(action === 'extract' ? 'extract' : 'compress');
      await loadDir(path);
      renderBrowser();
      return;
    }

    if (action === 'extract' || action === 'compress') setMode(action);
    else if (isArchiveName(path)) setMode('extract');
    else setMode('compress');

    // 定位到该文件所在目录，方便用户看清上下文
    const dir = path.slice(0, path.lastIndexOf('/')) || '/';
    await loadDir(dir);

    let entry = state.browserEntries.find((e) => e.path === path);
    if (!entry) {
      entry = { path, name: path.slice(path.lastIndexOf('/') + 1), is_dir: false, size: 0 };
    }
    togglePick(entry, true);
    renderBrowser();

    // 让入口一眼可见：闪一下开始按钮
    $('btn-run').classList.add('pulse');
    setTimeout(() => $('btn-run').classList.remove('pulse'), 2400);
  }

  // ---------------------------------------------------------------- 启动

  function init() {
    initTheme();
    initLang();
    bindEvents();
    $('level-out').textContent = $('opt-level').value;
    setMode('compress');
    setSource('server');
    setOutputMode('server');
    renderFormats();
    syncOptions();
    renderPicked();
    renderRunButton();
    // 记住上次用的视图：看图的人不希望每次进来都切一次
    try {
      const savedView = localStorage.getItem('yc7zip-view');
      if (savedView === 'grid' || savedView === 'list') state.view = savedView;
    } catch { /* 隐私模式 */ }
    $('view-list').classList.toggle('is-active', state.view === 'list');
    $('view-grid').classList.toggle('is-active', state.view === 'grid');
    bindSettingsEvents();
    loadSettings();
    checkHealth().then(openFromQuery);
    setInterval(checkHealth, 60000);
  }

  // ==================================================== 设置：壁纸与更新

  const settingsState = { data: null, update: null };

  // 壁纸铺在最底层的固定层，面板照旧是实色——文字始终落在实色上，
  // 不会出现"浅色照片上白字看不清"。暗化 scrim 按当前主题算。
  function applyWallpaper(settings) {
    const root = document.documentElement;
    const body = document.body;
    settingsState.data = settings;

    // 面板透明化与有没有壁纸无关，所以放在下面那个 early return 之前：
    // 不然"先调透明、再去掉壁纸"会把面板留成半透明。
    // 只写一个 CSS 变量：三个面板底色都由它派生，组件不用知道这事。
    const alpha = Math.max(0, Math.min(90, settings && settings.panel_alpha ? settings.panel_alpha : 0));
    root.style.setProperty('--panel-alpha', String(alpha / 100));

    const on = settings && settings.wallpaper && settings.has_wallpaper;
    body.classList.toggle('has-wallpaper', !!on);
    if (!on) return;

    const light = root.dataset.theme === 'light';
    const rgb = light ? '255,255,255' : '11,15,20';
    const dim = Math.max(0, Math.min(90, settings.dim == null ? 45 : settings.dim)) / 100;
    root.style.setProperty('--wp-scrim', 'rgba(' + rgb + ',' + dim + ')');
    root.style.setProperty('--wp-blur', (settings.blur || 0) + 'px');

    const fit = settings.fit || 'cover';
    root.style.setProperty('--wp-size', fit === 'tile' ? 'auto' : fit);
    root.style.setProperty('--wp-repeat', fit === 'tile' ? 'repeat' : 'no-repeat');

    const v = encodeURIComponent(settings.updated_at || '0');
    root.style.setProperty('--wp-image', 'url("' + apiPath('/api/wallpaper') + '?v=' + v + '")');
  }

  async function loadSettings() {
    try {
      const settings = await api('/api/ui-settings', {}, 10000);
      applyWallpaper(settings);
      syncSettingsForm(settings);
    } catch (e) { /* 服务端没起来时按没有设置处理 */ }
  }

  function syncSettingsForm(settings) {
    if (!settings) return;
    const dim = settings.dim == null ? 45 : settings.dim;
    const blur = settings.blur == null ? 0 : settings.blur;
    $('wp-dim').value = dim;
    $('wp-dim-out').textContent = dim + '%';
    $('wp-blur').value = blur;
    $('wp-blur-out').textContent = blur + ' px';
    const panel = settings.panel_alpha == null ? 0 : settings.panel_alpha;
    $('wp-panel').value = panel;
    $('wp-panel-out').textContent = panel + '%';
    $('wp-fit').value = settings.fit || 'cover';
    $('wp-enabled').checked = !!settings.wallpaper;

    const preview = $('wallpaper-preview');
    const empty = $('wallpaper-empty');
    if (settings.has_wallpaper) {
      empty.hidden = true;
      preview.style.backgroundImage =
        'url("' + apiPath('/api/wallpaper') + '?v=' + encodeURIComponent(settings.updated_at || '0') + '")';
    } else {
      empty.hidden = false;
      preview.style.backgroundImage = '';
    }
  }

  async function saveSettings(patch) {
    const cur = settingsState.data || {};
    const body = {
      wallpaper: patch.wallpaper == null ? (cur.wallpaper || false) : patch.wallpaper,
      dim: patch.dim == null ? (cur.dim == null ? 45 : cur.dim) : patch.dim,
      blur: patch.blur == null ? (cur.blur || 0) : patch.blur,
      panel_alpha: patch.panel_alpha == null ? (cur.panel_alpha || 0) : patch.panel_alpha,
      fit: patch.fit || cur.fit || 'cover',
    };
    const saved = await api('/api/ui-settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }, 15000);
    applyWallpaper(saved);
    syncSettingsForm(saved);
    return saved;
  }

  async function uploadWallpaper(file) {
    const form = new FormData();
    form.append('image', file, file.name);
    const res = await fetch(apiPath('/api/wallpaper'), { method: 'POST', body: form });
    const data = await res.json().catch(function () { return {}; });
    if (!res.ok) throw new Error(data.error || ('HTTP ' + res.status));
    applyWallpaper(data);
    syncSettingsForm(data);
    toast(t('wallpaperSaved'), 'success');
  }

  async function clearWallpaper() {
    const data = await api('/api/wallpaper', { method: 'DELETE' }, 15000);
    applyWallpaper(data);
    syncSettingsForm(data);
    toast(t('wallpaperCleared'), 'success');
  }

  // 渠道名按语言渲染：服务端只给 kind 与地址，文案在前端才有 zh/en 两套。
  function sourceName(kind, url) {
    if (kind === 'github') return t('sourceGitHub');
    if (kind === 'self') return t('sourceSelf') + (url ? ' · ' + url.replace(/^https?:\/\//, '') : '');
    return kind || '';
  }

  // 逐条渠道的结果。配了多条渠道就必须能分辨"是谁答的、谁没通"，
  // 只说一句"检查失败"会让人以为所有渠道都断了。
  function sourceLines(status) {
    if (!status.sources || !status.sources.length) return '';
    return '\n\n' + t('sourceResult') + '\n' + status.sources.map(function (s) {
      const detail = s.ok ? (s.version || '') : (s.error || t('sourceFailed'));
      return '· ' + sourceName(s.kind, s.url) + ' — ' + detail;
    }).join('\n');
  }

  // 渠道选择器只在渠道清单或语言变化时重建，并保住用户当前的选择，
  // 否则每次检查之后都会跳回「自动」。
  function renderSourcePicker(options) {
    const select = $('update-source');
    if (!select || !options) return;
    const wanted = select.value || 'auto';
    const signature = state.lang + '#' + options.map(function (o) {
      return o.kind + '|' + (o.url || '');
    }).join(',');
    if (signature === select.dataset.signature) return;
    select.dataset.signature = signature;
    select.innerHTML = '<option value="auto">' + escapeHtml(t('sourceAuto')) + '</option>' +
      options.map(function (o) {
        return '<option value="' + escapeHtml(o.kind) + '">' + escapeHtml(sourceName(o.kind, o.url)) + '</option>';
      }).join('');
    select.value = options.some(function (o) { return o.kind === wanted; }) ? wanted : 'auto';
  }

  function renderUpdateStatus(status) {
    const methods = { binary: t('methodBinary'), container: t('methodContainer'), package: t('methodPackage') };
    let head =
      '<span>' + escapeHtml(t('currentVersion')) + ' <b>' + escapeHtml(status.current || '') + '</b></span>' +
      '<span>' + escapeHtml(t('channelLabel')) + ' <b>' + escapeHtml(status.channel || '') + '</b></span>' +
      '<span>' + escapeHtml(t('deployMethod')) + ' <b>' + escapeHtml(methods[status.method] || status.method || '') + '</b></span>';
    if (status.source) {
      head += '<span>' + escapeHtml(t('updateSourceLabel')) + ' <b>' +
        escapeHtml(sourceName(status.source, status.source_url)) + '</b></span>';
    }
    $('update-current').innerHTML = head;
    renderSourcePicker(status.source_options);

    const result = $('update-result');
    const hint = $('update-hint');
    const apply = $('btn-update-apply');
    const lines = sourceLines(status);

    if (status.error) {
      result.hidden = false;
      result.dataset.kind = 'err';
      result.textContent = status.error + lines;
    } else if (status.has_update) {
      result.hidden = false;
      result.dataset.kind = 'ok';
      result.textContent = t('newVersionFound') + ' ' + status.latest + '\n' +
        (status.published_at ? status.published_at + '\n' : '') +
        (status.notes ? '\n' + status.notes.slice(0, 1200) : '') + lines;
    } else if (status.latest) {
      result.hidden = false;
      result.dataset.kind = '';
      result.textContent = t('alreadyLatest', { v: status.current }) + lines;
    } else {
      result.hidden = true;
    }

    apply.hidden = !(status.has_update && status.can_self_update);
    hint.textContent = status.can_self_update ? t('selfUpdateOk') : (status.reason || '');
  }

  async function checkUpdate() {
    const btn = $('btn-update-check');
    const label = btn.textContent;
    btn.disabled = true;
    btn.textContent = t('checking');
    try {
      const picker = $('update-source');
      const source = picker ? picker.value : 'auto';
      // 选了具体渠道就只问那一条，省得"我想验证自建源"却还是被 GitHub 的
      // 慢响应拖着等
      const query = source && source !== 'auto' ? '?source=' + encodeURIComponent(source) : '';
      const status = await api('/api/update/check' + query, { method: 'POST' }, 40000);
      settingsState.update = status;
      renderUpdateStatus(status);
    } catch (err) {
      $('update-result').hidden = false;
      $('update-result').dataset.kind = 'err';
      $('update-result').textContent = err.message;
    } finally {
      btn.disabled = false;
      btn.textContent = label;
    }
  }

  async function applyUpdate() {
    if (!window.confirm(t('confirmUpdate'))) return;
    const btn = $('btn-update-apply');
    btn.disabled = true;
    btn.textContent = t('updating');
    try {
      const result = await api('/api/update/apply', { method: 'POST' }, 900000);
      $('update-result').hidden = false;
      $('update-result').dataset.kind = 'ok';
      $('update-result').textContent = t('updateInstalled') + ' ' + result.installed + '\n' + t('restarting');
      toast(t('updateInstalled'), 'success');
    } catch (err) {
      $('update-result').hidden = false;
      $('update-result').dataset.kind = 'err';
      $('update-result').textContent = err.message;
      btn.disabled = false;
      btn.textContent = t('applyUpdate');
    }
  }

  async function uploadUpdate(file) {
    const form = new FormData();
    form.append('package', file, file.name);
    const res = await fetch(apiPath('/api/update/upload'), { method: 'POST', body: form });
    const data = await res.json().catch(function () { return {}; });
    if (!res.ok) throw new Error(data.error || ('HTTP ' + res.status));
    $('update-result').hidden = false;
    $('update-result').dataset.kind = 'ok';
    $('update-result').textContent = t('updateInstalled') + ' ' + data.installed + '\n' + t('restarting');
    toast(t('updateInstalled'), 'success');
  }

  // 只认 CHANGELOG 里实际用到的那点 markdown：版本标题、小节、列表、加粗、代码。
  // 为了这个引一个渲染器不划算，还要自己盯转义。
  function renderChangelog(markdown) {
    function esc(s) {
      return String(s).replace(/[&<>]/g, function (c) {
        return { '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c];
      });
    }
    function inline(s) {
      return esc(s)
        .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
        .replace(/`(.+?)`/g, '<code>$1</code>');
    }

    const html = [];
    let open = false;
    let inList = false;
    let shown = 0;

    function closeList() { if (inList) { html.push('</ul>'); inList = false; } }
    // 每个版本是一个 <details>：折叠起来只占一行，查哪版看哪版
    function closeRelease() { closeList(); if (open) { html.push('</div></details>'); open = false; } }

    for (const raw of String(markdown).split('\n')) {
      const line = raw.replace(/\s+$/, '');
      const ver = line.match(/^##\s+(zip\d{4}\.\d{3})\s*[·・]\s*(.+?)\s*$/);
      if (ver) {
        if (shown >= 3) break;
        shown++;
        closeRelease();
        // 最新的那个默认展开：它正是最常看的一条
        html.push('<details class="release"' + (shown === 1 ? ' open' : '') + '>' +
          '<summary class="release-head">' +
          '<span class="release-ver">' + esc(ver[1]) + '</span>' +
          '<span class="release-date">' + esc(ver[2]) + '</span>' +
          '<span class="release-toggle"></span></summary>' +
          '<div class="release-body">');
        open = true;
        continue;
      }
      if (!open) continue;

      const head = line.match(/^###\s+(.+?)\s*$/);
      if (head) {
        closeList();
        html.push('<h4 class="release-section">' + inline(head[1]) + '</h4>');
        continue;
      }
      if (line.startsWith('- ')) {
        if (!inList) { html.push('<ul class="release-items">'); inList = true; }
        html.push('<li>' + inline(line.slice(2).trim()) + '</li>');
        continue;
      }
      // 折行续接：中文之间不补空格
      if (line.startsWith('  ') && inList && html.length) {
        const i = html.length - 1;
        if (html[i].endsWith('</li>')) {
          html[i] = html[i].replace(/<\/li>$/, ' ') + inline(line.trim()) + '</li>';
        }
      }
    }
    closeRelease();
    return html.join('');
  }

  async function loadChangelog() {
    const host = $('changelog-inline');
    try {
      const data = await api('/api/changelog', {}, 10000);
      const md = (data && data.markdown) || '';
      host.innerHTML = md
        ? renderChangelog(md)
        : '<p class="release-more">' + escapeHtml(t('changelogUnavailable')) + '</p>';
    } catch (e) {
      host.innerHTML = '<p class="release-more">' + escapeHtml(t('changelogUnavailable')) + '</p>';
    }
  }

  function openSettings() {
    $('settings-modal').hidden = false;
    api('/api/update', {}, 10000).then(renderUpdateStatus).catch(function () {});
    loadSettings();
    loadChangelog();
  }

  function bindSettingsEvents() {
    $('settings-toggle').addEventListener('click', openSettings);
    $('settings-close').addEventListener('click', function () { $('settings-modal').hidden = true; });
    $('settings-modal').addEventListener('click', function (e) {
      if (e.target === $('settings-modal')) $('settings-modal').hidden = true;
    });

    $('btn-update-check').addEventListener('click', checkUpdate);
    $('btn-update-apply').addEventListener('click', applyUpdate);
    $('btn-update-local').addEventListener('click', function () { $('update-file').click(); });
    $('update-file').addEventListener('change', async function (e) {
      const file = e.target.files && e.target.files[0];
      e.target.value = '';
      if (!file) return;
      try {
        await uploadUpdate(file);
      } catch (err) {
        $('update-result').hidden = false;
        $('update-result').dataset.kind = 'err';
        $('update-result').textContent = err.message;
      }
    });

    $('btn-wallpaper-upload').addEventListener('click', function () { $('wallpaper-file').click(); });
    $('wallpaper-file').addEventListener('change', async function (e) {
      const file = e.target.files && e.target.files[0];
      e.target.value = '';
      if (!file) return;
      try { await uploadWallpaper(file); } catch (err) { toast(err.message, 'error'); }
    });
    $('btn-wallpaper-clear').addEventListener('click', async function () {
      try { await clearWallpaper(); } catch (err) { toast(err.message, 'error'); }
    });

    $('wp-dim').addEventListener('input', function (e) {
      $('wp-dim-out').textContent = e.target.value + '%';
      const s = Object.assign({}, settingsState.data || {}, { wallpaper: true, has_wallpaper: true, dim: Number(e.target.value) });
      applyWallpaper(s);
    });
    $('wp-dim').addEventListener('change', function () {
      saveSettings({ dim: Number($('wp-dim').value) }).catch(function () {});
    });

    $('wp-blur').addEventListener('input', function (e) {
      $('wp-blur-out').textContent = e.target.value + ' px';
      const s = Object.assign({}, settingsState.data || {}, { wallpaper: true, has_wallpaper: true, blur: Number(e.target.value) });
      applyWallpaper(s);
    });
    $('wp-blur').addEventListener('change', function () {
      saveSettings({ blur: Number($('wp-blur').value) }).catch(function () {});
    });

    $('wp-fit').addEventListener('change', function () { saveSettings({ fit: $('wp-fit').value }).catch(function () {}); });

    $('wp-panel').addEventListener('input', function (e) {
      $('wp-panel-out').textContent = e.target.value + '%';
      const s = Object.assign({}, settingsState.data || {}, { panel_alpha: Number(e.target.value) });
      applyWallpaper(s);
    });
    $('wp-panel').addEventListener('change', function () {
      saveSettings({ panel_alpha: Number($('wp-panel').value) }).catch(function () {});
    });

    $('wp-enabled').addEventListener('change', function () { saveSettings({ wallpaper: $('wp-enabled').checked }).catch(function () {}); });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
