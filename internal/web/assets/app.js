// sentra web — vanilla SPA-lite: session gate, hash router, fetch + SSE.
// No framework, no build step.

const $ = (sel, root = document) => root.querySelector(sel);
const el = (html) => { const t = document.createElement('template'); t.innerHTML = html.trim(); return t.content.firstElementChild; };
const esc = (s) => String(s).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
const fmtBytes = (n) => { if (n < 1024) return n + ' B'; const u = ['KB', 'MB', 'GB', 'TB']; let i = -1; do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1); return n.toFixed(1) + ' ' + u[i]; };

async function api(path, opts) {
  const r = await fetch(path, { credentials: 'same-origin', ...opts });
  if (r.status === 401) { showUnlock(); throw new Error('locked'); }
  const ct = r.headers.get('content-type') || '';
  const body = ct.includes('json') ? await r.json() : await r.text();
  if (!r.ok) throw new Error((body && body.error) || ('request failed: ' + r.status));
  return body;
}

const NAV = [
  { cat: 'Views', items: [['dashboard', 'Dashboard'], ['snapshots', 'Snapshots']] },
  { cat: 'Operations', items: [['backup', 'Backup']] },
  { cat: 'Inspect', items: [['check', 'Check'], ['diff', 'Diff'], ['recovery', 'Recovery Kit']] },
];

function renderNav() {
  const nav = $('#nav'); nav.innerHTML = '';
  for (const g of NAV) {
    nav.appendChild(el(`<div class="cat">${g.cat}</div>`));
    for (const [id, label] of g.items) nav.appendChild(el(`<a href="#/${id}" data-view="${id}">${label}</a>`));
  }
}
function setActive(view) {
  document.querySelectorAll('#nav a').forEach(a => a.classList.toggle('active', a.dataset.view === view));
}

// ---------- session ----------
function showUnlock() { $('#app').hidden = true; $('#unlock').hidden = false; $('#pass').focus(); }
function showApp() { $('#unlock').hidden = true; $('#app').hidden = false; }

async function boot() {
  renderNav();
  const s = await api('/api/session');
  $('#repo').textContent = s.repoName || '';
  if (s.locked) { showUnlock(); return; }
  showApp();
  route();
}

$('#unlock-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const errBox = $('#unlock-err'); errBox.hidden = true;
  try {
    const s = await api('/api/unlock', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ passphrase: $('#pass').value }) });
    $('#pass').value = '';
    $('#repo').textContent = s.repoName || '';
    showApp(); if (!location.hash) location.hash = '#/dashboard'; route();
  } catch (err) { errBox.textContent = err.message; errBox.hidden = false; }
});

// ---------- router ----------
const VIEWS = { dashboard: viewDashboard, snapshots: viewSnapshots, backup: viewBackup, check: viewCheck, diff: viewDiff, recovery: viewRecovery };
function route() {
  const view = (location.hash.replace(/^#\//, '') || 'dashboard').split('/')[0];
  const fn = VIEWS[view] || viewDashboard;
  setActive(view);
  const c = $('#content'); c.innerHTML = '<div class="sub">loading…</div>';
  fn(c).catch(err => { if (err.message !== 'locked') c.innerHTML = `<div class="err">${esc(err.message)}</div>`; });
}
window.addEventListener('hashchange', route);

// ---------- views ----------
async function viewDashboard(c) {
  const d = await api('/api/dashboard');
  const last = d.lastSnapshot;
  c.innerHTML = `
    <p class="eyebrow">Dashboard</p>
    <div class="cards">
      <div class="panel"><h3>repository</h3><div class="metric magenta">${d.snapshotCount}</div><div class="sub">snapshots · ${fmtBytes(d.totalBytes)} total</div></div>
      <div class="panel"><h3>last snapshot</h3>${last ? `<div class="metric">${esc(last.id.slice(0, 12))}</div><div class="sub">${esc(last.createdAt)} · ${last.files} files · ${fmtBytes(last.bytes)}</div>` : `<div class="metric mint">—</div><div class="sub">no snapshots yet</div>`}</div>
      <div class="panel"><h3>agent</h3><div class="metric mint">${d.recCount}</div><div class="sub">recommendations</div></div>
    </div>`;
}

async function viewSnapshots(c) {
  const list = await api('/api/snapshots');
  if (!list.length) { c.innerHTML = `<p class="eyebrow">Snapshots</p><div class="sub">no snapshots yet — run a backup.</div>`; return; }
  const rows = list.map(s => `<tr class="row" data-id="${esc(s.id)}"><td class="id">${esc(s.id.slice(0, 16))}</td><td>${esc(s.createdAt)}</td><td>${s.tag ? `<span class="tag-pill">${esc(s.tag)}</span>` : '—'}</td><td>${s.files}</td><td>${fmtBytes(s.bytes)}</td></tr>`).join('');
  c.innerHTML = `<p class="eyebrow">Snapshots</p><table><thead><tr><th>ID</th><th>Created</th><th>Tag</th><th>Files</th><th>Size</th></tr></thead><tbody>${rows}</tbody></table><div id="detail"></div>`;
  c.querySelectorAll('tr.row').forEach(tr => tr.addEventListener('click', () => showDetail(tr.dataset.id)));
}
async function showDetail(id) {
  const d = await api('/api/snapshots/' + encodeURIComponent(id));
  const files = d.files.slice(0, 500).map(f => `<tr><td>${esc(f.path)}</td><td>${fmtBytes(f.size)}</td><td class="sub">${esc(f.mode)}</td></tr>`).join('');
  $('#detail').innerHTML = `<div class="panel" style="margin-top:1.2rem"><h3>${esc(id.slice(0, 16))} · ${esc(d.root)}</h3><div class="sub">${d.stats.files} files · ${fmtBytes(d.stats.bytes)}</div><table style="margin-top:.6rem"><tbody>${files}</tbody></table>${d.files.length > 500 ? '<div class="sub">…first 500 shown</div>' : ''}</div>`;
}

// ---------- backup ----------
let pickerCwd = '';
async function viewBackup(c) {
  c.innerHTML = `<p class="eyebrow">New backup</p>
    <div id="picker" class="picker"></div>
    <div class="startbar"><input id="tag" placeholder="optional tag"><button id="start" class="btn go">Start backup</button></div>
    <div id="prog"></div>`;
  await loadPicker('');
  $('#start').addEventListener('click', () => confirmBackup());
}
async function loadPicker(path) {
  const d = await api('/api/fs?path=' + encodeURIComponent(path));
  pickerCwd = d.cwd;
  const rows = [];
  if (d.parent) rows.push(`<div class="row" data-path="${esc(d.parent)}"><span class="ico">▲</span> ..</div>`);
  for (const name of d.dirs) rows.push(`<div class="row" data-path="${esc(d.cwd + '/' + name)}"><span class="ico">▸</span> ${esc(name)}/</div>`);
  $('#picker').innerHTML = `<div class="path">${esc(d.cwd)}</div>${rows.join('') || '<div class="row sub">(no subfolders)</div>'}`;
  $('#picker').querySelectorAll('.row[data-path]').forEach(r => r.addEventListener('click', () => loadPicker(r.dataset.path)));
}
function confirmBackup() {
  const m = el(`<div class="modal-wrap"><div class="card modal">
    <div class="mh">Start backup?</div>
    <div class="mb">This backs up <b>${esc(pickerCwd)}</b> to your repository. Existing snapshots are untouched.</div>
    <div class="mrow"><button class="btn" id="cancel">Cancel</button><button class="btn primary" id="go">Start backup</button></div>
  </div></div>`);
  document.body.appendChild(m);
  $('#cancel', m).addEventListener('click', () => m.remove());
  $('#go', m).addEventListener('click', () => { m.remove(); startBackup(); });
}
async function startBackup() {
  const prog = $('#prog');
  prog.innerHTML = `<div class="progress"><div class="bar"><i id="fill"></i></div><div id="pmsg" class="sub" style="margin-top:.4rem">starting…</div></div>`;
  let res;
  try {
    res = await api('/api/backup', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ root: pickerCwd, tag: $('#tag').value }) });
  } catch (err) { $('#pmsg').innerHTML = `<span class="err">${esc(err.message)}</span>`; return; }
  const ev = new EventSource('/api/backup/' + res.opId + '/events');
  ev.addEventListener('progress', (e) => {
    const p = JSON.parse(e.data);
    const pct = p.total > 0 ? Math.round(100 * p.done / p.total) : 0;
    $('#fill').style.width = pct + '%';
    $('#pmsg').textContent = `${fmtBytes(p.done)} / ${fmtBytes(p.total)} (${pct}%)`;
  });
  ev.addEventListener('done', (e) => {
    ev.close(); $('#fill').style.width = '100%';
    const s = JSON.parse(e.data).snapshot;
    $('#pmsg').innerHTML = `<span class="ok">✓ Backup complete</span> — ${esc(s.id.slice(0, 12))} · ${s.files} files · ${fmtBytes(s.bytes)}`;
  });
  ev.addEventListener('error', (e) => {
    ev.close();
    const msg = e.data ? JSON.parse(e.data).message : 'connection lost';
    $('#pmsg').innerHTML = `<span class="err">${esc(msg)}</span>`;
  });
}

// ---------- inspect ----------
async function viewCheck(c) {
  c.innerHTML = `<p class="eyebrow">Integrity check</p><div class="sub">scanning the repository…</div>`;
  const r = await api('/api/check');
  const issues = (r.missing_blobs || []).length + (r.manifest_issues || []).length;
  const staleLock = r.lock && r.lock.stale;
  const verdict = issues === 0 && !staleLock
    ? `<span class="ok">✓ healthy</span>`
    : `<span class="err">${issues} issue${issues === 1 ? '' : 's'}${staleLock ? ' · stale lock' : ''}</span>`;
  c.innerHTML = `<p class="eyebrow">Integrity check</p>
    <div class="cards">
      <div class="panel"><h3>verdict</h3><div class="metric ${issues || staleLock ? '' : 'mint'}">${verdict}</div><div class="sub">checked ${esc(r.checked_at || '')}</div></div>
      <div class="panel"><h3>contents</h3><div class="metric">${r.snapshots}</div><div class="sub">snapshots · ${r.files} files · ${fmtBytes(r.bytes)}</div></div>
      <div class="panel"><h3>blobs</h3><div class="metric magenta">${r.data_blobs}</div><div class="sub">${r.referenced_blobs} referenced · ${fmtBytes(r.orphan_bytes)} orphaned</div></div>
    </div>
    ${issues ? `<div class="panel" style="margin-top:1.2rem;border-color:var(--red)"><h3 style="color:var(--red)">issues</h3>${(r.missing_blobs || []).map(m => `<div class="err">missing blob ${esc(JSON.stringify(m))}</div>`).join('')}${(r.manifest_issues || []).map(m => `<div class="err">${esc(JSON.stringify(m))}</div>`).join('')}</div>` : ''}`;
}

async function viewDiff(c) {
  const snaps = await api('/api/snapshots');
  const opts = snaps.map(s => `<option value="${esc(s.id)}">${esc(s.id.slice(0, 20))} · ${esc(s.tag || 'no tag')}</option>`).join('');
  c.innerHTML = `<p class="eyebrow">Diff snapshots</p>
    <div class="startbar"><select id="da">${opts}</select><span class="sub">→</span><select id="db">${opts}</select><button id="cmp" class="btn go">Compare</button></div>
    <div id="dout"></div>`;
  if (snaps.length > 1) $('#db').selectedIndex = 1;
  $('#cmp').addEventListener('click', async () => {
    const a = $('#da').value, b = $('#db').value;
    $('#dout').innerHTML = '<div class="sub" style="margin-top:1rem">comparing…</div>';
    try {
      const d = await api(`/api/diff?a=${encodeURIComponent(a)}&b=${encodeURIComponent(b)}`);
      const col = (title, arr, cls) => `<div class="panel" style="border-color:var(--${cls})"><h3>${title} (${arr.length})</h3>${arr.slice(0, 300).map(p => `<div class="sub">${esc(p)}</div>`).join('') || '<div class="sub">—</div>'}</div>`;
      $('#dout').innerHTML = `<div class="cards" style="margin-top:1.2rem">${col('added', d.added, 'mint')}${col('removed', d.removed, 'red')}${col('changed', d.changed, 'gold')}</div>`;
    } catch (err) { $('#dout').innerHTML = `<div class="err" style="margin-top:1rem">${esc(err.message)}</div>`; }
  });
}

async function viewRecovery(c) {
  c.innerHTML = `<p class="eyebrow">Recovery kit</p><div class="sub">building…</div>`;
  const r = await api('/api/recovery-kit');
  c.innerHTML = `<p class="eyebrow">Recovery kit</p>
    <div class="sub" style="margin-bottom:.8rem">How to recover this repository. No secrets are included. <button id="dl" class="btn">Download .md</button></div>
    <div class="panel" style="border-color:var(--purple)"><pre style="white-space:pre-wrap;margin:0;color:var(--subtle);font-size:.82rem">${esc(r.markdown)}</pre></div>`;
  $('#dl').addEventListener('click', () => {
    const blob = new Blob([r.markdown], { type: 'text/markdown' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob); a.download = 'sentra-recovery-kit.md'; a.click();
    URL.revokeObjectURL(a.href);
  });
}

boot().catch(err => { document.body.innerHTML = `<pre style="color:#FF6B86;padding:2rem">${esc(err.message)}</pre>`; });
