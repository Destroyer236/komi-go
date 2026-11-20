'use strict';

/* Komi Go frontend — state-driven, no framework. All values coming from the
   backend are treated as untrusted and escaped before they touch the DOM. */

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
const esc = (v) => String(v == null ? '' : v).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const clamp = (n, a, b) => Math.min(b, Math.max(a, n));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const S = {
  auth: {},
  profile: {},
  profileRepos: [],
  profileReposLoaded: false,
  profileReposLoading: false,
  profileError: '',
  profileFilter: 'released',
  profileQuery: '',
  contributions: null,
  contributionsLoaded: false,
  contributionsLoading: false,
  contributionsError: '',
  settings: {},
  settingsScreen: 'hub',
  settingsSection: 'appearance',
  installs: [],
  downloads: [],
  stats: {},
  cache: {},
  selfInfo: null,
  mirrorResults: [],
  mirrorTesting: false,
  route: 'discover',
  view: null,
  repoLoading: false,
  repoError: '',
  libraryFilter: 'all',
  repoUi: { key: '', platform: '', channel: 'stable', release: 0, asset: 0 },
  discover: {
    feed: 'trending', page: 1, query: '', sort: 'best', lang: '',
    only: false, results: [], loading: false, error: '', history: [], total: 0, hasMore: false, platform: 'all', category: 'all',
    categoryCatalog: [], categoryLoading: false, categoryError: '',
  },
  onboarding: { needs: false, step: 0, theme: 'system', auto: true, tray: true, autostart: false },
  accent: '',
  busy: 0,
};

// Upstream keeps one browse platform shared by Home and Search, defaulting to
// the host OS. Mirror that instead of opening search on "all platforms".
S.discover.platform = localPlatform();

let toastSeq = 0;
let modalOpen = false;
let lastFocus = null;
let deviceTimer = null;
let deviceSession = null;
let renderTimer = null;

/* ------------------------------------------------------------------ API */

function api() {
  return (window.go && window.go.main && window.go.main.App) || null;
}

async function call(name, ...args) {
  const a = api();
  if (!a || typeof a[name] !== 'function') {
    throw new Error('本地服务尚未就绪，请通过 Komi Go 桌面端打开');
  }
  try {
    return await a[name](...args);
  } catch (err) {
    const msg = err && err.message ? err.message : String(err || '操作失败');
    throw new Error(msg.replace(/^Error:\s*/i, ''));
  }
}

function onEvent(name, fn) {
  try {
    if (window.runtime && typeof window.runtime.EventsOn === 'function') {
      window.runtime.EventsOn(name, fn);
    }
  } catch (_) { /* preview mode */ }
}

function busyOn() {
  S.busy += 1;
  const line = $('#loadingLine');
  if (line) { line.hidden = false; line.style.display = 'flex'; }
}
function busyOff() {
  S.busy = Math.max(0, S.busy - 1);
  if (!S.busy) {
    const line = $('#loadingLine');
    if (line) { line.hidden = true; line.style.display = 'none'; }
  }
}

/* --------------------------------------------------------------- helpers */

function fmtBytes(n) {
  n = Number(n || 0);
  if (!n) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i += 1; }
  return `${n.toFixed(n >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

function fmtDate(v) {
  if (!v) return '—';
  const d = new Date(v);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return '—';
  return d.toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' });
}

function timeAgo(v) {
  if (!v) return '';
  const d = new Date(v);
  const sec = (Date.now() - d.getTime()) / 1000;
  if (!Number.isFinite(sec) || sec < 0) return fmtDate(v);
  if (sec < 60) return '刚刚';
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`;
  if (sec < 86400 * 30) return `${Math.floor(sec / 86400)} 天前`;
  return fmtDate(v);
}

function fmtCount(n) {
  n = Number(n || 0);
  if (n >= 1000000) return `${(n / 1000000).toFixed(1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(n);
}

function hostOf(v) {
  return (v && (v.sourceHost || v.SourceHost)) || 'github.com';
}

function repoKeyOf(v) {
  const host = hostOf(v);
  return host === 'github.com' ? `${v.owner}/${v.name || v.repo}` : `${host}/${v.owner}/${v.name || v.repo}`;
}

function appKeyOf(app) {
  return app.id || (hostOf(app) === 'github.com' ? `${app.owner}/${app.repo}` : `${hostOf(app)}/${app.owner}/${app.repo}`);
}

function repoUrl(v) {
  const host = hostOf(v);
  return `https://${host}/${v.owner}/${v.name || v.repo}`;
}

function avatarHtml(item, cls = '') {
  const url = item && (item.avatarUrl || item.avatarURL);
  const letter = esc(((item && (item.owner || item.name || '?')) + '').slice(0, 1).toUpperCase());
  if (url && /^https:\/\//i.test(url)) {
    return `<img class="avatar ${cls}" src="${esc(url)}" alt="" loading="lazy" onerror="this.style.display='none'">`;
  }
  return `<div class="avatar ${cls}" aria-hidden="true">${letter}</div>`;
}

function initials(name) {
  return esc(String(name || '?').trim().slice(0, 1).toUpperCase() || '?');
}

function statusText(status) {
  return ({
    queued: '排队中', downloading: '下载中', verifying: '校验中', done: '已完成',
    failed: '失败', paused: '已暂停', canceled: '已取消', installing: '安装中',
    unverified: '未校验',
  }[status] || status);
}

function statusBadge(status) {
  const cls = status === 'done' ? 'success' : status === 'failed' || status === 'paused' ? 'warn' : 'primary';
  return `<span class="badge ${cls}">${esc(statusText(status))}</span>`;
}

function icon(name, cls = '') {
  return `<svg class="icon ${cls}" aria-hidden="true"><use href="#i-${name}"/></svg>`;
}

function wavy(pct, cls = '') {
  const p = clamp(Number(pct || 0), 0, 100);
  return `<div class="linear-progress ${cls}" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.round(p)}">
    <span style="width:${p}%"></span>
  </div>`;
}

function toast(msg, kind = '') {
  const id = ++toastSeq;
  const el = document.createElement('div');
  el.className = `toast ${kind}`;
  el.dataset.id = id;
  el.innerHTML = `${icon(kind === 'error' ? 'alert' : kind === 'success' ? 'check' : 'info')}<div>${esc(msg)}</div>`;
  $('#toastStack').appendChild(el);
  setTimeout(() => el.remove(), kind === 'error' ? 7000 : 3800);
}

function notifyError(err) {
  toast(err && err.message ? err.message : String(err), 'error');
}

function setTheme(mode) {
  const resolved = mode === 'system'
    ? (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
    : mode;
  document.documentElement.dataset.theme = resolved;
  document.documentElement.dataset.themePreference = mode;
  applyAccent();
  if (mode === 'system' && window.__themeMediaBound !== true) {
    window.__themeMediaBound = true;
    if (window.matchMedia) {
      window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
        if (document.documentElement.dataset.themePreference === 'system') setTheme('system');
      });
    }
  }
}

const ACCENT_KEYS = [
  '--primary', '--on-primary', '--primary-container', '--on-primary-container',
  '--secondary', '--on-secondary', '--secondary-container', '--on-secondary-container',
  '--tertiary', '--on-tertiary', '--tertiary-container', '--on-tertiary-container',
  '--surface', '--surface-dim', '--surface-bright', '--surface-lowest', '--surface-low',
  '--surface-container-low', '--surface-container', '--surface-high', '--surface-highest',
  '--on-surface', '--on-surface-variant', '--outline', '--outline-variant',
  '--inverse-surface', '--inverse-on-surface', '--scrim',
];

function parseAccent(rgb) {
  return rgb.replace('#', '').match(/[0-9a-f]{2}/gi).map((v) => parseInt(v, 16));
}

function rgbToHsl(rgb) {
  const [r, g, b] = rgb.map((v) => v / 255);
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  if (max === min) return [0, 0, l * 100];
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  let h = max === r ? (g - b) / d + (g < b ? 6 : 0) : max === g ? (b - r) / d + 2 : (r - g) / d + 4;
  return [h * 60, s * 100, l * 100];
}

function hslToRgb(h, s, l) {
  h = ((h % 360) + 360) % 360 / 360;
  s /= 100;
  l /= 100;
  const q = l < 0.5 ? l * (1 + s) : l + s - l * s;
  const p = 2 * l - q;
  const f = (t) => {
    t = (t + 1) % 1;
    if (t < 1 / 6) return p + (q - p) * 6 * t;
    if (t < 1 / 2) return q;
    if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6;
    return p;
  };
  return [f(h + 1 / 3), f(h), f(h - 1 / 3)].map((v) => Math.round(v * 255));
}

function accentLuminance(rgb) {
  const c = rgb.map((v) => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
}

function accentHex(rgb) {
  return '#' + rgb.map((v) => v.toString(16).padStart(2, '0')).join('');
}

function applyAccent() {
  const root = document.documentElement;
  const raw = String(S.accent || S.settings['ui.accent'] || '');
  if (!/^#[0-9a-f]{6}$/i.test(raw)) {
    ACCENT_KEYS.forEach((k) => root.style.removeProperty(k));
    return;
  }
  const [rawHue, rawSat] = rgbToHsl(parseAccent(raw));
  const hue = rawHue;
  const sat = Math.max(24, rawSat);
  const dark = root.dataset.theme === 'dark';
  const hsl = (h, s, l) => `hsl(${Math.round(((h % 360) + 360) % 360)} ${Math.round(Math.max(0, Math.min(100, s)))}% ${Math.round(Math.max(0, Math.min(100, l)))}%)`;
  const tint = Math.min(28, Math.max(16, sat * 0.3));
  const primaryL = dark ? 72 : Math.max(28, Math.min(56, rgbToHsl(parseAccent(raw))[2]));
  const primary = hslToRgb(hue, Math.min(82, sat), primaryL);
  const onPrimary = accentLuminance(primary) > 0.45 ? hsl(hue, sat, 10) : '#ffffff';
  const set = (k, v) => root.style.setProperty(k, v);

  set('--primary', hsl(hue, sat, primaryL));
  set('--on-primary', onPrimary);
  set('--primary-container', hsl(hue, sat, dark ? 20 : 88));
  set('--on-primary-container', hsl(hue, sat, dark ? 88 : 16));
  set('--secondary', hsl(hue, sat * 0.22, dark ? 78 : 36));
  set('--on-secondary', hsl(hue, sat * 0.2, dark ? 16 : 98));
  set('--secondary-container', hsl(hue, sat * 0.45, dark ? 26 : 88));
  set('--on-secondary-container', hsl(hue, sat * 0.4, dark ? 88 : 14));
  set('--tertiary', hsl(hue + 42, sat * 0.35, dark ? 74 : 35));
  set('--on-tertiary', hsl(hue + 42, sat * 0.3, dark ? 12 : 98));
  set('--tertiary-container', hsl(hue + 42, sat * 0.45, dark ? 24 : 86));
  set('--on-tertiary-container', hsl(hue + 42, sat * 0.4, dark ? 88 : 14));
  set('--surface', hsl(hue, tint, dark ? 9 : 97));
  set('--surface-dim', hsl(hue, tint, dark ? 8 : 84));
  set('--surface-bright', hsl(hue, tint, dark ? 26 : 98));
  set('--surface-lowest', hsl(hue, tint, dark ? 11 : 100));
  set('--surface-low', hsl(hue, tint, dark ? 12 : 95));
  set('--surface-container-low', hsl(hue, tint, dark ? 12 : 95));
  set('--surface-container', hsl(hue, tint, dark ? 15 : 93));
  set('--surface-high', hsl(hue, tint, dark ? 19 : 91));
  set('--surface-highest', hsl(hue, tint, dark ? 24 : 88));
  set('--on-surface', hsl(hue, tint, dark ? 90 : 11));
  set('--on-surface-variant', hsl(hue, tint, dark ? 74 : 28));
  set('--outline', hsl(hue, tint * 0.7, dark ? 55 : 50));
  set('--outline-variant', hsl(hue, tint * 0.8, dark ? 28 : 80));
  set('--inverse-surface', hsl(hue, tint, dark ? 90 : 15));
  set('--inverse-on-surface', hsl(hue, tint, dark ? 18 : 94));
  set('--scrim', dark ? 'rgba(0, 0, 0, 0.62)' : `rgba(${hslToRgb(hue, tint, 8).join(', ')}, 0.42)`);
}

function applyMotion() {
  document.documentElement.dataset.reduceMotion = S.settings['ui.reduce_motion'] === 'true' ? 'true' : 'false';
}

/* --------------------------------------------------------------- markdown */

function inlineMd(raw, base) {
  let s = esc(raw);
  // code spans first so their content is not linkified
  const codes = [];
  s = s.replace(/`([^`]+)`/g, (_, c) => { codes.push(c); return `\u0000${codes.length - 1}\u0000`; });

  s = s.replace(/!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g, (_, alt, u) => {
    const src = mirrorGH(resolveMdUrl(u, base));
    return src ? `<img src="${esc(src)}" alt="${esc(alt)}" loading="lazy">` : '';
  });
  s = s.replace(/\[([^\]]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g, (_, text, u) => {
    const href = resolveMdUrl(u, base);
    return href ? `<a href="${esc(href)}" data-external="1" rel="noreferrer">${text}</a>` : text;
  });
  s = s.replace(/(^|\s)(https?:\/\/[^\s<]+)/g, (_, pre, u) => `${pre}<a href="${esc(u)}" data-external="1" rel="noreferrer">${esc(u)}</a>`);
  s = s.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  s = s.replace(/\*([^*]+)\*/g, '<em>$1</em>');
  s = s.replace(/\u0000(\d+)\u0000/g, (_, i) => `<code>${codes[Number(i)]}</code>`);
  return s;
}

function resolveMdUrl(raw, base) {
  const u = String(raw || '').trim();
  if (/^https:\/\//i.test(u)) return u;
  if (/^http:\/\//i.test(u)) return u;
  if (/^\/\//.test(u)) return 'https:' + u;
  if (/^(javascript|data|file):/i.test(u)) return '';
  if (!base) return '';
  if (u.startsWith('/')) return base.replace(/\/[^/]*$/, '') + u;
  return base.replace(/\/$/, '') + '/' + u.replace(/^\.\//, '');
}

function safeRawHtml(src, base) {
  let s = String(src || '');
  s = s.replace(/<!--[\s\S]*?-->/g, '');
  s = s.replace(/<(script|style|iframe|object|embed|form|input|button|textarea|select|template)[\s\S]*?<\/\1>/gi, '');
  s = s.replace(/<(script|style|iframe|object|embed|form|input|button|textarea|select|template)[^>]*>/gi, '');
  s = s.replace(/\son[a-z]+=(?:"[^"]*"|'[^']*')/gi, '');
  s = s.replace(/\sstyle=(?:"[^"]*"|'[^']*')/gi, '');
  s = s.replace(/\ssrcset=(?:"[^"]*"|'[^']*')/gi, '');
  s = s.replace(/\s(href|src)\s*=\s*(?:"([^"]*)"|'([^']*)')/gi, (m, attr, dq, sq) => {
    const raw = dq != null ? dq : sq;
    const u = resolveMdUrl(raw, base);
    if (!u) return '';
    return ` ${attr}="${esc(attr === 'src' ? mirrorGH(u) : u)}"`;
  });
  return s;
}

function mdTableCells(line) {
  let s = String(line || '').trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|')) s = s.slice(0, -1);
  return s.replace(/\\\|/g, '\u0001').split('|').map((c) => c.replace(/\u0001/g, '|').trim());
}

function isMdTableSeparator(line) {
  const cells = mdTableCells(line);
  return cells.length >= 2 && cells.every((c) => /^:?-+:?$/.test(c.replace(/\s/g, '')));
}

function inlineTableCell(raw, base) {
  const tags = [];
  let s = String(raw || '').replace(/<[^>]+>/g, (m) => {
    const safe = safeRawHtml(m, base);
    if (!safe) return '';
    tags.push(safe);
    return `\u0002${tags.length - 1}\u0002`;
  });
  return inlineMd(s, base).replace(/\u0002(\d+)\u0002/g, (_, i) => tags[Number(i)] || '');
}

function renderMarkdown(src, base) {
  const lines = String(src || '').replace(/\r\n?/g, '\n').split('\n');
  const out = [];
  let code = false;
  let codeBuf = [];
  let list = null;
  let para = [];
  let html = [];

  const flushPara = () => {
    if (para.length) { out.push(`<p>${para.map((l) => inlineMd(l, base)).join('<br>')}</p>`); para = []; }
  };
  const flushList = () => {
    if (list) { out.push(`</${list}>`); list = null; }
  };
  const flushHtml = () => {
    if (html.length) { out.push(safeRawHtml(html.join('\n'), base)); html = []; }
  };
  const closeAll = () => { flushPara(); flushList(); flushHtml(); };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (html.length) {
      if (!line.trim()) { flushHtml(); continue; }
      html.push(line);
      continue;
    }
    if (/^```/.test(line.trim())) {
      if (!code) { closeAll(); code = true; codeBuf = []; }
      else { out.push(`<pre><code>${esc(codeBuf.join('\n'))}</code></pre>`); code = false; }
      continue;
    }
    if (code) { codeBuf.push(line); continue; }
    if (!line.trim()) { closeAll(); continue; }

    // GitHub-flavoured markdown tables: header row + |---| separator row.
    if (line.includes('|') && i + 1 < lines.length && isMdTableSeparator(lines[i + 1])) {
      closeAll();
      const headers = mdTableCells(line);
      const aligns = mdTableCells(lines[i + 1]).map((c) => {
        const left = c.startsWith(':');
        const right = c.endsWith(':');
        return left && right ? 'center' : right ? 'right' : '';
      });
      out.push(`<div class="md-table"><table><thead><tr>${
        headers.map((c, n) => `<th${aligns[n] ? ` class="align-${aligns[n]}"` : ''}>${inlineTableCell(c, base)}</th>`).join('')
      }</tr></thead><tbody>`);
      i += 2;
      while (i < lines.length && lines[i].trim() && lines[i].includes('|')) {
        const cells = mdTableCells(lines[i]);
        out.push(`<tr>${headers.map((_, n) => `<td${aligns[n] ? ` class="align-${aligns[n]}"` : ''}>${inlineTableCell(cells[n] || '', base)}</td>`).join('')}</tr>`);
        i++;
      }
      out.push('</tbody></table></div>');
      i--; // the outer loop increments past the first non-table line.
      continue;
    }

    // GitHub READMEs commonly use raw HTML for centered logos and badges.
    if (/^\s*<[a-zA-Z!/]/.test(line)) { closeAll(); html = [line]; continue; }

    const h = line.match(/^(#{1,6})\s+(.*)$/);
    if (h) {
      closeAll();
      const n = Math.min(3, h[1].length);
      out.push(`<h${n}>${inlineMd(h[2], base)}</h${n}>`);
      continue;
    }
    if (/^\s*([-*_])\s*\1\s*\1/.test(line)) { closeAll(); out.push('<hr>'); continue; }

    const ul = line.match(/^\s*[-*+]\s+(.*)$/);
    const ol = line.match(/^\s*\d+\.\s+(.*)$/);
    if (ul || ol) {
      flushPara();
      const want = ul ? 'ul' : 'ol';
      if (list !== want) { flushList(); out.push(`<${want}>`); list = want; }
      out.push(`<li>${inlineMd((ul || ol)[1], base)}</li>`);
      continue;
    }
    flushList();
    const q = line.match(/^\s*>\s?(.*)$/);
    if (q) { flushPara(); out.push(`<blockquote>${inlineMd(q[1], base)}</blockquote>`); continue; }
    para.push(line);
  }
  if (code) out.push(`<pre><code>${esc(codeBuf.join('\n'))}</code></pre>`);
  closeAll();
  return out.join('');
}

function mirrorGH(u) {
  const tpl = S.settings['network.mirrorActive'] || '';
  if (!tpl || !/^https:\/\/(?:raw|objects)\.githubusercontent\.com\//i.test(u) && !/^https:\/\/github\.com\//i.test(u)) return u;
  return tpl.includes('{url}') ? tpl.replaceAll('{url}', u) : u;
}

function readmeBase(view) {
  const host = hostOf(view);
  if (host === 'github.com') return `https://raw.githubusercontent.com/${view.owner}/${view.name}/HEAD/`;
  return `https://${host}/${view.owner}/${view.name}/raw/branch/HEAD/`;
}

/* ------------------------------------------------------------------ rail */

function updateCategoryRail() {
  $$('.category-item').forEach((el) => {
    el.classList.toggle('active', (el.dataset.category || 'all') === S.discover.category);
  });
}

function updateRail() {
  const auth = S.auth || {};
  updateCategoryRail();
  const acct = $('#railAccount');
  if (acct) {
    acct.textContent = auth.signedIn ? (auth.login || '已登录') : '未登录';
    acct.classList.toggle('signed', !!auth.signedIn);
    acct.dataset.tip = auth.signedIn ? `${auth.login} · 点击管理账户` : '点击登录 GitHub';
  }
  const up = Number((S.stats && S.stats.updates) || 0);
  const badge = $('#railUpdateBadge');
  if (badge) { badge.hidden = up <= 0; badge.style.display = up > 0 ? 'inline-flex' : 'none'; badge.textContent = up > 99 ? '99+' : String(up); }
  const active = (S.downloads || []).filter((j) => ['queued', 'downloading', 'verifying', 'installing'].includes(j.status)).length;
  const db = $('#railDownloadBadge');
  if (db) { db.hidden = active <= 0; db.style.display = active > 0 ? 'inline-flex' : 'none'; db.textContent = String(active); }
  const chip = $('#versionChip');
  if (chip && auth.version) chip.textContent = 'v' + auth.version;
  const foot = $('#railFoot');
  if (foot && auth.elevated) foot.textContent = '管理员模式 · WebView2';
}

function navigate(route) {
  S.route = route;
  if (route === 'settings') S.settingsScreen = 'hub';
  const main = $('#main');
  if (main) main.scrollTop = 0;
  render();
  if (route === 'settings') loadSettingsPage();
  if (route === 'downloads') loadDownloads();
  if (route === 'library') loadLibrary();
}

/* ------------------------------------------------------------ data loading */

async function loadAuth() {
  try {
    S.auth = (await call('AuthState')) || {};
    setTheme(S.auth.theme || S.settings.theme || 'system');
  } catch (e) { console.error(e); }
  updateRail();
}

async function loadSettings() {
  try { S.settings = (await call('GetSettings')) || {}; } catch (e) { console.error(e); }
  S.accent = String(S.settings['ui.accent'] || '');
  applyMotion();
  setTheme(S.settings.theme || 'system');
}

async function loadLibrary() {
  try {
    S.installs = (await call('ListInstalled')) || [];
    S.stats = (await call('LibraryStats')) || {};
  } catch (e) { notifyError(e); }
  updateRail();
}

async function loadDownloads() {
  try { S.downloads = (await call('ListDownloads')) || []; } catch (e) { console.error(e); }
  updateRail();
}

async function loadCache() {
  try { S.cache = (await call('CacheInfo')) || {}; } catch (e) { console.error(e); }
}

function scheduleRender() {
  if (S.route === 'settings') return;
  if (renderTimer) return;
  renderTimer = setTimeout(() => { renderTimer = null; render(); }, 150);
}

/* -------------------------------------------------------------- discover */

function searchHistoryLoad() {
  try { S.discover.history = JSON.parse(localStorage.getItem('komi.searchHistory') || '[]').slice(0, 8); } catch (_) { S.discover.history = []; }
}
function searchHistoryPush(q) {
  q = q.trim();
  if (!q) return;
  const h = S.discover.history.filter((x) => x !== q);
  h.unshift(q);
  S.discover.history = h.slice(0, 8);
  try { localStorage.setItem('komi.searchHistory', JSON.stringify(S.discover.history)); } catch (_) { /* ignore */ }
}

function discoverParams() {
  return {
    q: S.discover.query.trim(),
    feed: S.discover.feed,
    page: S.discover.page,
    sort: S.discover.sort,
    lang: S.discover.lang,
    only: S.discover.only,
  };
}

async function loadDiscover() {
  const d = S.discover;
  d.loading = true;
  d.error = '';
  renderDiscover();
  try {
    let list;
    const timeout = new Promise((_, reject) => setTimeout(() => reject(new Error('发现服务响应超时，请检查网络或代理后重试')), d.query.trim() ? 35000 : 25000));
    if (d.query.trim()) {
      const page = await Promise.race([call('SearchPage', d.query.trim(), d.page, d.sort, d.lang, d.platform === 'all' ? '' : d.platform), timeout]);
      list = (page && page.items) || [];
      d.total = Number(page && page.total) || list.length;
      d.hasMore = !!(page && page.hasMore);
    } else {
      list = await Promise.race([call('Discover', d.feed, d.page, false), timeout]);
      d.total = 0;
      d.hasMore = (list || []).length >= 20;
    }
    d.results = list || [];
  } catch (e) {
    d.error = e.message;
    d.results = [];
  } finally {
    d.loading = false;
    renderDiscover();
  }
}

function platformLabel(p) {
  return p === 'windows' ? 'Windows' : p === 'macos' ? 'macOS' : p === 'linux' ? 'Linux' : p === 'android' ? 'Android' : p;
}

function localPlatform() {
  const os = String((S.auth.target && S.auth.target.os) || '').toLowerCase();
  if (os === 'windows' || os === 'macos' || os === 'linux') return os;
  const nav = String((navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || '').toLowerCase();
  if (/mac/.test(nav)) return 'macos';
  if (/linux/.test(nav)) return 'linux';
  return 'windows';
}

function platformChips(r, compact) {
  const local = localPlatform();
  const list = r.platforms || [];
  if (!list.length) return compact ? '' : '<span class="badge outline">仅源码</span>';
  return list.map((p) => `<span class="badge ${p === local ? 'primary' : 'outline'} platform-chip" title="${p === local ? '匹配本机系统' : ''}">${platformLabel(p)}</span>`).join('');
}

const DISCOVER_CATEGORIES = [
  { id: 'ai', label: '人工智能', codes: ['ai'] },
  { id: 'privacy', label: '隐私', codes: ['privacy', 'security'] },
  { id: 'network', label: '网络', codes: ['networking', 'browser', 'messaging'] },
  { id: 'media', label: '媒体', codes: ['audio', 'video', 'photo'] },
  { id: 'social', label: '社交', codes: ['social'] },
  { id: 'reading', label: '阅读', codes: ['reader', 'notes'] },
  { id: 'tool', label: '工具', codes: ['launcher', 'backup', 'self-hosted'] },
];

function discoverCategoryLabel(id) {
  if (id === 'all') return '全部';
  const c = DISCOVER_CATEGORIES.find((x) => x.id === id);
  return c ? c.label : '全部';
}

function repoMatchesCategory(r, id) {
  if (id === 'all') return true;
  const c = DISCOVER_CATEGORIES.find((x) => x.id === id);
  if (!c) return false;
  return (r.topicCodes || []).some((code) => c.codes.includes(String(code).toLowerCase()));
}

function discoverVisibleResults() {
  const d = S.discover;
  const isSearch = !!d.query.trim();
  const all = (!isSearch && d.category !== 'all') ? (d.categoryCatalog || []) : (d.results || []);
  if (d.category === 'all') return all;
  return all.filter((r) => repoMatchesCategory(r, d.category));
}

async function loadCategoryCatalog() {
  const d = S.discover;
  if (d.categoryCatalog.length || d.categoryLoading) return;
  d.categoryLoading = true;
  d.categoryError = '';
  renderDiscover();
  try {
    const list = await call('CategoryCatalog');
    d.categoryCatalog = list || [];
  } catch (e) {
    d.categoryError = e.message;
  } finally {
    d.categoryLoading = false;
    if (S.route === 'discover' && !S.discover.query.trim()) renderDiscover();
  }
}

function repoCard(r) {
  const host = hostOf(r);
  const inst = (S.installs || []).find((a) => a && a.owner === r.owner && a.repo === r.name && (!a.sourceHost || a.sourceHost === host));
  const stateBadge = inst
    ? (inst.updateAvailable ? '<span class="badge warn">可更新</span>' : '<span class="badge success">已安装</span>')
    : '';
  return `<article class="card clickable repo-card" data-action="open-repo" data-host="${esc(host)}" data-owner="${esc(r.owner)}" data-repo="${esc(r.name)}" tabindex="0" role="button">
    <div class="top">${avatarHtml(r)}
      <div class="grow" style="min-width:0">
        <div class="name ellipsis">${esc(r.name)}</div>
        <div class="tiny muted ellipsis">@${esc(r.owner)}${r.language ? ` <span class="dot-sep">◆</span> ${esc(r.language)}` : ''}</div>
      </div>
      ${stateBadge}
    </div>
    <div class="desc">${esc(r.description || '没有简介')}</div>
    <div class="platform-row">${platformChips(r, true)}</div>
    <div class="card-divider"></div>
    <div class="foot">
      <span class="meta">${icon('star')} ${fmtCount(r.stars)}</span>
      ${Number(r.downloadCount || 0) > 0 ? `<span class="meta">${icon('download')} ${fmtCount(r.downloadCount)}</span>` : ''}
      ${(r.latestReleaseAt || r.updatedAt) && timeAgo(r.latestReleaseAt || r.updatedAt) !== '—' ? `<span class="meta">${icon('tag')} ${timeAgo(r.latestReleaseAt || r.updatedAt)}</span>` : ''}
      <span class="read-more">Read ${icon('back', 'flip')}</span>
    </div>
  </article>`;
}

function renderDiscover() {
  const d = S.discover;
  const main = $('#main');
  const scroll = main.scrollTop;
  const isSearch = !!d.query.trim();
  const categoryLabel = discoverCategoryLabel(d.category);
  const visible = discoverVisibleResults();
  const categoryFetching = !isSearch && d.category !== 'all' && d.categoryLoading;
  const categoryFailed = !isSearch && d.category !== 'all' && !!d.categoryError;
  const tabs = [['trending', '热门'], ['fresh', '新鲜'], ['classic', '经典'], ['windows', 'Windows']];
  const hero = `
    <section class="card hero hero-banner">
      <div class="hero-body">
        <div class="hstack" style="margin-bottom:8px">
          <span class="badge primary">Windows · Go</span>
          <span class="badge outline">M3 Expressive</span>
        </div>
        <div class="hero-title">把 GitHub Release<br>变成<em>能装的桌面应用</em></div>
        <div class="hero-copy">搜索、下载、校验、安装、更新，一条链路做完。支持 GitHub、Codeberg 等 Forgejo 源，资产按 CPU 架构和安装包类型自动挑选。</div>
        <div class="hero-actions">
          <button class="btn filled" data-action="focus-search">${icon('search')} 搜索应用</button>
          <button class="btn tonal" data-action="check-updates">${icon('refresh')} 检查更新</button>
        </div>
      </div>
      <aside class="hero-aside">
        <div class="hstack"><span class="badge tertiary">本机</span><span class="tiny muted">${esc((S.auth.target && (S.auth.target.os + '/' + S.auth.target.arch)) || 'windows/amd64')}</span></div>
        <div class="hero-stats">
          <div class="stat"><div class="num">${Number(S.stats.installed || 0)}</div><div class="cap">已安装</div></div>
          <div class="stat"><div class="num">${Number(S.stats.updates || 0)}</div><div class="cap">待更新</div></div>
          <div class="stat"><div class="num">${fmtBytes(S.stats.size || 0)}</div><div class="cap">占用空间</div></div>
        </div>
        <div class="hero-note">${icon('shield', 'sm')} 自动匹配架构与安装包类型 · SHA-256 校验</div>
      </aside>
    </section>`;

  const toolbar = `
    <div class="discover-toolbar">
      ${isSearch ? '' : `<div class="tab-group" role="tablist" aria-label="发现分类">
        ${tabs.map(([k, label]) => `<button class="tab" role="tab" aria-selected="${d.feed === k}" data-action="feed" data-feed="${k}">${label}</button>`).join('')}
      </div>`}
      <div class="hstack" style="margin-left:auto">
        <div class="tab-group platform-tabs" role="tablist" aria-label="安装包平台">
          ${[['all', '全部'], ['windows', 'Windows'], ['macos', 'macOS'], ['linux', 'Linux']]
            .map(([v, l]) => `<button class="tab" role="tab" aria-selected="${d.platform === v}" data-action="platform" data-platform="${v}">${l}${v === localPlatform() ? ' · 本机' : ''}</button>`).join('')}
        </div>
        ${isSearch ? `<select class="input" style="width:auto" data-action="lang" aria-label="语言筛选">
          ${[['', '所有语言'], ['Go', 'Go'], ['Rust', 'Rust'], ['TypeScript', 'TypeScript'], ['JavaScript', 'JavaScript'], ['Python', 'Python'], ['C#', 'C#'], ['C++', 'C++'], ['Java', 'Java']]
            .map(([v, l]) => `<option value="${v}" ${d.lang === v ? 'selected' : ''}>${l}</option>`).join('')}
        </select>` : ''}
        ${isSearch ? `<select class="input" style="width:auto" data-action="sort">
          <option value="best" ${d.sort === 'best' ? 'selected' : ''}>最佳匹配</option>
          <option value="stars" ${d.sort === 'stars' ? 'selected' : ''}>按星标</option>
          <option value="releases" ${d.sort === 'releases' ? 'selected' : ''}>最近发布</option>
          <option value="updated" ${d.sort === 'updated' ? 'selected' : ''}>按更新</option>
          <option value="forks" ${d.sort === 'forks' ? 'selected' : ''}>按复刻</option>
        </select>` : ''}
      </div>
    </div>`;

  const searchbar = `
    <form class="searchbar" id="searchForm" role="search">
      <span class="searchbar-icon">${icon('search')}</span>
      <input id="searchInput" type="search" name="q" autocomplete="off" spellcheck="false"
        value="${esc(d.query)}" placeholder="搜索仓库、应用名，或粘贴 github.com/owner/repo 链接" aria-label="搜索 GitHub 仓库">
      ${d.query ? `<button class="icon-btn" type="button" data-action="clear-search" aria-label="清空搜索">${icon('close', 'sm')}</button>` : ''}
      <button class="btn filled small" type="submit">搜索</button>
    </form>
    ${d.history.length && !isSearch ? `<div class="history-row" aria-label="最近搜索">
      <span class="tiny muted">最近</span>
      ${d.history.map((q) => `<button class="chip" data-action="history-search" data-q="${esc(q)}">${esc(q)}</button>`).join('')}
      <button class="btn text small history-clear" data-action="clear-history" aria-label="清除搜索历史">${icon('close', 'sm')} 清除历史</button>
    </div>` : ''}`;

  let list;
  if (d.loading || categoryFetching) {
    list = `<div class="repo-grid">${'<div class="skeleton"></div>'.repeat(6)}</div>`;
  } else if (categoryFailed) {
    list = `<div class="empty"><div class="art">${icon('alert', 'lg')}</div><h3>分类数据加载失败</h3><p>${esc(d.categoryError)}</p><button class="btn tonal" data-action="retry-category">${icon('refresh')} 重试</button></div>`;
  } else if (d.error) {
    list = `<div class="empty"><div class="art">${icon('alert', 'lg')}</div><h3>搜索失败</h3><p>${esc(d.error)}</p><button class="btn tonal" data-action="retry-discover">${icon('refresh')} 重试</button></div>`;
  } else if (!visible.length) {
    list = d.results.length && d.category !== 'all'
      ? `<div class="empty"><div class="art">${icon('inbox', 'lg')}</div><h3>“${esc(categoryLabel)}”分类暂无应用</h3><p>完整 Windows 应用榜里没有归到这个分类的仓库，可以看看全部分类。</p><button class="btn tonal" data-action="category" data-category="all">查看全部</button></div>`
      : `<div class="empty"><div class="art">${icon('inbox', 'lg')}</div><h3>没有找到结果</h3><p>换一个关键词，或切换到“全部”平台查看所有安装包。</p><button class="btn tonal" data-action="platform" data-platform="all">查看全部平台</button></div>`;
  } else {
    list = `<div class="repo-grid">${visible.map(repoCard).join('')}</div>
      ${(!isSearch && d.category !== 'all') ? '' : `<div class="pager">
        <button class="icon-btn" data-action="page" data-page="${d.page - 1}" ${d.page <= 1 ? 'disabled' : ''} aria-label="上一页">${icon('back')}</button>
        <span class="badge outline">第 ${d.page} 页</span>
        <button class="icon-btn" data-action="page" data-page="${d.page + 1}" ${d.hasMore ? '' : 'disabled'} aria-label="下一页">${icon('back', 'flip')}</button>
      </div>`}`;
  }

  main.innerHTML = `${isSearch ? '' : hero}
    ${searchbar}
    <div class="page-head" style="margin-top:${isSearch ? '0' : '24px'}">
      <div>
        <h2>${isSearch ? '搜索结果' : (d.category === 'all' ? '发现' : esc(categoryLabel))}</h2>
        <div class="sub">${isSearch ? `“${esc(d.query)}” · ${d.category === 'all' ? '' : esc(categoryLabel) + '分类 · '}${d.platform === 'all' ? '所有平台' : platformLabel(d.platform)}安装包` : `${d.category === 'all' ? '所有平台安装包' : '分类：' + esc(categoryLabel)} · 本机 ${platformLabel(localPlatform())} 优先推荐`}</div>
      </div>
      ${isSearch ? '<button class="btn text" data-action="clear-search">清除搜索</button>' : ''}
    </div>
    ${toolbar}
    ${list}`;
  bindSearchForm();
  main.scrollTop = scroll;
}

function bindSearchForm() {
  const form = $('#searchForm');
  if (form) form.addEventListener('submit', onSearchSubmit);
  const input = $('#searchInput');
  if (input) {
    input.addEventListener('input', () => { S.discover.queryLive = input.value; });
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); onSearchSubmit(e); }
    });
  }
}

async function onSearchSubmit(e) {
  if (e) e.preventDefault();
  const input = $('#searchInput');
  const q = (input ? input.value : S.discover.query).trim();
  if (q && Array.from(q).length < 2) {
    toast('搜索词至少需要 2 个字符', 'warn');
    return;
  }
  S.discover.query = q;
  S.discover.page = 1;
  if (q) searchHistoryPush(q);
  const main = $('#main');
  if (main) main.scrollTop = 0;
  render(); // rebuild with search mode and current query
  await loadDiscover();
}

/* ------------------------------------------------------------------ repo */

async function openRepo(host, owner, repo, force = false) {
  S.route = 'repo';
  S.repoLoading = true;
  S.repoError = '';
  S.view = null;
  S.repoUi.key = '';
  render();
  busyOn();
  try {
    let view;
    if (host && host !== 'github.com') {
      view = await call('RefreshRepo', host, owner, repo, force);
    } else if (force) {
      view = await call('RefreshRepo', 'github.com', owner, repo, true);
    } else {
      view = await call('SearchRepo', `${owner}/${repo}`);
    }
    S.view = view;
  } catch (e) {
    S.repoError = e.message;
  } finally {
    S.repoLoading = false;
    busyOff();
    if (S.route === 'repo') render();
  }
}

function releaseNotesModal(index) {
  const view = S.view;
  if (!view) return;
  const rel = view.releases[index];
  if (!rel) return;
  openModal(`<div class="modal-head"><div><div class="modal-title">${esc(rel.name || rel.tagName)}</div><div class="modal-sub">${esc(rel.tagName)} · ${fmtDate(rel.publishedAt)}</div></div><button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
    <div class="markdown">${rel.body ? renderMarkdown(rel.body, readmeBase(view)) : '<p class="muted">这个 Release 没有写说明。</p>'}</div>`, 'wide');
}

function assetUsable(a) {
  return a && !a.junk;
}

function assetInstallable(a) {
  return a && !a.junk && a.installable !== false;
}

function assetPlatform(a) {
  return a && a.platform ? a.platform : '';
}

function platformGroups(assets) {
  const order = ['windows', 'macos', 'linux', 'android'];
  const groups = {};
  for (const a of assets || []) {
    if (a.junk) continue;
    const p = assetPlatform(a);
    if (!p) continue;
    (groups[p] = groups[p] || []).push(a);
  }
  return order.filter((p) => groups[p]);
}

function repoAvailablePlatforms(view) {
  const fromRepo = (view && view.platforms) || [];
  const seen = {};
  const out = [];
  const add = (p) => { if (p && !seen[p]) { seen[p] = true; out.push(p); } };
  fromRepo.forEach(add);
  ((view && view.releases) || []).forEach((rel) => {
    (rel.assets || []).forEach((a) => { if (!a.junk) add(assetPlatform(a)); });
  });
  const order = ['windows', 'macos', 'linux', 'android'];
  return out.sort((a, b) => order.indexOf(a) - order.indexOf(b));
}

function repoChannelReleases(view, channel) {
  return ((view && view.releases) || []).filter((rel) => {
    if (rel.draft) return false;
    if (channel === 'stable') return !rel.prerelease;
    if (channel === 'pre') return !!rel.prerelease;
    return true;
  });
}

function repoPlatformAssets(rel, platform) {
  return ((rel && rel.assets) || []).filter((a) => !a.junk && assetPlatform(a) === platform);
}

function repoLatestForPlatform(view, platform) {
  const releases = ((view && view.releases) || []).filter((rel) => !rel.draft);
  for (const stableOnly of [true, false]) {
    for (const rel of releases) {
      if (stableOnly && rel.prerelease) continue;
      if (repoPlatformAssets(rel, platform).length) return rel;
    }
  }
  return null;
}

function ensureRepoUi(view) {
  const key = view ? repoKeyOf(view) : '';
  const ui = S.repoUi;
  if (ui.key === key && ui.platform) return ui;
  const platforms = repoAvailablePlatforms(view);
  let platform = localPlatform();
  if (!platforms.includes(platform)) platform = platforms[0] || 'windows';
  ui.key = key;
  ui.platform = platform;
  ui.channel = repoChannelReleases(view, 'stable').length ? 'stable' : 'all';
  ui.release = 0;
  ui.asset = 0;
  repoResetSelection(view, ui);
  return ui;
}

function repoResetSelection(view, ui) {
  const list = repoChannelReleases(view, ui.channel);
  let releaseIndex = -1;
  for (let i = 0; i < view.releases.length; i++) {
    const rel = view.releases[i];
    if (!list.includes(rel)) continue;
    if (repoPlatformAssets(rel, ui.platform).length) { releaseIndex = i; break; }
  }
  if (releaseIndex < 0 && list.length) releaseIndex = view.releases.indexOf(list[0]);
  if (releaseIndex < 0) releaseIndex = 0;
  ui.release = releaseIndex;
  const rel = view.releases[releaseIndex];
  const assets = repoPlatformAssets(rel, ui.platform);
  const rec = assets.findIndex((a) => a.recommended);
  const first = rec >= 0 ? assets[rec] : assets[0];
  ui.asset = first ? rel.assets.indexOf(first) : 0;
}

function repoSelected(view) {
  const ui = ensureRepoUi(view);
  const rel = view.releases[ui.release] || null;
  const asset = rel && rel.assets ? rel.assets[ui.asset] || null : null;
  return { ui, rel, asset };
}

function repoSelectorHtml(view) {
  const ui = ensureRepoUi(view);
  const platforms = repoAvailablePlatforms(view);
  const channels = [['stable', '稳定版'], ['pre', '预发布'], ['all', '全部']];
  const releaseList = repoChannelReleases(view, ui.channel);
  const selectedRel = view.releases[ui.release];
  const assets = repoPlatformAssets(selectedRel, ui.platform);
  const sortedAssets = assets.slice().sort((a, b) => {
    const rank = (n) => /x86_64|amd64|x64|universal|noarch/i.test(n) ? 0 : /arm64|aarch64/i.test(n) ? 1 : 2;
    return rank(a.name) - rank(b.name) || String(a.name).localeCompare(String(b.name));
  });
  const local = ui.platform === localPlatform();
  const canInstall = local && selectedRel && view.releases[ui.release] && view.releases[ui.release].assets[ui.asset]
    && assetInstallable(view.releases[ui.release].assets[ui.asset]);
  const versionButtons = platforms.map((p) => {
    const latestRel = repoLatestForPlatform(view, p);
    return `<button class="platform-pill ${p === ui.platform ? 'active' : ''}" data-action="repo-platform" data-platform="${p}">
      <span class="platform-pill-icon">${p === 'windows' ? '⊞' : p === 'macos' ? '⌘' : '◈'}</span>
      <span class="platform-pill-name">${platformLabel(p)}</span>
      <span class="platform-pill-ver">${esc((latestRel && latestRel.tagName) || '—')}</span>
    </button>`;
  }).join('');
  return `<section class="card repo-selector" id="repoSelector">
    <div class="selector-row">
      <div class="platform-switch">${versionButtons || '<span class="tiny muted">没有可下载资源</span>'}</div>
    </div>
    <div class="tab-group channel-tabs" role="tablist" aria-label="版本通道">
      ${channels.map(([v, label]) => `<button class="tab" role="tab" aria-selected="${ui.channel === v}" data-action="repo-channel" data-channel="${v}">${label}</button>`).join('')}
    </div>
    <div class="selector-grid">
      <label class="field"><span class="field-label">资源</span>
        <select class="input select" data-repo-select="asset" aria-label="资源">
          ${sortedAssets.length ? sortedAssets.map((a) => `<option value="${selectedRel.assets.indexOf(a)}" ${a === selectedRel.assets[ui.asset] ? 'selected' : ''}>${esc(a.name)}${a.recommended ? ' · 推荐' : ''}</option>`).join('') : '<option value="">没有${platformLabel(ui.platform)}资源</option>'}
        </select>
      </label>
      <label class="field"><span class="field-label">版本</span>
        <select class="input select" data-repo-select="release" aria-label="版本">
          ${releaseList.length ? releaseList.map((rel) => `<option value="${view.releases.indexOf(rel)}" ${view.releases.indexOf(rel) === ui.release ? 'selected' : ''}>${esc(rel.tagName)}${rel.name && rel.name !== rel.tagName ? ' · ' + esc(rel.name) : ''}</option>`).join('') : '<option value="">没有版本</option>'}
        </select>
      </label>
    </div>
    ${selectedRel && assets.length
      ? `<button class="btn filled install-cta" data-action="install-selected">
          ${icon('download')} ${canInstall ? '安装' : '下载'} ${esc(selectedRel.tagName)}
          <span class="tiny" style="opacity:.82">${esc(selectedRel.assets[ui.asset].name)} · ${fmtBytes(selectedRel.assets[ui.asset].size)}${local ? '' : ' · 用于转移'}</span>
        </button>`
      : `<button class="btn outlined install-cta" disabled>${icon('alert')} 该版本没有 ${platformLabel(ui.platform)} 资源</button>`}
  </section>`;
}

function pickLatestInstall(releases) {
  const arr = releases || [];
  for (const allowPre of [false, true]) {
    for (let i = 0; i < arr.length; i++) {
      const rel = arr[i];
      if (!rel || rel.draft || (!allowPre && rel.prerelease)) continue;
      const assets = rel.assets || [];
      const rec = assets.findIndex((a) => a && a.recommended && assetInstallable(a));
      const any = assets.findIndex((a) => assetInstallable(a));
      const j = rec >= 0 ? rec : any;
      if (j >= 0) return { rel, i, j, asset: assets[j], pre: !!rel.prerelease };
    }
  }
  return null;
}

function repoPage() {
  const view = S.view;
  if (S.repoLoading) {
    return `<div class="page-head"><button class="icon-btn" data-action="back-discover" aria-label="返回">${icon('back')}</button><div class="grow"></div></div>
      <div class="skeleton" style="height:120px"></div><div class="skeleton" style="height:220px;margin-top:16px"></div>`;
  }
  if (S.repoError || !view) {
    return `<div class="page-head"><button class="icon-btn" data-action="back-discover" aria-label="返回">${icon('back')}</button></div>
      <div class="empty"><div class="art">${icon('alert', 'lg')}</div><h3>仓库加载失败</h3><p>${esc(S.repoError || '未知错误')}</p>
      <button class="btn tonal" data-action="retry-repo">${icon('refresh')} 重试</button></div>`;
  }

  const ready = view.platformReady;
  ensureRepoUi(view);
  const readmeHead = `<div class="spread"><div class="hstack"><h2>README</h2>${view.readmeStale ? '<span class="badge warn">缓存</span>' : ''}</div>
    ${view.readme ? `<button class="btn text small" data-action="toggle-readme">展开/收起</button>` : ''}</div>`;

  return `<div class="page-head">
      <div class="hstack" style="align-items:flex-start">
        <button class="icon-btn" data-action="back-discover" aria-label="返回">${icon('back')}</button>
        <div>
          <h2>仓库详情</h2>
          <div class="sub">${esc(hostOf(view) === 'github.com' ? 'GitHub' : hostOf(view))}</div>
        </div>
      </div>
      <div class="page-actions">
        <button class="btn outlined small" data-action="refresh-repo">${icon('refresh', 'sm')} 刷新</button>
        <button class="btn tonal small" data-action="open-repo-page">${icon('external', 'sm')} 浏览器打开</button>
      </div>
    </div>
    ${view.stale ? `<div class="stale-banner">${icon('info')}<div class="grow">当前展示的是离线缓存（${esc(timeAgo(view.cachedAt))}）</div></div>` : ''}
    <section class="card">
      <div class="repo-hero">
        ${avatarHtml(view, 'lg')}
        <div style="min-width:0">
          <h1 class="ellipsis">${esc(view.name)}</h1>
          <div class="owner">${esc(view.owner)} · ${esc(hostOf(view))}</div>
          <div class="muted" style="margin-top:8px;max-width:640px">${esc(view.description || '没有简介')}</div>
          <div class="repo-meta">
            <span class="meta">${icon('star')} ${fmtCount(view.stars)}</span>
            <span class="meta">${icon('code')} ${fmtCount(view.forks)}</span>
            <span class="meta">${icon('alert')} ${fmtCount(view.openIssues)} issues</span>
            <span class="meta">${icon('tag')} 更新于 ${timeAgo(view.updatedAt)}</span>
            ${view.license ? `<span class="meta">${icon('shield')} ${esc(view.license)}</span>` : ''}
            ${view.archived ? '<span class="badge error">已归档</span>' : ''}
            ${platformChips(view)}
            ${ready ? `<span class="badge primary">${platformLabel(localPlatform())} 可安装</span>` : `<span class="badge outline">${esc(view.platformNote || '安装性未知')}</span>`}
          </div>
          <div class="tag-row" style="margin-top:10px">${(view.topics || []).map((t) => `<span class="topic">${esc(t)}</span>`).join('')}</div>
        </div>
        <div class="actions">
          <button class="btn text" data-action="open-issues">${icon('info')} Issues</button>
        </div>
      </div>
    </section>
    ${repoSelectorHtml(view)}
    <section class="card readme-card">
      ${readmeHead}
      ${view.readme
        ? `<div class="readme-body collapsed" id="readmeBody"><div class="markdown">${renderMarkdown(view.readme, readmeBase(view))}</div></div>
           <div class="readme-actions"><button class="btn text small readme-toggle" data-action="toggle-readme"><span class="readme-label">展开全文</span>${icon('chevron')}</button></div>`
        : `<div class="note">${esc(view.readmeError || '这个仓库没有 README。')}</div>`}
    </section>
    <div class="spread" style="margin-top:18px">
      <span class="tiny muted">API 配额：${Number((view.rateLimit && view.rateLimit.remaining) || 0)}/${Number((view.rateLimit && view.rateLimit.limit) || 0)} · 缓存于 ${esc(timeAgo(view.cachedAt) || '—')}</span>
      <button class="btn text small" data-action="open-downloads">${icon('folder', 'sm')} 下载目录</button>
    </div>`;
}

/* ----------------------------------------------------------------- library */

function libraryFiltered() {
  const f = S.libraryFilter;
  return (S.installs || []).filter((a) => {
    switch (f) {
      case 'updates': return a.updateAvailable;
      case 'tracked': return a.libraryStatus === 'tracked';
      case 'pending': return a.pendingVersion || a.pendingInstallPath;
      case 'uninstalled': return a.libraryStatus === 'uninstalled';
      default: return a.libraryStatus !== 'uninstalled';
    }
  });
}

function appRow(a) {
  const status = a.libraryStatus || 'installed';
  const pending = !!(a.pendingVersion || a.pendingInstallPath);
  const update = a.updateAvailable;
  const name = a.displayName || a.repo;
  const version = a.installedVersion || a.installedReleaseTag || (status === 'tracked' ? '仅跟踪' : '—');
  return `<div class="app-row ${status === 'uninstalled' ? 'uninstalled' : ''}" data-action="app-menu" data-id="${esc(a.id)}" tabindex="0" role="button">
      <div class="avatar">${initials(name)}</div>
      <div style="min-width:0">
        <div class="name">${esc(name)}
          ${update ? '<span class="badge error">可更新</span>' : ''}
          ${pending ? '<span class="badge warn">待确认</span>' : ''}
          ${a.pinned ? '<span class="badge outline">已固定</span>' : ''}
          ${status === 'tracked' ? '<span class="badge outline">仅跟踪</span>' : ''}
          ${status === 'uninstalled' ? '<span class="badge outline">未安装</span>' : ''}
        </div>
        <div class="repo">${esc(a.owner)}/${esc(a.repo)}${a.sourceHost && a.sourceHost !== 'github.com' ? ' · ' + esc(a.sourceHost) : ''}</div>
      </div>
      <div class="col"><strong>${esc(version)}</strong><span>${update && a.latestReleaseTag ? '→ ' + esc(a.latestReleaseTag) : '已安装版本'}</span></div>
      <div class="col"><strong>${fmtBytes(a.installSize || 0)}</strong><span>${a.installDir ? '安装目录已记录' : '目录未知'}</span></div>
      <div class="actions">
        ${update ? `<button class="btn tonal small" data-action="update-app" data-id="${esc(a.id)}">${icon('download', 'sm')} 更新</button>` : ''}
        ${status !== 'uninstalled' && status !== 'tracked' ? `<button class="icon-btn" data-action="open-app" data-id="${esc(a.id)}" data-tip="打开应用" aria-label="打开应用">${icon('play', 'sm')}</button>` : ''}
        <button class="icon-btn" data-action="app-menu" data-id="${esc(a.id)}" data-tip="更多操作" aria-label="更多操作">${icon('chevron')}</button>
      </div>
    </div>`;
}

function renderLibrary() {
  const main = $('#main');
  const scroll = main.scrollTop;
  const list = libraryFiltered();
  const filters = [['all', '全部'], ['updates', '有更新'], ['tracked', '仅跟踪'], ['pending', '待确认'], ['uninstalled', '未安装']];
  const stats = S.stats || {};
  main.innerHTML = `
    <div class="page-head">
      <div><h1>应用库</h1><div class="sub">安装记录、真实安装目录、版本与更新都在这里。</div></div>
      <div class="page-actions">
        <button class="btn outlined small" data-action="import-library">${icon('inbox', 'sm')} 导入</button>
        <button class="btn outlined small" data-action="export-library">${icon('code', 'sm')} 导出</button>
        <button class="btn filled small" data-action="check-updates">${icon('refresh', 'sm')} 检查更新</button>
      </div>
    </div>
    <div class="stat-row">
      <div class="stat"><div class="num">${Number(stats.installed || 0)}</div><div class="cap">已安装</div></div>
      <div class="stat"><div class="num">${Number(stats.updates || 0)}</div><div class="cap">可用更新</div></div>
      <div class="stat"><div class="num">${fmtBytes(stats.size || 0)}</div><div class="cap">占用空间</div></div>
      <div class="stat"><div class="num">${fmtBytes(stats.diskFree || 0)}</div><div class="cap">磁盘可用</div></div>
    </div>
    <div class="library-toolbar">
      <div class="tab-group" role="tablist" aria-label="库筛选">
        ${filters.map(([k, l]) => `<button class="tab" role="tab" aria-selected="${S.libraryFilter === k}" data-action="library-filter" data-filter="${k}">${l}${k === 'updates' && stats.updates ? ` · ${stats.updates}` : ''}</button>`).join('')}
      </div>
      <div class="hstack" style="margin-left:auto">
        <button class="btn text small" data-action="scan-external">${icon('search', 'sm')} 扫描系统应用</button>
        <button class="btn text small" data-action="refresh-library">${icon('refresh', 'sm')} 刷新</button>
      </div>
    </div>
    ${list.length ? `<div class="app-list">${list.map(appRow).join('')}</div>` : `<div class="empty"><div class="art">${icon('apps', 'lg')}</div><h3>这里是空的</h3><p>去“发现”找一个应用，或者导入已有的 Komi / Obtainium 备份。</p><button class="btn filled" data-action="go-discover">${icon('compass')} 去发现</button></div>`}
    <div class="spread" style="margin-top:20px">
      <span class="tiny muted">最后检查：${stats.lastCheck ? esc(new Date(Number(stats.lastCheck) * 1000).toLocaleString('zh-CN')) : '尚未检查'}</span>
      <button class="btn text small" data-action="open-apps-folder">${icon('folder', 'sm')} 安装目录</button>
    </div>`;
  main.scrollTop = scroll;
}

/* --------------------------------------------------------------- downloads */

function downloadRow(j) {
  const pct = Number(j.percent || (j.total ? Math.round((j.done / j.total) * 100) : 0));
  const active = ['queued', 'downloading', 'verifying'].includes(j.status);
  const done = j.status === 'done' || j.status === 'unverified';
  const failed = j.status === 'failed';
  const paused = j.status === 'paused';
  return `<div class="card dl-row">
    <div class="dl-top">
      <div class="avatar">${initials(j.displayName || j.assetName)}</div>
      <div class="grow">
        <div class="dl-name ellipsis">${esc(j.displayName || j.assetName)}</div>
        <div class="dl-sub ellipsis">${esc(j.assetName)} · ${esc(j.tag || '')}</div>
      </div>
      ${statusBadge(j.status)}
      ${done && !j.verified ? '<span class="badge warn" title="Release 没有提供可校验的摘要">未校验</span>' : ''}
      ${done && j.verified ? '<span class="badge success" title="SHA-256 校验通过">已校验</span>' : ''}
    </div>
    <div class="dl-status">
      <div class="progress-wrap">
        ${wavy(pct, paused ? 'paused' : failed ? 'error' : '')}
        <div class="progress-meta"><span>${fmtBytes(j.done)} / ${fmtBytes(j.total)}</span><span>${pct}%</span></div>
      </div>
      <div class="dl-actions">
        ${active ? `<button class="icon-btn" data-action="pause-download" data-id="${esc(j.id)}" data-tip="暂停" aria-label="暂停">${icon('pause')}</button>` : ''}
        ${paused || failed ? `<button class="icon-btn" data-action="resume-download" data-id="${esc(j.id)}" data-tip="继续" aria-label="继续">${icon('play')}</button>` : ''}
        ${!done ? `<button class="icon-btn danger" data-action="cancel-download" data-id="${esc(j.id)}" data-tip="取消并删除临时文件" aria-label="取消">${icon('close')}</button>` : ''}
        ${done ? `<button class="btn filled small" data-action="install-download" data-id="${esc(j.id)}">${icon('download', 'sm')} 安装</button>` : ''}
        ${done ? `<button class="icon-btn" data-action="reveal-download" data-id="${esc(j.id)}" data-tip="在资源管理器中显示" aria-label="显示文件">${icon('folder', 'sm')}</button>` : ''}
        ${done || failed ? `<button class="icon-btn danger" data-action="forget-download" data-id="${esc(j.id)}" data-tip="只移除记录，不删文件" aria-label="移除记录">${icon('trash', 'sm')}</button>` : ''}
      </div>
    </div>
    ${j.error ? `<div class="warning" style="margin-top:8px">${esc(j.error)}</div>` : ''}
    ${j.source ? `<div class="tiny muted" style="margin-top:6px">来源：${esc(j.source)}</div>` : ''}
  </div>`;
}

function renderDownloads() {
  const main = $('#main');
  const scroll = main.scrollTop;
  const active = (S.downloads || []).filter((j) => !['done', 'unverified', 'failed', 'canceled'].includes(j.status));
  const finished = (S.downloads || []).filter((j) => ['done', 'unverified', 'failed', 'canceled'].includes(j.status));
  main.innerHTML = `
    <div class="page-head">
      <div><h1>下载</h1><div class="sub">断点续传、镜像测速与自动优选、磁盘预检；校验失败的标记不会被隐藏。</div></div>
      <div class="page-actions">
        <button class="btn outlined small" data-action="choose-download-dir" data-tip="更改新任务的保存位置">${icon('folder', 'sm')} 更改目录</button>
        <button class="btn outlined small" data-action="open-downloads">${icon('folder', 'sm')} 下载目录</button>
        <button class="btn text small" data-action="clear-downloads">${icon('trash', 'sm')} 清理记录</button>
      </div>
    </div>
    <div class="section-title"><h2>进行中</h2><span class="badge outline">${active.length}</span></div>
    ${active.length ? `<div>${active.map(downloadRow).join('')}</div>` : `<div class="empty" style="padding:28px"><div class="art">${icon('download', 'lg')}</div><h3>没有进行中的下载</h3><p>在仓库详情里选择版本和资产后，任务会出现在这里。</p></div>`}
    <div class="section-title"><h2>已完成</h2><span class="badge outline">${finished.length}</span></div>
    ${finished.length ? `<div>${finished.map(downloadRow).join('')}</div>` : `<div class="note">下载完成后可以直接“安装”，安装器会请求 Windows 自己的权限确认。</div>`}
    <div class="spread" style="margin-top:16px">
      <span class="tiny muted">目录：${esc((S.auth && S.auth.downloadsDir) || '默认下载目录')} · 磁盘可用：${fmtBytes((S.stats && S.stats.diskFree) || 0)}</span>
      <span class="tiny muted">校验：只有 Release 提供 SHA-256 时才会标记“已校验”</span>
    </div>`;
  main.scrollTop = scroll;
}

/* ---------------------------------------------------------------- settings */

function settingRow(iconName, title, desc, control, cls = '') {
  return `<div class="setting-row inset-setting ${cls}">
    <span class="inset-icon">${icon(iconName, 'sm')}</span>
    <div class="text"><div class="title">${title}</div>${desc ? `<div class="desc">${desc}</div>` : ''}</div>
    <div class="control">${control}</div>
  </div>`;
}

function settingSwitch(key, title, desc, iconName = 'settings') {
  return settingRow(iconName, title, desc,
    `<label class="switch"><input type="checkbox" data-setting="${key}" ${S.settings[key] === 'true' ? 'checked' : ''}><span class="track"></span><span class="sr-only"></span></label>`);
}

function insetRow({ action = '', iconName = 'settings', title, desc = '', right = '', chevron = true, danger = false }) {
  const tag = action ? 'button' : 'div';
  return `<${tag} class="inset-row${danger ? ' danger' : ''}" ${action ? `data-action="${action}"` : ''}>
    <span class="inset-icon">${icon(iconName, 'sm')}</span>
    <span class="inset-main"><span class="inset-title">${title}</span>${desc ? `<span class="inset-desc">${desc}</span>` : ''}</span>
    ${right ? `<span class="inset-control">${right}</span>` : ''}
    ${chevron ? `<span class="inset-chevron">${icon('chevron', 'sm')}</span>` : ''}
  </${tag}>`;
}

function settingsGroup(title, rows, sub = '') {
  return `<section class="settings-group">
    <div class="settings-group-head"><h2>${title}</h2>${sub ? `<p>${sub}</p>` : ''}</div>
    <div class="inset-list">${rows}</div>
  </section>`;
}

function profileCount(value) {
  const n = Number(value);
  return Number.isFinite(n) ? String(n) : '—';
}

function settingsProfile(a) {
  const p = S.profile || {};
  const signed = !!a.signedIn;
  const name = p.name || a.login || '未登录';
  const handle = signed ? '@' + (p.login || a.login || '') : '登录后同步 GitHub 账号';
  const repo = signed ? profileCount(p.publicRepos) : '—';
  const followers = signed ? profileCount(p.followers) : '—';
  const following = signed ? profileCount(p.following) : '—';
  const ratePct = a.rateLimit && a.rateLimit.limit
    ? clamp((Number(a.rateLimit.remaining || 0) / Number(a.rateLimit.limit || 1)) * 100, 0, 100) : 0;
  const reset = a.rateLimit && a.rateLimit.reset
    ? new Date(a.rateLimit.reset).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }) : '—';
  return `<section class="profile-hero">
    <div class="profile-top">
      <button class="profile-avatar" data-action="${signed ? 'profile-open' : 'login'}" aria-label="${signed ? '查看个人资料' : '登录 GitHub'}">${avatarHtml({ owner: a.login || name, avatarUrl: p.avatarUrl || '' }, 'xl')}</button>
      <button class="profile-id" data-action="${signed ? 'profile-open' : 'login'}">
        <span class="profile-name">${esc(name)}</span>
        <span class="profile-handle">${esc(handle)}</span>
        <span class="profile-note">${signed ? (a.hasEnvToken ? '环境变量令牌 · 仅本次会话' : a.tokenPersisted ? '已安全保存在 Windows 凭据管理器' : '仅本次会话') : '匿名访问受 60 次/小时限制'}</span>
      </button>
      <div class="profile-actions">
        ${signed
          ? `<button class="btn outlined small" data-action="logout">退出登录</button>`
          : `<button class="btn filled small" data-action="login">登录 GitHub</button>`}
      </div>
    </div>
    <div class="profile-stats">
      <button class="profile-stat" ${signed ? 'data-action="profile-open"' : ''}><strong>${repo}</strong><span>仓库</span></button>
      <div class="profile-stat"><strong>${followers}</strong><span>关注者</span></div>
      <div class="profile-stat"><strong>${following}</strong><span>正在关注</span></div>
    </div>
    <div class="profile-quota">
      <div class="profile-quota-head"><span>API 配额</span><span>${Number((a.rateLimit && a.rateLimit.remaining) || 0)}/${Number((a.rateLimit && a.rateLimit.limit) || 0)} · 重置 ${esc(reset)}</span></div>
      <div class="rate-meter"><i style="width:${ratePct}%"></i></div>
    </div>
  </section>`;
}

function renderSettingsHub() {
  const a = S.auth || {};
  const signed = !!a.signedIn;
  const installs = (S.installs || []).length;
  const active = (S.downloads || []).filter((j) => ['queued', 'downloading', 'verifying', 'installing'].includes(j.status)).length;
  const finished = (S.downloads || []).filter((j) => ['done', 'unverified', 'failed', 'canceled'].includes(j.status)).length;
  const starsHref = a.login ? `https://github.com/${encodeURIComponent(a.login)}?tab=stars` : 'https://github.com/stars';
  return `<div class="settings-page">
    <div class="settings-header">
      <div><h1>个人资料</h1><div class="sub">账户、资料库、更新与应用设置。</div></div>
      <span class="badge ${signed ? 'primary' : 'outline'}">${signed ? '已登录 ' + esc(a.login || '') : '未登录'}</span>
    </div>
    ${settingsProfile(a)}
    ${settingsGroup('资料库',
      insetRow({ action: 'nav-library', iconName: 'apps', title: '应用库', desc: installs ? `${installs} 个应用已纳入管理，查看安装位置和更新状态` : '还没有安装应用，从发现页开始' }) +
      insetRow({ action: 'nav-downloads', iconName: 'download', title: '下载', desc: active ? `${active} 个任务进行中，${finished} 个已完成` : `${finished} 个已完成，暂无进行中的任务` }) +
      insetRow({ action: 'open-stars', iconName: 'star', title: '星标', desc: '在 GitHub 上查看你加星的仓库' }),
    '安装、下载与收藏')}
    ${settingsGroup('更新',
      insetRow({ action: 'release-notes', iconName: 'refresh', title: '更新内容', desc: `Komi Go ${esc(a.version || '')} · 最近版本的亮点` }) +
      insetRow({ action: 'announcements', iconName: 'inbox', title: '公告', desc: '资讯、调查和重要通知' }),
    '版本与公告')}
    ${settingsGroup('应用',
      insetRow({ action: 'settings-general', iconName: 'settings', title: '调整', desc: '外观、更新、连接、桌面、数据与诊断' }) +
      insetRow({ action: 'about-app', iconName: 'info', title: '关于', desc: '版本、社区、法律与数据目录' }),
    '个性化 Komi Go')}
    ${settingsGroup('账户',
      signed
        ? insetRow({ action: 'logout', iconName: 'power', title: '退出登录', desc: '从 Windows 凭据管理器删除令牌', danger: true, chevron: false })
        : insetRow({ action: 'login', iconName: 'key', title: '登录 GitHub', desc: '提升 API 配额，访问私有仓库与星标', chevron: false }),
    signed ? '' : '登录后同步你的 GitHub 资料')}
  </div>`;
}

function installedRepoKeys() {
  return new Set((S.installs || []).map((x) => `${String(x.owner || '').toLowerCase()}/${String(x.repo || '').toLowerCase()}`));
}

function profileFilteredRepos() {
  const all = S.profileRepos || [];
  const q = String(S.profileQuery || '').trim().toLowerCase();
  const installed = installedRepoKeys();
  return all.filter((r) => {
    if (S.profileFilter === 'windows' && !(r.platforms || []).includes('windows')) return false;
    if (S.profileFilter === 'installed' && !installed.has(`${String(r.owner || '').toLowerCase()}/${String(r.name || '').toLowerCase()}`)) return false;
    if (!q) return true;
    return `${r.name || ''} ${r.description || ''} ${r.language || ''} ${r.latestTag || ''}`.toLowerCase().includes(q);
  });
}

function profileRepoCard(r) {
  return `<article class="profile-repo-card">
    <button class="profile-repo-hit" data-action="profile-open-repo" data-owner="${esc(r.owner)}" data-name="${esc(r.name)}">
      <div class="profile-repo-head">
        <div class="profile-repo-title"><strong>${esc(r.name)}</strong><span>${esc(r.latestTag || r.latestName || 'Latest')} · 发布于 ${esc(timeAgo(r.publishedAt) || '—')}</span></div>
        ${r.private ? '<span class="badge outline">私有</span>' : ''}
      </div>
      <p class="profile-repo-desc">${esc(r.description || '这个仓库还没有填写简介。')}</p>
      <div class="profile-repo-meta">
        <span class="meta">${icon('star')} ${fmtCount(r.stars)}</span>
        ${r.language ? `<span class="meta">${icon('code')} ${esc(r.language)}</span>` : ''}
        ${platformChips(r, true)}
      </div>
    </button>
  </article>`;
}

function profileRepoListHtml() {
  if (S.profileReposLoading) {
    return `<div class="profile-repo-skeleton"></div><div class="profile-repo-skeleton"></div><div class="profile-repo-skeleton"></div>`;
  }
  if (S.profileError) {
    return `<div class="warning">${esc(S.profileError)}</div><div class="modal-actions" style="justify-content:flex-start"><button class="btn outlined small" data-action="profile-repos-retry">重新读取</button></div>`;
  }
  const list = profileFilteredRepos();
  if (!list.length) {
    return `<div class="empty" style="padding:36px 18px"><div class="art">${icon('inbox', 'lg')}</div><h3>没有匹配的仓库</h3><p>这里只显示带 Release 的仓库；换一个筛选或清空搜索试试。</p></div>`;
  }
  return list.map(profileRepoCard).join('');
}

function profileRepoCountText() {
  const total = Number((S.profile && S.profile.publicRepos) || 0) || (S.profileRepos || []).length;
  return `显示 ${profileFilteredRepos().length} 个中的 ${total} 个仓库`;
}

function renderProfileRepoList() {
  const list = $('#profileRepoList');
  if (list) list.innerHTML = profileRepoListHtml();
  const count = $('#profileRepoCount');
  if (count) count.textContent = profileRepoCountText();
}

async function ensureProfileRepos(force = false) {
  if (S.profileReposLoading) return;
  if (S.profileReposLoaded && !force) return;
  S.profileReposLoading = true;
  S.profileError = '';
  S.profileReposLoaded = false;
  if (S.route === 'settings' && S.settingsScreen === 'profile') renderSettings();
  try {
    S.profileRepos = (await call('ProfileRepos')) || [];
    S.profileReposLoaded = true;
  } catch (e) {
    S.profileError = e && e.message ? e.message : String(e);
  } finally {
    S.profileReposLoading = false;
    if (S.route === 'settings' && S.settingsScreen === 'profile') renderSettings();
  }
}

async function ensureContributions(force = false) {
  if (S.contributionsLoading) return;
  if (S.contributionsLoaded && !force) return;
  S.contributionsLoading = true;
  S.contributionsError = '';
  S.contributionsLoaded = false;
  if (S.route === 'settings' && S.settingsScreen === 'profile') renderSettings();
  try {
    S.contributions = await call('ContributionCalendar');
    S.contributionsLoaded = true;
  } catch (e) {
    S.contributionsError = e && e.message ? e.message : String(e);
  } finally {
    S.contributionsLoading = false;
    if (S.route === 'settings' && S.settingsScreen === 'profile') renderSettings();
  }
}

function contributionCard() {
  if (!(S.auth && S.auth.signedIn)) return '';
  const cal = S.contributions;
  let body = '';
  if (S.contributionsError) {
    body = `<div class="contribution-empty"><span>${esc(S.contributionsError)}</span><button class="btn text small" data-action="contributions-retry">重试</button></div>`;
  } else if (S.contributionsLoading || !cal) {
    body = `<div class="contribution-loading" aria-label="正在读取贡献活动"></div>`;
  } else {
    const weeks = (cal.weeks || []).map((week) => `<div class="contribution-week">${
      (week.days || []).map((day) => `<span class="contribution-cell level-${clamp(Number(day.level) || 0, 0, 4)}" title="${esc(day.date)} · ${Number(day.count) || 0} 次贡献" aria-label="${esc(day.date)} ${Number(day.count) || 0} 次贡献"></span>`).join('')
    }</div>`).join('');
    body = `<div class="contribution-grid">${weeks}</div>`;
  }
  return `<section class="contribution-card">
    <div class="contribution-head"><h2>贡献活动</h2><span>${cal ? `今年 ${Number(cal.total) || 0} 次` : '今年 — 次'}</span></div>
    ${body}
    <div class="contribution-legend"><span>较少</span><div class="contribution-scale">${[0, 1, 2, 3, 4].map((l) => `<i class="level-${l}"></i>`).join('')}</div><span>较多</span></div>
  </section>`;
}

function renderSettingsProfile() {
  const a = S.auth || {};
  const filters = [['released', '有发布版'], ['windows', '可在此安装'], ['installed', '已安装']];
  return `<div class="settings-page profile-page">
    <div class="settings-header with-back">
      <button class="icon-btn" data-action="settings-hub" aria-label="返回设置">${icon('back')}</button>
      <div><h1>${esc(a.login || '个人资料')}</h1><div class="sub">仓库、贡献与发布版本。</div></div>
    </div>
    ${settingsProfile(a)}
    ${contributionCard()}
    <div class="profile-search-row">
      <div class="profile-search">${icon('search')}<input id="profileSearch" placeholder="搜索仓库…" value="${esc(S.profileQuery)}" aria-label="搜索仓库"></div>
    </div>
    <div class="profile-filter-row">
      <div class="tab-group">${filters.map(([k, l]) => `<button class="tab" aria-selected="${S.profileFilter === k}" data-action="profile-filter" data-filter="${k}">${l}</button>`).join('')}</div>
    </div>
    <div class="profile-repo-count" id="profileRepoCount">${profileRepoCountText()}</div>
    <div class="profile-repo-list" id="profileRepoList">${profileRepoListHtml()}</div>
  </div>`;
}

const SETTINGS_SECTIONS = [
  { id: 'appearance', title: '外观与显示', desc: '主题、减少动效与界面观感', iconName: 'settings' },
  { id: 'updates', title: '安装与更新', desc: '自动检查、预发布、资产过滤与应用自身升级', iconName: 'refresh' },
  { id: 'network', title: '连接', desc: '代理、镜像与下载加速', iconName: 'external' },
  { id: 'desktop', title: '桌面集成', desc: '托盘、开机启动与 komi:// 协议', iconName: 'power' },
  { id: 'data', title: '隐私与数据', desc: '缓存、库备份与本地应用扫描', iconName: 'shield' },
  { id: 'about', title: '关于与诊断', desc: '版本、日志与数据目录', iconName: 'info' },
];

function settingsSection(id) {
  return SETTINGS_SECTIONS.find((s) => s.id === id) || SETTINGS_SECTIONS[0];
}

function settingsSectionSummary(id) {
  const a = S.auth || {};
  const theme = S.settings.theme || 'system';
  const themeLabel = { system: '跟随系统', light: '浅色', dark: '深色' }[theme] || '跟随系统';
  if (id === 'appearance') return `主题：${themeLabel} · 主色：${S.settings['ui.accent'] ? '自定义' : '默认'} · 减少动效：${S.settings['ui.reduce_motion'] === 'true' ? '开启' : '关闭'}`;
  if (id === 'updates') return `自动检查：${S.settings['update.auto'] === 'true' ? '开启' : '关闭'} · 间隔 ${esc(S.settings['update.interval'] || '12')} 小时`;
  if (id === 'network') {
    const mode = S.settings['network.mirrorSelected'] || 'auto';
    const mirror = mode === 'auto' ? '自动' : mode === 'direct' ? '直连' : (mirrorConfig().find((m) => m.template === mode)?.name || '自定义');
    return `代理：${S.settings['network.proxy'] ? '已配置' : '未设置'} · 镜像：${mirror}`;
  }
  if (id === 'desktop') return `关闭到托盘：${S.settings['ui.close_to_tray'] === 'true' ? '开启' : '关闭'} · 开机启动：${a.autostart ? '开启' : '关闭'}`;
  if (id === 'data') return `缓存 ${Number(S.cache.entries || 0)} 条 · ${fmtBytes(S.cache.bytes || 0)}`;
  return `Komi Go ${esc(a.version || '')} · 日志与数据目录`;
}

function renderSettingsGeneral() {
  return `<div class="settings-page">
    <div class="settings-header with-back">
      <button class="icon-btn" data-action="settings-hub" aria-label="返回设置">${icon('back')}</button>
      <div><h1>调整</h1><div class="sub">先选择板块，进入后再修改具体设置。</div></div>
    </div>
    <section class="settings-group">
      <div class="settings-group-head"><h2>设置分类</h2><p>外观、更新、连接、桌面、数据与诊断</p></div>
      <div class="settings-category-grid">
        ${SETTINGS_SECTIONS.map((s) => `<button class="settings-category" data-action="settings-section" data-section="${s.id}">
          <span class="settings-category-icon">${icon(s.iconName)}</span>
          <span class="settings-category-text"><strong>${s.title}</strong><small>${settingsSectionSummary(s.id)}</small></span>
          <span class="inset-chevron">${icon('back')}</span>
        </button>`).join('')}
      </div>
    </section>
  </div>`;
}

function renderSettingsSection(id) {
  const section = settingsSection(id);
  return `<div class="settings-page">
    <div class="settings-header with-back">
      <button class="icon-btn" data-action="settings-general" aria-label="返回调整">${icon('back')}</button>
      <div><h1>${section.title}</h1><div class="sub">${section.desc}</div></div>
    </div>
    ${settingsSectionContent(section.id)}
  </div>`;
}

function settingsSectionContent(id) {
  const a = S.auth || {};
  const theme = S.settings.theme || 'system';
  const accent = String(S.settings['ui.accent'] || '');
  if (id === 'appearance') {
    const accentPresets = ['#006e2c', '#0061a4', '#6750a4', '#b3261e', '#825500', '#006a6a'];
    return settingsGroup('外观与显示',
      settingRow('settings', '主题', '跟随系统、浅色或深色；切换立即生效。',
        `<div class="tab-group">${['system', 'light', 'dark'].map((t) => `<button class="tab" aria-selected="${theme === t}" data-action="theme" data-theme="${t}">${{ system: '跟随系统', light: '浅色', dark: '深色' }[t]}</button>`).join('')}</div>`) +
      settingRow('palette', '主题色', accent ? `当前 ${accent.toUpperCase()}，界面背景、按钮与选中态均已同步。` : '自定义后，界面背景、按钮、选中态与进度条都会同步换色。',
        `<div class="accent-control">
          ${accentPresets.map((c) => `<button class="accent-dot${accent.toLowerCase() === c ? ' selected' : ''}" style="--dot:${c}" data-action="accent-color" data-accent="${c}" title="${c}" aria-label="使用主题色 ${c}"></button>`).join('')}
          <label class="accent-custom" title="自定义颜色">
            <input type="color" id="accentColor" value="${esc(accent || '#006e2c')}" aria-label="自定义主题色">
            <span id="accentValue">${accent ? esc(accent.toUpperCase()) : '自定义'}</span>
          </label>
          ${accent ? '<button class="btn text small accent-reset" data-action="accent-reset">恢复默认</button>' : ''}
        </div>`) +
      settingSwitch('ui.reduce_motion', '减少动效', '关闭按压形变、波浪进度与过渡动画。', 'play'),
    '决定 Komi Go 的观感与动效');
  }
  if (id === 'updates') {
    return settingsGroup('安装与更新',
      settingSwitch('update.auto', '自动检查更新', '应用启动后按间隔在后台检查，不自动安装。', 'refresh') +
      settingRow('refresh', '检查间隔', '1–168 小时。默认 12 小时。',
        `<input class="input" type="number" min="1" max="168" style="width:96px" data-setting="update.interval" value="${esc(S.settings['update.interval'] || '12')}"><span class="muted small">小时</span>`) +
      settingSwitch('update.prerelease', '接受预发布', '默认跳过 prerelease 标签与草稿。', 'tag') +
      settingSwitch('update.fallback_older', '回退到旧版本', '当最新版本没有本机资产时，继续向前找可用版本。', 'skip') +
      settingRow('filter', '资产过滤', '正则，可选。留空表示接受所有匹配资产；最多 200 字符。',
        `<input class="input" data-setting="update.filter" value="${esc(S.settings['update.filter'] || '')}" placeholder="例如 portable|setup">`) +
      settingRow('code', '自更新仓库', '填写发布 Komi Go 安装包的 owner/repo；用于应用自身升级。',
        `<input class="input" data-setting="self_update.repo" value="${esc(S.settings['self_update.repo'] || '')}" placeholder="owner/repo">`) +
      settingRow('refresh', '应用更新', S.selfInfo && S.selfInfo.available ? `发现新版本 ${esc(S.selfInfo.latest || '')}` : '检查并下载 Komi Go 的新版本。',
        `<button class="btn tonal small" data-action="check-self-update">${icon('refresh', 'sm')} 检查</button><button class="btn outlined small" data-action="download-self-update" ${S.selfInfo && S.selfInfo.available ? '' : 'disabled'}>安装</button>`),
    'Release 检查与应用自身升级');
  }
  if (id === 'network') {
    return settingsGroup('连接',
      settingRow('external', '代理', '支持 http/https 代理；留空跟随系统。',
        `<input class="input" id="proxyInput" value="${esc(S.settings['network.proxy'] || '')}" placeholder="http://127.0.0.1:7890">`) +
      settingRow('external', '代理操作', '保存后立即应用到后续请求。',
        `<button class="btn outlined small" data-action="save-proxy">保存代理</button><button class="btn text small" data-action="ping">测试连接</button>`) +
      settingRow('download', '镜像模式', '自动会在下载前测速并优先延迟最低的镜像。', mirrorModeControl()) +
      mirrorPanel(),
    '下载加速与连接方式');
  }
  if (id === 'desktop') {
    return settingsGroup('桌面集成',
      settingSwitch('ui.close_to_tray', '关闭到托盘', '点击关闭按钮时隐藏到托盘，而不是退出。', 'power') +
      settingRow('power', '开机启动', '写入 HKCU Run，启动时隐藏到托盘。',
        `<label class="switch"><input type="checkbox" id="autostartSwitch" ${a.autostart ? 'checked' : ''}><span class="track"></span></label>`) +
      settingRow('external', 'komi:// 协议', '双击链接或从浏览器直接打开仓库页。',
        `<span class="badge ${a.urlScheme ? 'success' : 'outline'}">${a.urlScheme ? '已注册' : '未注册'}</span><button class="btn outlined small" data-action="register-scheme">注册</button>`),
    '托盘、启动项与系统链接');
  }
  if (id === 'data') {
    return settingsGroup('隐私与数据',
      settingRow('folder', '下载目录', esc(a.downloadsDir || '默认下载目录'),
        `<button class="btn outlined small" data-action="choose-download-dir">更改</button><button class="icon-btn" data-action="open-downloads" data-tip="打开下载目录" aria-label="打开下载目录">${icon('folder')}</button>`) +
      settingRow('inbox', 'HTTP 缓存', `${Number(S.cache.entries || 0)} 条 · ${fmtBytes(S.cache.bytes || 0)}；数据库 ${fmtBytes(S.cache.dbBytes || 0)}`,
        `<button class="btn outlined small" data-action="clear-cache">清空缓存</button>`) +
      settingRow('copy', '库备份', '导出 Komi 备份或 Obtainium 兼容列表；导入只建立跟踪，不自动安装。',
        `<button class="btn outlined small" data-action="export-library">导出</button><button class="btn outlined small" data-action="export-obtainium">Obtainium</button><button class="btn tonal small" data-action="import-library">导入</button>`) +
      settingRow('apps', '系统应用扫描', '读取“应用和功能”，尝试反查 GitHub 来源仓库。',
        `<button class="btn outlined small" data-action="scan-external">扫描</button>`),
    '缓存、备份与本地应用');
  }
  return settingsGroup('关于与诊断',
    settingRow('info', `Komi Go ${esc(a.version || '')}`, 'Go + Wails v2 + SQLite + WebView2 · Material 3 Expressive',
      `<button class="btn text small" data-action="copy-diagnostics">复制诊断</button><button class="btn text small" data-action="open-logs">日志</button>`) +
    settingRow('folder', '数据目录', `<span class="mono" style="font-size:11px">${esc(a.dataDir || '')}</span>`,
      `<button class="icon-btn" data-action="open-data" data-tip="打开数据目录" aria-label="打开数据目录">${icon('folder')}</button>`),
  '版本与本地数据');
}

function mirrorConfig() {
  try {
    const list = JSON.parse(S.settings['network.mirrors'] || '[]');
    return Array.isArray(list) ? list.filter((m) => m && m.template) : [];
  } catch (_) { return []; }
}

function mirrorRows() {
  const list = mirrorConfig();
  if (!list.length) {
    return '<div class="mirror-empty">还没有配置镜像。展开下方 JSON 添加，或点“测速”查看内置镜像。</div>';
  }
  const results = S.mirrorResults || [];
  const measured = results.filter((r) => r && r.ok);
  const fastest = measured.length ? Math.min(...measured.map((r) => r.latencyMs)) : -1;
  return list.map((m) => {
    const r = results.find((x) => x && x.template === m.template);
    const host = String(m.template).replace(/^https:\/\//, '').replace(/\/?\{url\}.*$/, '');
    let latency = '<span class="badge outline">未测速</span>';
    if (S.mirrorTesting && !r) latency = '<span class="badge outline">测速中…</span>';
    else if (r && r.ok) {
      latency = `<span class="badge ${r.latencyMs === fastest ? 'success' : 'outline'}">${r.latencyMs} ms</span>${r.latencyMs === fastest ? '<span class="badge success">最快</span>' : ''}`;
    } else if (r) {
      latency = `<span class="badge warn" title="${esc(r.error || '')}">不可用</span>`;
    }
    return `<div class="mirror-row"><div class="mirror-id"><div class="name">${esc(m.name || host)}</div><div class="host">${esc(host)}</div></div><div class="mirror-lat">${latency}</div></div>`;
  }).join('');
}

function mirrorModeControl() {
  return `<select class="input" id="mirrorMode" data-action="mirror-mode">
    <option value="auto" ${(S.settings['network.mirrorSelected'] || 'auto') === 'auto' ? 'selected' : ''}>自动 · 延迟最低</option>
    <option value="direct" ${S.settings['network.mirrorSelected'] === 'direct' ? 'selected' : ''}>不使用镜像 · 直连</option>
    ${mirrorConfig().map((m) => `<option value="${esc(m.template)}" ${S.settings['network.mirrorSelected'] === m.template ? 'selected' : ''}>${esc(m.name || m.template)}${m.api ? ' · 支持 API' : ''}</option>`).join('')}
  </select>`;
}

function mirrorPanel() {
  return `<div class="settings-subpanel">
    <div class="mirror-panel">
      <div class="mirror-head">
        <div><span class="title">镜像站</span> <span class="badge outline">${mirrorConfig().length}</span></div>
        <button class="btn tonal small" data-action="test-mirrors" ${S.mirrorTesting ? 'disabled' : ''}>${icon('refresh', 'sm')} ${S.mirrorTesting ? '测速中…' : '测速'}</button>
      </div>
      <div class="mirror-list">${mirrorRows()}</div>
      <p class="tiny muted">内置 NxNow、MonkeyRay、GeekerTao、GHProxy.net、GHFast、LLKK；GitHub 直连始终作为回退源。镜像只代理资产下载，API 请求仍直连上游聚合服务。</p>
      <details class="mirror-editor">
        <summary>编辑镜像配置（JSON）</summary>
        <textarea class="textarea" id="mirrorsInput" placeholder='[{"name":"镜像","template":"https://mirror.example/{url}"}]'>${esc(S.settings['network.mirrors'] || '')}</textarea>
        <div class="modal-actions" style="justify-content:flex-start;margin-top:8px"><button class="btn outlined small" data-action="save-mirrors">保存镜像</button></div>
      </details>
    </div>
  </div>`;
}

function renderSettings() {
  const main = $('#main');
  const scroll = main.scrollTop;
  main.innerHTML = S.settingsScreen === 'general'
    ? renderSettingsGeneral()
    : S.settingsScreen === 'section'
      ? renderSettingsSection(S.settingsSection)
      : S.settingsScreen === 'profile'
        ? renderSettingsProfile()
        : renderSettingsHub();
  main.scrollTop = scroll;
  const search = $('#profileSearch');
  if (search) search.addEventListener('input', (e) => {
    S.profileQuery = e.target.value;
    renderProfileRepoList();
  });
  const auto = $('#autostartSwitch');
  if (auto) auto.addEventListener('change', async () => {
    try { await call('SetAutostart', auto.checked); S.auth.autostart = auto.checked; toast(auto.checked ? '已开启开机启动' : '已关闭开机启动', 'success'); }
    catch (e) { notifyError(e); auto.checked = !auto.checked; }
  });
  const accent = $('#accentColor');
  if (accent) {
    const preview = () => {
      S.settings['ui.accent'] = accent.value;
      S.accent = accent.value;
      applyAccent();
      const label = $('#accentValue');
      if (label) label.textContent = accent.value.toUpperCase();
    };
    accent.addEventListener('input', preview);
    accent.addEventListener('change', async () => {
      try {
        await call('SetSetting', 'ui.accent', accent.value);
        S.settings['ui.accent'] = accent.value;
        S.accent = accent.value;
        applyAccent();
        renderSettings();
        toast('主题色已更新', 'success');
      } catch (e) { notifyError(e); }
    });
  }
}

async function loadSettingsPage() {
  await Promise.all([loadSettings(), loadAuth(), loadCache()]);
  await loadProfile();
  renderSettings();
}

async function loadProfile() {
  if (!S.auth || !S.auth.signedIn) { S.profile = {}; return; }
  try { S.profile = (await call('Profile')) || {}; } catch (_) { S.profile = {}; }
}

/* ------------------------------------------------------------------ modals */

function openModal(html, size = '') {
  lastFocus = document.activeElement;
  modalOpen = true;
  $('#modalRoot').innerHTML = `<div class="scrim" data-action="scrim" role="presentation">
    <div class="modal ${size}" role="dialog" aria-modal="true">${html}</div>
  </div>`;
  const modal = $('.modal');
  if (modal) {
    const first = modal.querySelector('input, textarea, button.btn');
    if (first) setTimeout(() => first.focus(), 30);
  }
}

function closeModal() {
  modalOpen = false;
  $('#modalRoot').innerHTML = '';
  if (deviceTimer) { clearTimeout(deviceTimer); deviceTimer = null; }
  deviceSession = null;
  if (lastFocus && lastFocus.focus) { try { lastFocus.focus(); } catch (_) { /* ignore */ } }
}

function confirmDialog({ title, body, confirm = '确定', danger = false, onConfirm }) {
  openModal(`<div class="modal-head"><div><div class="modal-title">${esc(title)}</div><div class="modal-sub">${esc(body || '')}</div></div>
    <button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
    <div class="warning">这个操作会立即执行，请确认目标正确。</div>
    <div class="modal-actions"><button class="btn text" data-action="close-modal">取消</button>
    <button class="btn ${danger ? 'danger' : 'filled'}" id="confirmBtn">${esc(confirm)}</button></div>`, 'narrow');
  const btn = $('#confirmBtn');
  if (btn) btn.onclick = async () => {
    btn.disabled = true;
    try { await onConfirm(); closeModal(); }
    catch (e) { notifyError(e); btn.disabled = false; }
  };
}

/* ------------------------------------------------------------------ actions */

async function checkUpdates(force = true) {
  busyOn();
  try {
    const apps = await call('CheckUpdates', force);
    S.installs = apps || [];
    S.stats = (await call('LibraryStats')) || {};
    updateRail();
    const count = S.installs.filter((x) => x.updateAvailable).length;
    toast(count ? `发现 ${count} 个可用更新` : '所有应用都是最新版本', count ? 'success' : '');
    if (S.route === 'library') render();
    if (S.route === 'settings') renderSettings();
  } catch (e) { notifyError(e); }
  finally { busyOff(); }
}

async function startDownload(index, assetIndex) {
  const view = S.view;
  if (!view) return;
  const rel = view.releases[index];
  const asset = rel && rel.assets && rel.assets[assetIndex];
  if (!rel || !asset) return;
  if (!asset.digest) toast('这个资产没有 SHA-256 摘要，下载后会标记为“未校验”');
  const req = {
    repoKey: repoKeyOf(view),
    tag: rel.tagName,
    displayName: `${view.name} ${rel.tagName}`,
    assetId: asset.id,
    assetName: asset.name,
    downloadUrl: asset.downloadUrl,
    size: asset.size,
    digest: asset.digest || '',
    publishedAt: Math.floor(new Date(rel.publishedAt).getTime() / 1000) || 0,
  };
  busyOn();
  try {
    await call('StartDownload', req);
    await loadDownloads();
    toast('已加入下载队列', 'success');
    navigate('downloads');
  } catch (e) { notifyError(e); }
  finally { busyOff(); }
}

async function installDownload(id) {
  busyOn();
  try {
    const res = await call('InstallDownloaded', id);
    await loadLibrary();
    if (res && res.pending) {
      toast(res.message || '安装器已启动，等待系统确认', 'success');
      openModal(`<div class="modal-head"><div><div class="modal-title">等待系统确认</div><div class="modal-sub">${esc(res.message || '')}</div></div><button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
        <div class="note">Windows 安装器可能还在运行。Komi 会在后台轮询“应用和功能”，登记完成后自动写入安装目录、卸载命令与大小。</div>
        <div class="modal-actions"><button class="btn filled" data-action="close-modal">知道了</button></div>`, 'narrow');
    } else {
      const msg = res && res.message ? res.message : '安装完成';
      toast(res && res.unverified ? `${msg}（下载时没有可校验的摘要）` : msg, 'success');
    }
    render();
  } catch (e) { notifyError(e); }
  finally { busyOff(); }
}

async function appMenu(id) {
  const app = (S.installs || []).find((x) => x.id === id);
  if (!app) return;
  const installed = app.libraryStatus !== 'uninstalled' && app.libraryStatus !== 'tracked';
  const installDanger = installed;
  const rows = [];
  if (installed) rows.push(['open-app', '打开应用', 'play']);
  if (app.installDir) rows.push(['open-install-dir', '打开安装目录', 'folder']);
  if (app.libraryStatus !== 'uninstalled') rows.push(['sync-app', '同步安装信息', 'refresh']);
  rows.push(['toggle-pin', app.pinned ? '取消固定版本' : '固定版本（不提示更新）', 'pin']);
  if (app.updateAvailable) rows.push(['skip-version', `跳过 ${app.latestReleaseTag || '当前版本'}`, 'skip']);
  rows.push(['app-prefs', '资产选择偏好', 'filter']);
  rows.push(['open-app-repo', '打开仓库页', 'external']);
  if (app.libraryStatus === 'uninstalled') rows.push(['restore-app', '恢复跟踪', 'refresh']);
  if (installDanger) rows.push(['uninstall-app', '卸载应用', 'trash']);
  if (!installed || app.libraryStatus === 'uninstalled' || app.libraryStatus === 'tracked') rows.push(['remove-app', '移除记录', 'trash']);

  openModal(`<div class="modal-head"><div class="hstack">${avatarHtml({ owner: app.displayName || app.repo })}<div><div class="modal-title">${esc(app.displayName || app.repo)}</div><div class="modal-sub">${esc(app.owner)}/${esc(app.repo)} · ${esc(app.installedVersion || app.libraryStatus || '')}</div></div></div>
    <button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
    <div class="modal-sub" style="margin:8px 0 10px">${esc(app.installDir || '尚未记录安装目录')}${app.installSize ? ' · ' + fmtBytes(app.installSize) : ''}</div>
    <div class="modal-list">
      ${rows.map(([action, label, ico]) => `<button class="modal-row" data-action="${action}" data-id="${esc(id)}" style="text-align:left"><span class="grow">${label}</span>${icon(ico, 'sm')}</button>`).join('')}
    </div>
    <div class="modal-actions"><button class="btn text" data-action="close-modal">关闭</button></div>`);
}

async function appPrefs(id) {
  const app = (S.installs || []).find((x) => x.id === id);
  if (!app) return;
  openModal(`<div class="modal-head"><div><div class="modal-title">资产偏好</div><div class="modal-sub">${esc(app.displayName)} · 下一次更新沿用同一资产家族</div></div><button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
    <div class="vstack" style="gap:14px;margin-top:12px">
      <div class="field"><label>资产过滤正则</label><input class="input" id="prefFilter" value="${esc(app.assetFilterRegex || '')}" placeholder="留空表示不额外过滤"></div>
      <label class="switch"><input type="checkbox" id="prefPre" ${app.includePrereleases ? 'checked' : ''}><span class="track"></span><span class="label">接受预发布版本</span></label>
      <label class="switch"><input type="checkbox" id="prefFallback" ${app.fallbackToOlderReleases ? 'checked' : ''}><span class="track"></span><span class="label">最新版没有资产时回退旧版</span></label>
      <div class="note">已学习资产家族：<span class="mono">${esc(app.assetGlob || '未学习')}</span><br>首选变体：${esc(app.preferredVariant || '自动')} · 上次选择 ${Number(app.pickedIndex || 0) + 1}/${Number(app.siblingCount || 1)}</div>
    </div>
    <div class="modal-actions"><button class="btn text" data-action="close-modal">取消</button><button class="btn filled" id="prefSave">保存</button></div>`, 'narrow');
  const btn = $('#prefSave');
  if (btn) btn.onclick = async () => {
    busyOn();
    try {
      await call('SetAppPreferences', id, $('#prefFilter').value || '', $('#prefPre').checked, $('#prefFallback').checked);
      toast('偏好已保存', 'success'); closeModal(); await loadLibrary(); render();
    } catch (e) { notifyError(e); }
    finally { busyOff(); }
  };
}

async function loginModal() {
  openModal(`<div class="modal-head"><div><div class="modal-title">登录 GitHub</div><div class="modal-sub">使用设备码授权，令牌保存在 Windows 凭据管理器。</div></div><button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
    <div class="note" style="margin-bottom:12px">已内置 Komi Go 的 OAuth App，无需填写 client_id。点击开始后，把生成的 8 位代码填到 GitHub 授权页即可登录。</div>
    <div class="modal-actions"><button class="btn filled" id="deviceStart">生成设备码</button></div>
    <div id="deviceArea"></div>`);
  const start = $('#deviceStart');
  if (start) start.onclick = startDeviceFlow;
  startDeviceFlow();
}

async function startDeviceFlow() {
  busyOn();
  try {
    const flow = await call('StartDeviceFlow');
    const code = flow.device_code || flow.deviceCode;
    const userCode = flow.user_code || flow.userCode;
    const verifyUrl = flow.verification_uri || flow.verificationURI || 'https://github.com/login/device';
    const bareCode = String(userCode || '').replace(/[^A-Za-z0-9]/g, '');
    const openUrl = verifyUrl + (verifyUrl.includes('?') ? '&' : '?') + 'user_code=' + encodeURIComponent(bareCode);
    const expires = Number(flow.expires_in || flow.expiresIn || 900);
    deviceSession = { code, interval: Math.max(5, Number(flow.interval || 5)) * 1000 };
    const area = $('#deviceArea');
    if (area) {
      area.innerHTML = `<div class="divider"></div>
        <div class="card flat" style="text-align:center">
          <div class="tiny muted">在 GitHub 输入以下代码</div>
          <div style="font-size:30px;font-weight:700;letter-spacing:4px;font-family:var(--mono);margin:8px 0">${esc(userCode)}</div>
          <div class="hstack" style="justify-content:center;flex-wrap:wrap">
            <button class="btn tonal small" id="copyCode">复制代码</button>
            <button class="btn outlined small" data-action="open-device-url" data-url="${esc(openUrl)}">打开授权页</button>
          </div>
          <div class="tiny muted" style="margin-top:10px">有效期 ${Math.max(1, Math.round(expires / 60))} 分钟，窗口关闭即取消轮询。</div>
        </div>`;
      const copy = $('#copyCode');
      if (copy) copy.onclick = () => copyText(bareCode, '授权码已复制');
    }
    pollDevice();
  } catch (e) { notifyError(e); }
  finally { busyOff(); }
}

async function pollDevice() {
  if (!deviceSession || !modalOpen) return;
  try {
    const res = await call('PollDeviceFlow', deviceSession.code);
    if (res && res.pending) {
      if (res.slowDown) deviceSession.interval += 5000;
      deviceSession.errors = 0;
      deviceTimer = setTimeout(pollDevice, deviceSession.interval);
      return;
    }
    toast(`设备码登录成功：${res.login || ''}`, 'success');
    closeModal();
    await loadAuth(); await loadProfile(); await loadSettings(); if (S.route === 'settings') renderSettings();
  } catch (e) {
    const msg = String((e && (e.message || e.error || e)) || '').toLowerCase();
    const terminal = ['access_denied', 'expired_token', 'expired_device_code', 'bad_verification_code', 'incorrect_device_code', 'device_flow_disabled']
      .some((k) => msg.includes(k));
    if (terminal) {
      notifyError(new Error('授权已失效，请重新生成设备码：' + (e.message || e)));
      return;
    }
    deviceSession.errors = (deviceSession.errors || 0) + 1;
    if (deviceSession.errors <= 5) {
      const backoff = Math.min(Math.max(deviceSession.interval, 5000) * deviceSession.errors, 30000);
      deviceTimer = setTimeout(pollDevice, backoff);
      return;
    }
    notifyError(e);
  }
}

async function scanExternalModal() {
  busyOn();
  try {
    const apps = await call('ScanExternalApps');
    const list = apps || [];
    openModal(`<div class="modal-head"><div><div class="modal-title">系统应用扫描</div><div class="modal-sub">从 HKLM/HKCU 卸载表读取，排除系统组件与更新包。</div></div><button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
      <div class="modal-sub" style="margin:6px 0">共 ${list.length} 个未跟踪应用，反查仓库是尽力而为。</div>
      <div class="modal-list" id="externalList">
        ${list.length ? list.map((x, i) => `<div class="modal-row" data-external-row="${i}">
          <div class="avatar">${initials(x.name)}</div>
          <div class="grow"><div class="ellipsis">${esc(x.name)}</div><div class="tiny muted">${esc(x.publisher || '未知发布者')} · ${esc(x.version || '—')} · ${fmtBytes(x.size)}</div></div>
          <button class="btn outlined tiny" data-action="match-external" data-index="${i}">查找来源</button>
        </div>`).join('') : '<div class="note">没有可扫描的应用，或读取注册表失败。</div>'}
      </div>
      <div class="modal-actions"><button class="btn text" data-action="close-modal">关闭</button></div>`, 'wide');
    window.__externalApps = list;
  } catch (e) { notifyError(e); }
  finally { busyOff(); }
}

async function matchExternal(index) {
  const x = (window.__externalApps || [])[index];
  if (!x) return;
  const row = document.querySelector(`[data-external-row="${index}"]`);
  if (row) row.innerHTML = `<div class="grow"><div class="ellipsis">${esc(x.name)}</div><div class="tiny muted">正在搜索 GitHub…</div></div><span class="spinner"></span>`;
  try {
    const candidates = (await call('MatchExternalApp', x.name)) || [];
    if (!row) return;
    if (!candidates.length) {
      row.innerHTML = `<div class="grow"><div>${esc(x.name)}</div><div class="tiny muted">没有找到可信候选</div></div><button class="btn text tiny" data-action="close-modal">关闭</button>`;
      return;
    }
    row.innerHTML = `<div class="grow"><div class="ellipsis">${esc(x.name)}</div><div class="tiny muted">候选仓库</div>
      ${candidates.map((c, i) => `<div class="spread" style="margin-top:6px;gap:8px">
        <div class="grow"><span class="mono tiny">${esc(c.owner)}/${esc(c.name)}</span> <span class="badge ${c.platformReady ? 'primary' : 'outline'}">${c.platformReady ? '可安装' : esc(c.platformNote || '未知')}</span></div>
        <button class="btn tonal tiny" data-action="track-external" data-owner="${esc(c.owner)}" data-repo="${esc(c.name)}" data-name="${esc(x.name)}">跟踪</button>
      </div>`).join('')}</div>`;
  } catch (e) {
    if (row) row.innerHTML = `<div class="grow"><div>${esc(x.name)}</div><div class="warning">${esc(e.message)}</div></div>`;
  }
}

async function importLibrary() {
  busyOn();
  try {
    const report = await call('ImportLibraryFile');
    openImportPreview(report, false);
  } catch (e) {
    if (!/取消/.test(e.message)) notifyError(e);
  } finally { busyOff(); }
}

function openImportPreview(report, applied) {
  const rows = report.rows || [];
  openModal(`<div class="modal-head"><div><div class="modal-title">${applied ? '导入完成' : '导入预览'}</div><div class="modal-sub">共 ${report.total} · 新增 ${report.added} · 已存在 ${report.skipped} · 无效 ${report.invalid}</div></div>
    <button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
    <div class="modal-list">${rows.map((r) => `<div class="modal-row"><div class="grow"><div class="ellipsis">${esc(r.name || r.key)}</div><div class="tiny muted mono">${esc(r.key)}</div></div><span class="badge ${r.status === 'new' ? 'primary' : r.status === 'exists' ? 'outline' : 'error'}">${r.status === 'new' ? '新增' : r.status === 'exists' ? '已存在' : '无效'}</span></div>`).join('') || '<div class="note">没有可导入的条目。</div>'}</div>
    <div class="modal-actions"><button class="btn text" data-action="close-modal">关闭</button>
    ${applied ? '' : `<button class="btn filled" id="applyImport">确认导入 ${report.added} 个</button>`}</div>`, 'wide');
  const btn = $('#applyImport');
  if (btn) btn.onclick = async () => {
    btn.disabled = true;
    busyOn();
    try {
      const result = await call('ApplyImport');
      await loadLibrary();
      openImportPreview(result, true);
      toast('导入完成', 'success');
    } catch (e) { notifyError(e); btn.disabled = false; }
    finally { busyOff(); }
  };
}

/* --------------------------------------------------------------- copy utils */

async function copyText(text, okMsg = '已复制') {
  try {
    let copied = false;
    if (navigator.clipboard && navigator.clipboard.writeText) {
      try {
        await navigator.clipboard.writeText(text);
        copied = true;
      } catch (_) { /* WebView2 may deny clipboard without focus; fall back below */ }
    }
    if (!copied) {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      copied = document.execCommand('copy');
      ta.remove();
    }
    if (!copied) throw new Error('剪贴板不可用');
    toast(okMsg, 'success');
  } catch (e) { notifyError(new Error('复制失败：' + (e.message || e))); }
}

/* --------------------------------------------------------------- onboarding */

function showOnboarding() {
  const o = S.onboarding;
  const step = o.step;
  const dots = [0, 1, 2].map((i) => `<div class="onb-dot ${i <= step ? 'active' : ''}"></div>`).join('');
  let body = '';
  if (step === 0) {
    body = `<h1>欢迎使用 Komi Go</h1>
      <p>把 GitHub 上的 Release 变成可安装、可更新的 Windows 桌面应用。先选一个主题，之后也能在设置里改。</p>
      <div class="choice-grid">
        ${[['system', '跟随系统'], ['light', '浅色'], ['dark', '深色']].map(([k, l]) => `<button class="choice ${o.theme === k ? 'selected' : ''}" data-action="onb-theme" data-theme="${k}"><div class="swatch ${k}"></div><div class="t">${l}</div></button>`).join('')}
      </div>`;
  } else if (step === 1) {
    body = `<h1>保持更新，但不打扰</h1>
      <p>Komi 会在后台按间隔检查新版本，只提示、不自动安装。安装始终由你点击确认。</p>
      <label class="switch"><input type="checkbox" id="onbAuto" ${o.auto ? 'checked' : ''}><span class="track"></span><span class="label">启动后自动检查更新</span></label>
      <div class="note" style="margin-top:12px">预发布版本默认跳过；无 SHA-256 的资产会明确标注“未校验”。</div>`;
  } else {
    body = `<h1>放在托盘里</h1>
      <p>关闭到托盘后，下载和更新检查会继续；也可以选择开机自动启动，但启动时保持安静。</p>
      <label class="switch"><input type="checkbox" id="onbTray" ${o.tray ? 'checked' : ''}><span class="track"></span><span class="label">关闭窗口时隐藏到托盘</span></label>
      <label class="switch" style="margin-top:8px"><input type="checkbox" id="onbAutostart" ${o.autostart ? 'checked' : ''}><span class="track"></span><span class="label">开机自动启动（隐藏到托盘）</span></label>
      <div class="note" style="margin-top:12px">Komi 注册 komi:// 协议，之后可以直接从浏览器打开仓库页。</div>`;
  }
  $('#onboardingRoot').innerHTML = `<div class="onb-scrim"><div class="onb" role="dialog" aria-modal="true">
    <div class="onb-steps">${dots}</div>${body}
    <div class="onb-actions">
      <button class="btn text" data-action="onb-skip">跳过</button>
      <div class="hstack">
        ${step > 0 ? '<button class="btn outlined" data-action="onb-back">上一步</button>' : ''}
        <button class="btn filled" data-action="${step === 2 ? 'onb-finish' : 'onb-next'}">${step === 2 ? '开始使用' : '下一步'}</button>
      </div>
    </div>
  </div></div>`;
  const auto = $('#onbAuto');
  if (auto) auto.addEventListener('change', () => { o.auto = auto.checked; });
  const tray = $('#onbTray');
  if (tray) tray.addEventListener('change', () => { o.tray = tray.checked; });
  const as = $('#onbAutostart');
  if (as) as.addEventListener('change', () => { o.autostart = as.checked; });
}

async function finishOnboarding(skip = false) {
  const o = S.onboarding;
  $('#onboardingRoot').innerHTML = '';
  o.needs = false;
  busyOn();
  try {
    await call('CompleteOnboarding', skip ? 'system' : o.theme, skip ? true : o.auto, skip ? true : o.tray);
    if (!skip && o.autostart) {
      try { await call('SetAutostart', true); } catch (e) { notifyError(e); }
    }
    S.settings.theme = skip ? 'system' : o.theme;
    S.settings['update.auto'] = skip ? 'true' : String(o.auto);
    S.settings['ui.close_to_tray'] = skip ? 'true' : String(o.tray);
    setTheme(S.settings.theme);
    toast('设置已保存', 'success');
    await loadAuth();
  } catch (e) { notifyError(e); }
  finally { busyOff(); }
}

/* ------------------------------------------------------------------ render */

function render() {
  const main = $('#main');
  if (!main) return;
  $$('.rail-item').forEach((el) => {
    const active = el.dataset.route === S.route || (S.route === 'repo' && el.dataset.route === 'discover');
    if (active) el.setAttribute('aria-current', 'page'); else el.removeAttribute('aria-current');
  });
  switch (S.route) {
    case 'repo': main.innerHTML = repoPage(); break;
    case 'library': renderLibrary(); break;
    case 'downloads': renderDownloads(); break;
    case 'settings': renderSettings(); break;
    default: renderDiscover();
  }
  updateRail();
}

/* ------------------------------------------------------------------ events */

function bindGlobal() {
  document.addEventListener('click', onGlobalClick);
  document.addEventListener('change', onGlobalChange);
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      if (modalOpen) closeModal();
      else if (S.onboarding.needs) { /* keep onboarding */ }
    }
  });
  $$('.rail-item').forEach((el) => el.addEventListener('click', () => navigate(el.dataset.route)));
  $$('[data-action="win-min"],[data-action="win-max"],[data-action="win-close"]');
}

async function onGlobalClick(e) {
  const link = e.target.closest('[data-external="1"]');
  if (link) {
    e.preventDefault();
    const href = link.getAttribute('href');
    if (href) { try { await call('OpenExternal', href); } catch (err) { notifyError(err); } }
    return;
  }
  const el = e.target.closest('[data-action]');
  if (!el) {
    if (e.target.closest('[data-action="app-menu"]') === null && S.route === 'repo' && e.target.closest('.asset-row') === null) { /* noop */ }
    return;
  }
  const action = el.dataset.action;
  if (action === 'scrim') {
    if (e.target.closest && e.target.closest('.modal')) return;
    closeModal();
    return;
  }
  if (action === 'close-modal') { closeModal(); return; }
  if (action === 'win-min') { call('WindowMinimise').catch(() => {}); return; }
  if (action === 'win-max') { call('WindowMaximise').catch(() => {}); return; }
  if (action === 'win-close') { call('WindowClose').catch(() => {}); return; }

  try {
    switch (action) {
      case 'focus-search': {
        navigate('discover');
        setTimeout(() => { const i = $('#searchInput'); if (i) i.focus(); }, 60);
        break;
      }
      case 'feed': S.discover.feed = el.dataset.feed; S.discover.page = 1; S.discover.query = ''; loadDiscover(); break;
      case 'category': {
        const category = el.dataset.category || 'all';
        if (category !== S.discover.category) S.discover.categoryError = '';
        S.discover.category = category;
        if (S.route !== 'discover') navigate('discover');
        else render();
        if (category !== 'all') void loadCategoryCatalog();
        break;
      }
      case 'retry-category': S.discover.categoryError = ''; loadCategoryCatalog(); break;
      case 'history-search': {
        S.discover.query = el.dataset.q || '';
        S.discover.page = 1;
        const main = $('#main');
        if (main) main.scrollTop = 0;
        render();
        await loadDiscover();
        break;
      }
      case 'clear-history': {
        S.discover.history = [];
        try { localStorage.removeItem('komi.searchHistory'); } catch (_) { /* ignore */ }
        render();
        toast('搜索历史已清除');
        break;
      }
      case 'clear-search': S.discover.query = ''; S.discover.page = 1; render(); loadDiscover(); break;
      case 'retry-discover': loadDiscover(); break;
      case 'page': {
        const p = Number(el.dataset.page);
        if (p > 0) { S.discover.page = p; loadDiscover(); }
        break;
      }
      case 'platform': {
        S.discover.platform = el.dataset.platform || 'all';
        S.discover.page = 1;
        const main = $('#main');
        if (main) main.scrollTop = 0;
        loadDiscover();
        break;
      }
      case 'open-repo': await openRepo(el.dataset.host, el.dataset.owner, el.dataset.repo); break;
      case 'back-discover': S.route = 'discover'; render(); break;
      case 'refresh-repo': if (S.view) await openRepo(hostOf(S.view), S.view.owner, S.view.name, true); break;
      case 'retry-repo': if (S.view) await openRepo(hostOf(S.view), S.view.owner, S.view.name, false); break;
      case 'open-repo-page': if (S.view) await call('OpenRepoPage', hostOf(S.view), S.view.owner, S.view.name); break;
      case 'open-issues': if (S.view) await call('OpenExternal', S.view.issuesUrl || (repoUrl(S.view) + '/issues')); break;
      case 'scroll-releases': { const r = $('#repoSelector'); if (r) r.scrollIntoView({ behavior: 'smooth', block: 'start' }); break; }
      case 'repo-platform': {
        const ui = ensureRepoUi(S.view);
        ui.platform = el.dataset.platform || 'windows';
        repoResetSelection(S.view, ui);
        render();
        break;
      }
      case 'repo-channel': {
        const ui = ensureRepoUi(S.view);
        ui.channel = el.dataset.channel || 'stable';
        repoResetSelection(S.view, ui);
        render();
        break;
      }
      case 'install-selected': {
        const sel = repoSelected(S.view);
        if (sel.rel && sel.asset) await startDownload(sel.ui.release, sel.ui.asset);
        break;
      }
      case 'install-latest': {
        const p = pickLatestInstall(S.view && S.view.releases);
        if (p) await startDownload(p.i, p.j);
        break;
      }
      case 'toggle-readme': {
        const body = $('#readmeBody');
        if (body) {
          const expanded = body.classList.toggle('expanded');
          body.classList.toggle('collapsed', !expanded);
          const label = $('.readme-toggle .readme-label');
          if (label) label.textContent = expanded ? '收起' : '展开全文';
          $$('[data-action="toggle-readme"]').forEach((btn) => btn.classList.toggle('is-expanded', expanded));
        }
        break;
      }
      case 'release-notes': releaseNotesModal(Number(el.dataset.index)); break;
      case 'copy-asset': {
        const v = S.view; const rel = v.releases[Number(el.dataset.index)]; const a = rel.assets[Number(el.dataset.asset)];
        if (a) await copyText(a.downloadUrl || a.name, '下载链接已复制');
        break;
      }
      case 'download': await startDownload(Number(el.dataset.index), Number(el.dataset.asset)); break;
      case 'check-updates': await checkUpdates(true); break;
      case 'library-filter': S.libraryFilter = el.dataset.filter; renderLibrary(); break;
      case 'refresh-library': await loadLibrary(); renderLibrary(); break;
      case 'go-discover': navigate('discover'); break;
      case 'app-menu': await appMenu(el.dataset.id); break;
      case 'open-app': await call('OpenApp', el.dataset.id); toast('已启动应用', 'success'); break;
      case 'open-install-dir': await call('OpenInstallDir', el.dataset.id); break;
      case 'sync-app': {
        busyOn();
        try { const app = await call('UpdateInstallInfo', el.dataset.id); await loadLibrary(); render(); toast('安装信息已同步', 'success'); closeModal(); }
        catch (err) { notifyError(err); }
        finally { busyOff(); }
        break;
      }
      case 'update-app': {
        busyOn();
        try { await call('UpdateApp', el.dataset.id); await loadDownloads(); closeModal(); navigate('downloads'); toast('更新已加入下载队列', 'success'); }
        catch (err) { notifyError(err); }
        finally { busyOff(); }
        break;
      }
      case 'toggle-pin': {
        const app = (S.installs || []).find((x) => x.id === el.dataset.id);
        await call('SetPinned', el.dataset.id, !(app && app.pinned));
        await loadLibrary(); closeModal(); render(); break;
      }
      case 'skip-version': await call('SkipVersion', el.dataset.id); await loadLibrary(); closeModal(); render(); toast('已跳过这个版本', 'success'); break;
      case 'app-prefs': closeModal(); await appPrefs(el.dataset.id); break;
      case 'uninstall-app': {
        const app = (S.installs || []).find((x) => x.id === el.dataset.id);
        closeModal();
        confirmDialog({
          title: '卸载 ' + (app ? app.displayName : '这个应用'),
          body: app && app.installDir ? `将调用系统卸载程序，安装目录：${app.installDir}` : '将调用系统登记的卸载程序。',
          confirm: '卸载', danger: true,
          onConfirm: async () => {
            busyOn();
            try {
              const res = await call('UninstallApp', el.dataset.id);
              await loadLibrary(); render(); toast((res && res.message) || '已卸载', 'success');
            } finally { busyOff(); }
          },
        });
        break;
      }
      case 'restore-app': await call('RestoreTracking', el.dataset.id); await loadLibrary(); closeModal(); render(); break;
      case 'remove-app': {
        closeModal();
        confirmDialog({
          title: '移除记录', body: '只删除 Komi 的跟踪记录，不会动系统里的程序或文件。',
          confirm: '移除', danger: true,
          onConfirm: async () => { await call('RemoveInstalled', el.dataset.id); await loadLibrary(); render(); },
        });
        break;
      }
      case 'open-app-repo': {
        const app = (S.installs || []).find((x) => x.id === el.dataset.id);
        closeModal();
        if (app) await openRepo(hostOf(app), app.owner, app.repo);
        break;
      }
      case 'open-apps-folder': await call('OpenFolder', 'apps'); break;
      case 'open-data': await call('OpenFolder', 'data'); break;
      case 'open-logs': await call('OpenFolder', 'logs'); break;
      case 'open-downloads': await call('OpenDownloads'); break;
      case 'choose-download-dir': {
        const dir = await call('ChooseDownloadDir');
        if (!dir) break;
        S.auth = S.auth || {};
        S.auth.downloadsDir = dir;
        toast('下载目录已更新', 'success');
        if (S.route === 'downloads') renderDownloads();
        if (S.route === 'settings') renderSettings();
        break;
      }
      case 'reveal-download': await call('RevealDownload', el.dataset.id); break;
      case 'pause-download': await call('PauseDownload', el.dataset.id); await loadDownloads(); render(); break;
      case 'resume-download': await call('ResumeDownload', el.dataset.id); await loadDownloads(); render(); break;
      case 'cancel-download': {
        confirmDialog({
          title: '取消下载', body: '会删除临时文件与这条任务记录。', confirm: '取消下载', danger: true,
          onConfirm: async () => { await call('CancelDownload', el.dataset.id); await loadDownloads(); render(); },
        });
        break;
      }
      case 'forget-download': await call('ForgetDownload', el.dataset.id); await loadDownloads(); render(); break;
      case 'install-download': await installDownload(el.dataset.id); break;
      case 'clear-downloads': {
        const n = await call('ClearFinishedDownloads');
        await loadDownloads(); render(); toast(n ? `已清理 ${n} 条记录（文件保留）` : '没有可清理的记录', 'success');
        break;
      }
      case 'theme': {
        const mode = el.dataset.theme;
        await call('SetSetting', 'theme', mode);
        S.settings.theme = mode; setTheme(mode);
        if (S.route === 'settings') renderSettings();
        break;
      }
      case 'accent-color': {
        const color = String(el.dataset.accent || '').toLowerCase();
        if (!/^#[0-9a-f]{6}$/.test(color)) break;
        await call('SetSetting', 'ui.accent', color);
        S.settings['ui.accent'] = color;
        S.accent = color;
        applyAccent();
        renderSettings();
        toast('主题色已更新', 'success');
        break;
      }
      case 'accent-reset': {
        await call('SetSetting', 'ui.accent', '');
        S.settings['ui.accent'] = '';
        S.accent = '';
        applyAccent();
        renderSettings();
        toast('已恢复默认主题色', 'success');
        break;
      }
      case 'save-proxy': {
        const v = $('#proxyInput').value.trim();
        busyOn();
        try { await call('SetProxy', v); S.settings['network.proxy'] = v; toast('代理已应用', 'success'); }
        catch (err) { notifyError(err); }
        finally { busyOff(); }
        break;
      }
      case 'save-mirrors': {
        const v = $('#mirrorsInput').value.trim();
        busyOn();
        try {
          await call('SetSetting', 'network.mirrors', v);
          try { S.settings = (await call('GetSettings')) || S.settings; } catch (_) { S.settings['network.mirrors'] = v; }
          S.mirrorResults = [];
          renderSettings();
          toast('镜像已保存', 'success');
        }
        catch (err) { notifyError(err); }
        finally { busyOff(); }
        break;
      }
      case 'test-mirrors': {
        if (S.mirrorTesting) break;
        S.mirrorTesting = true;
        renderSettings();
        try {
          const r = await call('TestMirrors');
          S.mirrorResults = r || [];
          const ok = S.mirrorResults.filter((x) => x && x.ok);
          toast(ok.length ? `测速完成，${ok.length} 个镜像可用，最快 ${Math.min(...ok.map((x) => x.latencyMs))} ms` : '测速完成，没有可用镜像', ok.length ? 'success' : 'error');
        } catch (err) { notifyError(err); }
        finally { S.mirrorTesting = false; renderSettings(); }
        break;
      }
      case 'ping': {
        busyOn();
        try { const r = await call('PingAPI'); toast(r.ok ? `连接正常 · ${r.ms} ms` : `连接失败：${r.error}`, r.ok ? 'success' : 'error'); }
        catch (err) { notifyError(err); }
        finally { busyOff(); }
        break;
      }
      case 'clear-cache': {
        const n = await call('ClearCache', 'all');
        await loadCache();
        toast(`已清理 ${n} 条缓存`, 'success');
        if (S.route === 'settings') renderSettings();
        break;
      }
      case 'login': await loginModal(); break;
      case 'settings-general': S.settingsScreen = 'general'; renderSettings(); $('#main').scrollTop = 0; break;
      case 'settings-section': S.settingsScreen = 'section'; S.settingsSection = el.dataset.section || 'appearance'; renderSettings(); $('#main').scrollTop = 0; break;
      case 'settings-hub': S.settingsScreen = 'hub'; renderSettings(); $('#main').scrollTop = 0; break;
      case 'profile-open': {
        if (!(S.auth && S.auth.signedIn)) { await loginModal(); break; }
        S.settingsScreen = 'profile'; renderSettings(); $('#main').scrollTop = 0;
        ensureProfileRepos();
        ensureContributions();
        break;
      }
      case 'profile-filter': S.profileFilter = el.dataset.filter || 'released'; renderSettings(); break;
      case 'profile-repos-retry': S.profileReposLoaded = false; ensureProfileRepos(true); break;
      case 'contributions-retry': S.contributionsLoaded = false; ensureContributions(true); break;
      case 'profile-open-repo': await openRepo('github.com', el.dataset.owner, el.dataset.name); break;
      case 'nav-library': navigate('library'); break;
      case 'nav-downloads': navigate('downloads'); break;
      case 'open-stars': {
        if (!(S.auth && S.auth.signedIn)) { toast('请先登录 GitHub', 'error'); break; }
        await call('OpenExternal', `https://github.com/${S.auth.login || ''}?tab=stars`);
        break;
      }
      case 'about-app': {
        openModal(`<div class="modal-head"><div><div class="modal-title">关于 Komi Go</div><div class="modal-sub">GitHub Release 应用商店 · Windows 桌面端</div></div><button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
          <div class="about-list">
            <div><span>版本</span><strong>${esc((S.auth && S.auth.version) || '')}</strong></div>
            <div><span>技术栈</span><strong>Go · Wails v2 · SQLite · WebView2</strong></div>
            <div><span>界面</span><strong>Material 3 Expressive</strong></div>
            <div><span>仓库</span><strong>Destroyer236/komi-go</strong></div>
            <div><span>数据目录</span><strong class="mono">${esc((S.auth && S.auth.dataDir) || '')}</strong></div>
          </div>
          <div class="modal-actions"><button class="btn text" data-action="close-modal">关闭</button><button class="btn outlined" data-action="open-repo">打开 GitHub 仓库</button></div>`, 'narrow');
        break;
      }
      case 'open-repo': await call('OpenExternal', 'https://github.com/Destroyer236/komi-go'); break;
      case 'release-notes': {
        openModal(`<div class="modal-head"><div><div class="modal-title">更新内容</div><div class="modal-sub">Komi Go ${esc((S.auth && S.auth.version) || '')} · 最近版本</div></div><button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
          <div class="release-list">
            <div class="release-item"><span class="release-dot"></span><div><strong>设备码登录开箱可用</strong><p>内置 OAuth App，复制 8 位授权码后弹窗保持打开，轮询会跨瞬时网络错误继续。</p></div></div>
            <div class="release-item"><span class="release-dot"></span><div><strong>详情页选择器</strong><p>平台、稳定/预发布通道、资源与版本集中在一个紧凑选择器中。</p></div></div>
            <div class="release-item"><span class="release-dot"></span><div><strong>API 配额实时刷新</strong><p>设置页直接读取 GitHub rate_limit，不再依赖镜像响应头。</p></div></div>
            <div class="release-item"><span class="release-dot"></span><div><strong>设置页重构</strong><p>个人资料、资料库、更新、应用与账户按分组列表组织。</p></div></div>
          </div>
          <div class="modal-actions"><button class="btn text" data-action="close-modal">关闭</button><button class="btn filled" data-action="check-self-update">检查应用更新</button></div>`, 'narrow');
        break;
      }
      case 'announcements': {
        openModal(`<div class="modal-head"><div><div class="modal-title">公告</div><div class="modal-sub">Komi Go 的使用提示与重要通知</div></div><button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
          <div class="note" style="margin-bottom:12px">令牌保存在 Windows 凭据管理器，设备码流程不会让客户端持有 client secret。</div>
          <div class="note" style="margin-bottom:12px">下载镜像只代理资产文件，API 请求仍直连 GitHub；遇到限流时可在设置里等待配额重置。</div>
          <div class="note">安装包会请求 Windows 自己的权限确认，Komi Go 不会静默提权。</div>
          <div class="modal-actions"><button class="btn text" data-action="close-modal">知道了</button></div>`, 'narrow');
        break;
      }
      case 'account': if (S.auth && S.auth.signedIn) navigate('settings'); else await loginModal(); break;
      case 'login-device': await loginModal(); break;
      case 'logout': {
        confirmDialog({
          title: '退出登录', body: '会从 Windows 凭据管理器删除令牌。', confirm: '退出', danger: true,
          onConfirm: async () => { await call('ClearToken'); S.profile = {}; S.profileRepos = []; S.profileReposLoaded = false; S.contributions = null; S.contributionsLoaded = false; await loadAuth(); if (S.route === 'settings') renderSettings(); },
        });
        break;
      }
      case 'open-device-url': await call('OpenExternal', el.dataset.url); break;
      case 'register-scheme': await call('RegisterURLScheme'); await loadAuth(); renderSettings(); toast('komi:// 已注册', 'success'); break;
      case 'check-self-update': {
        busyOn();
        try {
          S.selfInfo = await call('CheckSelfUpdate');
          if (S.selfInfo.available) {
            toast(`发现新版本 ${S.selfInfo.latest}`, 'success');
            openModal(`<div class="modal-head"><div><div class="modal-title">发现新版本 ${esc(S.selfInfo.latest)}</div><div class="modal-sub">当前 ${esc(S.selfInfo.current)} · ${esc(S.selfInfo.name)} · ${fmtBytes(S.selfInfo.size)}</div></div><button class="icon-btn" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>
              <div class="note">下载完成后会启动安装器；应用会先退出，安装脚本等待 2 秒后执行并重新启动。</div>
              <div class="modal-actions"><button class="btn text" data-action="close-modal">稍后</button><button class="btn filled" id="selfUpdateNow">下载并安装</button></div>`, 'narrow');
            const now = $('#selfUpdateNow');
            if (now) now.onclick = async () => { closeModal(); await call('DownloadAndApplySelfUpdate'); };
          } else toast('已经是最新版本', '');
          renderSettings();
        } catch (err) { notifyError(err); }
        finally { busyOff(); }
        break;
      }
      case 'download-self-update': {
        busyOn();
        try { await call('DownloadAndApplySelfUpdate'); }
        catch (err) { notifyError(err); }
        finally { busyOff(); }
        break;
      }
      case 'export-library': {
        busyOn();
        try { const p = await call('ExportLibraryFile'); if (p) toast('已导出：' + p, 'success'); }
        catch (err) { if (!/取消/.test(err.message)) notifyError(err); }
        finally { busyOff(); }
        break;
      }
      case 'export-obtainium': {
        busyOn();
        try { const p = await call('ExportObtainiumFile'); if (p) toast('已导出 Obtainium 列表：' + p, 'success'); }
        catch (err) { if (!/取消/.test(err.message)) notifyError(err); }
        finally { busyOff(); }
        break;
      }
      case 'import-library': await importLibrary(); break;
      case 'scan-external': await scanExternalModal(); break;
      case 'match-external': await matchExternal(Number(el.dataset.index)); break;
      case 'track-external': {
        busyOn();
        try {
          await call('TrackApp', 'github.com', el.dataset.owner, el.dataset.repo, el.dataset.name);
          toast('已加入跟踪列表', 'success');
          await loadLibrary();
        } catch (err) { notifyError(err); }
        finally { busyOff(); }
        break;
      }
      case 'copy-diagnostics': {
        busyOn();
        try { const text = await call('Diagnostics'); await copyText(text, '诊断信息已复制'); }
        catch (err) { notifyError(err); }
        finally { busyOff(); }
        break;
      }
      case 'onb-theme': S.onboarding.theme = el.dataset.theme; setTheme(el.dataset.theme); showOnboarding(); break;
      case 'onb-next': S.onboarding.step = Math.min(2, S.onboarding.step + 1); showOnboarding(); break;
      case 'onb-back': S.onboarding.step = Math.max(0, S.onboarding.step - 1); showOnboarding(); break;
      case 'onb-finish': await finishOnboarding(false); break;
      case 'onb-skip': await finishOnboarding(true); break;
      default: break;
    }
  } catch (err) {
    notifyError(err);
  }
}

async function onGlobalChange(e) {
  const t = e.target;
  if (!t) return;
  if (t.matches('select[data-repo-select]')) {
    const view = S.view;
    if (!view) return;
    const ui = ensureRepoUi(view);
    if (t.dataset.repoSelect === 'release') {
      ui.release = Number(t.value) || 0;
      const rel = view.releases[ui.release];
      const assets = repoPlatformAssets(rel, ui.platform);
      const rec = assets.findIndex((a) => a.recommended);
      const first = assets[rec >= 0 ? rec : 0];
      ui.asset = rel && rel.assets && first ? rel.assets.indexOf(first) : 0;
    } else {
      ui.asset = Number(t.value) || 0;
    }
    render();
    return;
  }
  if (t.matches('select[data-repo-channel]')) {
    const ui = ensureRepoUi(S.view);
    ui.channel = t.value || 'stable';
    repoResetSelection(S.view, ui);
    render();
    return;
  }
  if (t.matches('input[data-setting-local="only"]')) {
    return;
  }
  if (t.matches('select[data-action="mirror-mode"]')) {
    const mode = t.value;
    try {
      await call('SetSetting', 'network.mirrorSelected', mode);
      S.settings['network.mirrorSelected'] = mode;
      try { S.settings = (await call('GetSettings')) || S.settings; } catch (_) {}
      renderSettings();
      toast(mode === 'auto' ? '已切换为自动选择最快镜像' : mode === 'direct' ? '已切换为直连' : '已固定使用所选镜像', 'success');
    } catch (err) { notifyError(err); }
    return;
  }
  if (t.matches('select[data-action="lang"]')) {
    S.discover.lang = t.value;
    S.discover.page = 1;
    loadDiscover();
    return;
  }
  if (t.matches('select[data-action="sort"]')) {
    S.discover.sort = t.value;
    S.discover.page = 1;
    loadDiscover();
    return;
  }
  const key = t.dataset && t.dataset.setting;
  if (!key) return;
  let value;
  if (t.type === 'checkbox') value = t.checked ? 'true' : 'false';
  else value = t.value;
  try {
    await call('SetSetting', key, String(value));
    S.settings[key] = String(value);
    if (key === 'theme') setTheme(String(value));
    if (key === 'ui.reduce_motion') applyMotion();
    toast('设置已保存', 'success');
  } catch (err) {
    notifyError(err);
    if (t.type === 'checkbox') t.checked = !t.checked;
  }
}

/* --------------------------------------------------------------------- boot */

function bindBackendEvents() {
  onEvent('app:ready', (payload) => { if (payload) { S.auth = payload; updateRail(); } });
  onEvent('auth:changed', async (payload) => {
    S.auth = payload || S.auth;
    S.profileReposLoaded = false;
    S.profileRepos = [];
    S.contributions = null;
    S.contributionsLoaded = false;
    updateRail();
    if (S.route === 'settings') { await loadProfile(); renderSettings(); }
  });
  onEvent('auth:expired', (msg) => { toast(msg || '登录已失效', 'error'); loadAuth(); });
  onEvent('auth:warning', (msg) => toast(msg));
  onEvent('settings:changed', (payload) => { if (payload) Object.assign(S.settings, payload); });
  onEvent('ledger:changed', async () => { await loadLibrary(); if (['library', 'settings', 'discover'].includes(S.route)) render(); });
  onEvent('download:added', () => { loadDownloads().then(() => { if (S.route === 'downloads') render(); }); });
  onEvent('download:changed', () => { loadDownloads().then(() => { if (S.route === 'downloads') render(); }); });
  onEvent('download:progress', (job) => {
    if (!job || !job.id) return;
    const i = S.downloads.findIndex((x) => x.id === job.id);
    if (i >= 0) S.downloads[i] = Object.assign({}, S.downloads[i], job);
    else S.downloads.push(job);
    updateRail();
    if (S.route === 'downloads' && !renderTimer) {
      renderTimer = setTimeout(() => { renderTimer = null; render(); }, 250);
    }
  });
  onEvent('download:done', (job) => {
    if (job && job.id) {
      job.status = 'done';
      job.percent = 100;
      job.done = job.total || job.done;
      const i = S.downloads.findIndex((x) => x.id === job.id);
      if (i >= 0) S.downloads[i] = Object.assign({}, S.downloads[i], job);
      else S.downloads.push(job);
    }
    loadDownloads().then(() => {
      updateRail();
      if (S.route === 'downloads') render();
    });
  });
  onEvent('download:removed', () => loadDownloads().then(() => { if (S.route === 'downloads') render(); }));
  onEvent('install:pending', (p) => toast(`${p.name || '应用'} 等待系统确认`, ''));
  onEvent('install:done', (p) => { toast(`${p.name || '应用'} 安装完成`, 'success'); loadLibrary(); });
  onEvent('install:timeout', (p) => toast(`${p.name || '应用'} 未在系统中确认，可稍后“同步安装信息”`, 'error'));
  onEvent('install:error', (p) => toast(`${p.name || '应用'} 登记失败：${p.error}`, 'error'));
  onEvent('updates:checked', (p) => { if (p && p.count) toast(`后台检查：${p.count} 个可用更新`, 'success'); loadLibrary(); });
  onEvent('deeplink', (ref) => {
    if (!ref) return;
    toast('正在打开分享链接');
    if (ref.host && ref.host !== 'github.com') openRepo(ref.host, ref.owner, ref.repo);
    else openRepo('github.com', ref.owner, ref.repo);
  });
  onEvent('selfupdate:progress', (p) => {
    if (p && p.total) toast(`应用更新下载 ${Math.round((p.done / p.total) * 100)}%`);
  });
  onEvent('tray:ready', () => { S.auth.tray = true; updateRail(); });
}

async function boot() {
  // Always land on Discover after a restart; a repo that failed last time must
  // not trap the user on an error page. A deep-link event can navigate later.
  S.route = 'discover';
  S.view = null;
  S.repoLoading = false;
  S.repoError = '';
  S.busy = 0;
  const line = $('#loadingLine');
  if (line) { line.hidden = true; line.style.display = 'none'; }
  searchHistoryLoad();
  bindGlobal();
  bindBackendEvents();

  await Promise.all([loadSettings(), loadAuth(), loadDownloads(), loadLibrary()]);
  try {
    S.onboarding = (await call('OnboardingState')) || S.onboarding;
    S.onboarding.step = 0;
    if (S.onboarding.needsOnboarding || S.onboarding.needs) {
      S.onboarding.needs = true;
      S.onboarding.theme = S.onboarding.theme || 'system';
      S.onboarding.auto = S.onboarding.auto !== false;
      S.onboarding.tray = S.onboarding.tray !== false;
      showOnboarding();
    }
  } catch (e) { /* first run state is optional */ }

  render();
  if (!S.discover.query) loadDiscover();
  setInterval(() => { if (S.route === 'downloads') loadDownloads(); }, 4000);
}

if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
else boot();
