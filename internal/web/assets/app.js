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
  { cat: 'Operations', items: [['backup', 'Backup'], ['restore', 'Restore'], ['prune', 'Prune'], ['sync', 'Sync'], ['password', 'Password']] },
  { cat: 'Manage', items: [['policies', 'Policies'], ['schedule', 'Schedule'], ['agent', 'Agent']] },
  { cat: 'Inspect', items: [['check', 'Check'], ['diff', 'Diff'], ['doctor', 'Doctor'], ['recovery', 'Recovery Kit']] },
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
function showUnlock() { $('#app').hidden = true; $('#setup').hidden = true; $('#unlock').hidden = false; $('#pass').focus(); }
function showApp() { $('#unlock').hidden = true; $('#setup').hidden = true; $('#app').hidden = false; }

async function boot() {
  renderNav();
  const s = await api('/api/session');
  $('#repo').textContent = s.repoName || '';
  if (s.setupNeeded) { bootSetup(); return; }
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
const VIEWS = { dashboard: viewDashboard, snapshots: viewSnapshots, backup: viewBackup, check: viewCheck, diff: viewDiff, recovery: viewRecovery, restore: viewRestore, prune: viewPrune, password: viewPassword, sync: viewSync, doctor: viewDoctor, policies: viewPolicies, schedule: viewSchedule, agent: viewAgent };
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

// ---------- shared: typed confirm + SSE + progress ----------
function typedConfirm(word, title, bodyHtml) {
  return new Promise((resolve) => {
    const m = el(`<div class="modal-wrap"><div class="card modal">
      <div class="mh">${esc(title)}</div><div class="mb">${bodyHtml}</div>
      <input id="cw" placeholder="type &quot;${esc(word)}&quot; to confirm" autocomplete="off" style="margin-top:.9rem;width:100%">
      <div class="mrow"><button class="btn" id="cc">Cancel</button><button class="btn primary" id="co" disabled style="opacity:.5">Confirm</button></div>
    </div></div>`);
    document.body.appendChild(m);
    const inp = $('#cw', m), ok = $('#co', m); inp.focus();
    inp.addEventListener('input', () => { const g = inp.value === word; ok.disabled = !g; ok.style.opacity = g ? '1' : '.5'; });
    const close = v => { m.remove(); resolve(v); };
    $('#cc', m).addEventListener('click', () => close(false));
    ok.addEventListener('click', () => { if (inp.value === word) close(true); });
  });
}
// confirmSimple is a yes/no modal for reversible actions (delete a policy,
// install a schedule) that don't warrant a typed word.
function confirmSimple(title, bodyHtml) {
  return new Promise((resolve) => {
    const m = el(`<div class="modal-wrap"><div class="card modal">
      <div class="mh">${esc(title)}</div><div class="mb">${bodyHtml}</div>
      <div class="mrow"><button class="btn" id="cc">Cancel</button><button class="btn primary" id="co">Confirm</button></div>
    </div></div>`);
    document.body.appendChild(m);
    const close = v => { m.remove(); resolve(v); };
    $('#cc', m).addEventListener('click', () => close(false));
    $('#co', m).addEventListener('click', () => close(true));
  });
}
function streamSSE(opId, { onProgress, onDone, onError }) {
  const ev = new EventSource('/api/op/' + opId + '/events');
  ev.addEventListener('progress', e => onProgress(JSON.parse(e.data)));
  ev.addEventListener('done', e => { ev.close(); onDone(JSON.parse(e.data)); });
  ev.addEventListener('error', e => { ev.close(); onError(e.data ? JSON.parse(e.data).message : 'connection lost'); });
}
function progressBox(container) {
  container.innerHTML = `<div class="progress"><div class="bar"><i id="fill"></i></div><div id="pmsg" class="sub" style="margin-top:.4rem">starting…</div></div>`;
  return {
    prog: p => { const pct = p.total > 0 ? Math.round(100 * p.done / p.total) : 0; $('#fill').style.width = pct + '%'; $('#pmsg').textContent = `${fmtBytes(p.done)} / ${fmtBytes(p.total)} (${pct}%)`; },
    ok: html => { $('#fill').style.width = '100%'; $('#pmsg').innerHTML = html; },
    err: msg => { $('#pmsg').innerHTML = `<span class="err">${esc(msg)}</span>`; },
  };
}

// ---------- restore ----------
async function viewRestore(c) {
  const snaps = await api('/api/snapshots');
  if (!snaps.length) { c.innerHTML = `<p class="eyebrow">Restore</p><div class="sub">no snapshots to restore.</div>`; return; }
  const opts = snaps.map(s => `<option value="${esc(s.id)}">${esc(s.id.slice(0, 20))} · ${esc(s.tag || 'no tag')} · ${fmtBytes(s.bytes)}</option>`).join('');
  c.innerHTML = `<p class="eyebrow">Restore</p>
    <div class="startbar"><select id="rsnap">${opts}</select><input id="rdest" placeholder="destination folder (absolute path)"><button id="rgo" class="btn go">Restore</button></div>
    <div id="rprog"></div>`;
  $('#rgo').addEventListener('click', async () => {
    const id = $('#rsnap').value, dest = $('#rdest').value.trim();
    if (!dest) { $('#rprog').innerHTML = `<div class="err" style="margin-top:1rem">enter a destination folder</div>`; return; }
    if (!(await typedConfirm('restore', 'Restore snapshot?', `Writes the snapshot's files into <b>${esc(dest)}</b>, overwriting existing files there.`))) return;
    const box = progressBox($('#rprog'));
    let res;
    try { res = await api('/api/restore', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ snapshotId: id, dest, confirm: 'restore' }) }); }
    catch (err) { box.err(err.message); return; }
    streamSSE(res.opId, { onProgress: box.prog, onDone: d => box.ok(`<span class="ok">✓ Restored</span> to ${esc(d.dest)}`), onError: box.err });
  });
}

// ---------- prune ----------
async function viewPrune(c) {
  c.innerHTML = `<p class="eyebrow">Prune</p><div class="sub">computing retention…</div>`;
  let pv;
  try { pv = await api('/api/prune/preview'); }
  catch (err) { c.innerHTML = `<p class="eyebrow">Prune</p><div class="err">${esc(err.message)}</div>`; return; }
  const rows = pv.decisions.map(d => `<tr class="row"><td class="id">${esc(d.id.slice(0, 16))}</td><td>${esc(d.tag || '—')}</td><td>${d.keep ? '<span class="ok">keep</span>' : '<span class="err">drop</span>'}</td><td class="sub">${esc((d.reasons || []).join(', '))}</td></tr>`).join('');
  c.innerHTML = `<p class="eyebrow">Prune</p>
    <div class="sub" style="margin-bottom:.8rem">Retention: keep_last=${pv.policy.KeepLast} daily=${pv.policy.KeepDaily} weekly=${pv.policy.KeepWeekly} monthly=${pv.policy.KeepMonthly}. <b>${pv.dropCount}</b> snapshot(s) would be dropped.</div>
    <table><thead><tr><th>ID</th><th>Tag</th><th>Verdict</th><th>Why</th></tr></thead><tbody>${rows}</tbody></table>
    <div class="startbar"><button id="pgo" class="btn primary" ${pv.dropCount ? '' : 'disabled style="opacity:.5"'}>Prune ${pv.dropCount} snapshot(s)</button></div>
    <div id="pout"></div>`;
  const btn = $('#pgo'); if (!btn) return;
  btn.addEventListener('click', async () => {
    if (!(await typedConfirm('prune', 'Prune snapshots?', `Permanently deletes <b>${pv.dropCount}</b> snapshot(s) and their unreferenced data. This cannot be undone.`))) return;
    try {
      const r = await api('/api/prune', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ confirm: 'prune' }) });
      $('#pout').innerHTML = `<div class="ok" style="margin-top:1rem">✓ Pruned — ${r.deletedBlobs} blobs freed (${fmtBytes(r.deletedBytes)}), ${r.liveBlobs} retained</div>`;
    } catch (err) { $('#pout').innerHTML = `<div class="err" style="margin-top:1rem">${esc(err.message)}</div>`; }
  });
}

// ---------- password ----------
async function viewPassword(c) {
  c.innerHTML = `<p class="eyebrow">Rotate passphrase</p>
    <div style="display:flex;flex-direction:column;gap:.7rem;max-width:440px">
      <input id="np" type="password" placeholder="new passphrase (min 8)" autocomplete="new-password">
      <input id="cp" type="password" placeholder="confirm new passphrase" autocomplete="new-password">
      <button id="prot" class="btn primary">Rotate passphrase</button>
      <div id="pwout"></div>
    </div>`;
  $('#prot').addEventListener('click', async () => {
    const np = $('#np').value, cp = $('#cp').value;
    if (np.length < 8) { $('#pwout').innerHTML = `<div class="err">passphrase must be at least 8 characters</div>`; return; }
    if (np !== cp) { $('#pwout').innerHTML = `<div class="err">passphrases do not match</div>`; return; }
    if (!(await typedConfirm('rotate', 'Rotate passphrase?', `The <b>old passphrase stops working immediately</b> and there is no recovery if the new one is lost. Existing snapshots stay readable.`))) return;
    try {
      const r = await api('/api/password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ newPassphrase: np, confirmPassphrase: cp, confirm: 'rotate' }) });
      $('#np').value = ''; $('#cp').value = '';
      $('#pwout').innerHTML = r.warning ? `<div class="err">${esc(r.warning)}</div>` : `<div class="ok">✓ Passphrase rotated${r.keyringSaved ? ' · keyring updated' : ''}</div>`;
    } catch (err) { $('#pwout').innerHTML = `<div class="err">${esc(err.message)}</div>`; }
  });
}

// ---------- doctor ----------
async function viewDoctor(c) {
  c.innerHTML = `<p class="eyebrow">Doctor</p><div class="sub">running diagnostics…</div>`;
  let d;
  try { d = await api('/api/doctor'); }
  catch (err) { c.innerHTML = `<p class="eyebrow">Doctor</p><div class="err">${esc(err.message)}</div>`; return; }
  const badge = s => s === 'ok' ? '<span class="ok">✓</span>' : s === 'warn' ? '<span style="color:var(--gold)">▲</span>' : '<span class="err">✗</span>';
  const rows = d.checks.map(ck => `<tr class="row"><td style="width:2rem">${badge(ck.status)}</td><td>${esc(ck.label)}</td><td class="sub">${esc(ck.detail || '')}</td></tr>`).join('');
  c.innerHTML = `<p class="eyebrow">Doctor · ${esc(d.backend)} backend</p><table><tbody>${rows}</tbody></table>`;
}

// ---------- sync ----------
async function viewSync(c) {
  c.innerHTML = `<p class="eyebrow">Sync to a clone</p>
    <div class="sub" style="margin-bottom:.8rem">Replicate this repository to a destination described by another sentra.yaml. The destination becomes a working clone with the same passphrase.</div>
    <div style="display:flex;flex-direction:column;gap:.7rem;max-width:560px">
      <input id="sdst" placeholder="path to destination sentra.yaml (on this machine)">
      <label class="sub"><input id="sinit" type="checkbox"> initialize an empty destination (copy config first)</label>
      <label class="sub"><input id="sdry" type="checkbox"> dry run (list what would be copied, write nothing)</label>
      <button id="sgo" class="btn go" style="align-self:flex-start">Sync</button>
    </div>
    <div id="sprog"></div>`;
  $('#sgo').addEventListener('click', async () => {
    const path = $('#sdst').value.trim();
    if (!path) { $('#sprog').innerHTML = `<div class="err" style="margin-top:1rem">enter the destination sentra.yaml path</div>`; return; }
    const dry = $('#sdry').checked;
    if (!dry && !(await typedConfirm('sync', 'Sync to destination?', `Copies this repository (and its wrapped key) to the destination in <b>${esc(path)}</b>.`))) return;
    const box = progressBox($('#sprog'));
    let res;
    try { res = await api('/api/sync', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ dstConfigPath: path, initDest: $('#sinit').checked, dryRun: dry, confirm: 'sync' }) }); }
    catch (err) { box.err(err.message); return; }
    streamSSE(res.opId, {
      onProgress: box.prog,
      onDone: d => box.ok(`<span class="ok">✓ ${d.dryRun ? 'Dry run' : 'Synced'}</span> — ${d.copiedBlobs} copied (${fmtBytes(d.copiedBytes)}), ${d.skippedBlobs} already present${d.bootstrapped ? ' · destination bootstrapped' : ''}`),
      onError: box.err,
    });
  });
}

// ---------- policies ----------
async function viewPolicies(c) {
  const list = await api('/api/policies');
  const rows = list.map(p => `<tr class="row">
    <td>${esc(p.name)}${p.valid ? '' : ` <span class="err" title="${esc(p.error)}">⚠</span>`}</td>
    <td>${esc(p.scheduleSpec)}</td>
    <td class="sub">${esc(p.paths.join(', '))}</td>
    <td>${p.tags.length ? p.tags.map(t => `<span class="tag-pill">${esc(t)}</span>`).join(' ') : '—'}</td>
    <td>${p.check ? '✓' : '—'}</td>
    <td>${esc(p.prune)}</td>
    <td style="white-space:nowrap"><button class="btn" data-run="${esc(p.name)}" data-prune="${esc(p.prune)}">run</button> <button class="btn" data-del="${esc(p.name)}">delete</button></td>
  </tr>`).join('');
  c.innerHTML = `<p class="eyebrow">Backup policies</p>
    ${list.length ? `<table><thead><tr><th>Name</th><th>Schedule</th><th>Paths</th><th>Tags</th><th>Check</th><th>Prune</th><th></th></tr></thead><tbody>${rows}</tbody></table>` : '<div class="sub">no policies yet — add one below.</div>'}
    <div id="pol-out"></div>
    <div class="panel" style="margin-top:1.4rem"><h3>add a policy</h3>
      <div style="display:flex;flex-direction:column;gap:.6rem;max-width:600px;margin-top:.6rem">
        <input id="pn" placeholder="name (letters, digits, - and _)">
        <input id="pp" placeholder="paths — comma-separated absolute paths">
        <input id="pt" placeholder="tags — comma-separated (optional)">
        <input id="ps" value="manual" placeholder="schedule: manual | hourly | daily@HH:MM | weekly@mon:HH:MM | monthly@HH:MM">
        <label class="sub"><input id="pc" type="checkbox"> run an integrity check after each backup</label>
        <select id="ppr"><option value="off">prune: off</option><option value="dry-run">prune: dry-run</option><option value="apply">prune: apply</option></select>
        <button id="padd" class="btn go" style="align-self:flex-start">Add policy</button>
      </div>
    </div>`;
  c.querySelectorAll('[data-run]').forEach(b => b.addEventListener('click', () => runPolicy(b.dataset.run, b.dataset.prune)));
  c.querySelectorAll('[data-del]').forEach(b => b.addEventListener('click', () => deletePolicy(b.dataset.del)));
  $('#padd').addEventListener('click', addPolicy);
}
function splitCSV(v) { return v.split(',').map(s => s.trim()).filter(Boolean); }
async function addPolicy() {
  const body = {
    name: $('#pn').value.trim(),
    paths: splitCSV($('#pp').value),
    tags: splitCSV($('#pt').value),
    scheduleSpec: $('#ps').value.trim() || 'manual',
    check: $('#pc').checked,
    prune: $('#ppr').value,
  };
  try {
    await api('/api/policies', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    route();
  } catch (err) { $('#pol-out').innerHTML = `<div class="err" style="margin-top:1rem">${esc(err.message)}</div>`; }
}
async function deletePolicy(name) {
  if (!(await confirmSimple('Delete policy?', `Removes <b>${esc(name)}</b> from sentra.yaml. Snapshots it already created are untouched.`))) return;
  try { await api('/api/policies/' + encodeURIComponent(name), { method: 'DELETE' }); route(); }
  catch (err) { $('#pol-out').innerHTML = `<div class="err" style="margin-top:1rem">${esc(err.message)}</div>`; }
}
async function runPolicy(name, prune) {
  const needsConfirm = prune === 'apply';
  if (needsConfirm && !(await typedConfirm('run', 'Run policy?', `Backs up <b>${esc(name)}</b>'s paths, then <b>applies retention</b> (deletes snapshots that fall out of policy).`))) return;
  const box = progressBox($('#pol-out'));
  let res;
  try { res = await api('/api/policies/' + encodeURIComponent(name) + '/run', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ confirm: needsConfirm ? 'run' : '' }) }); }
  catch (err) { box.err(err.message); return; }
  streamSSE(res.opId, {
    onProgress: box.prog,
    onDone: d => box.ok(`<span class="ok">✓ ${d.snapshots} snapshot(s)</span>${d.checked ? ' · checked' : ''}${d.pruned ? ` · ${d.pruned} pruned` : ''}`),
    onError: box.err,
  });
}

// ---------- schedule ----------
async function viewSchedule(c) {
  const d = await api('/api/schedule');
  if (!d.policies.length) { c.innerHTML = `<p class="eyebrow">Schedule</p><div class="sub">no policies yet — add one under Policies first.</div>`; return; }
  const rows = d.policies.map(p => `<tr class="row">
    <td>${esc(p.policy)}</td>
    <td>${esc(p.spec)}</td>
    <td>${p.manual ? '<span class="sub">—</span>' : p.installed ? '<span class="ok">installed</span>' : '<span class="sub">not installed</span>'}</td>
    <td style="white-space:nowrap">${p.manual ? '' : `<button class="btn" data-prev="${esc(p.policy)}">preview</button> ${p.installed ? `<button class="btn" data-uninst="${esc(p.policy)}">uninstall</button>` : `<button class="btn" data-inst="${esc(p.policy)}">install</button>`}`}</td>
  </tr>`).join('');
  c.innerHTML = `<p class="eyebrow">Schedule · ${esc(d.os)}</p>
    <div class="sub" style="margin-bottom:.8rem">Installs an OS timer that runs <code>sentra policy run</code> on the policy's cadence. Manual policies have nothing to schedule.</div>
    <table><thead><tr><th>Policy</th><th>Schedule</th><th>State</th><th></th></tr></thead><tbody>${rows}</tbody></table>
    <div id="sch-out"></div>`;
  c.querySelectorAll('[data-prev]').forEach(b => b.addEventListener('click', () => previewSchedule(b.dataset.prev)));
  c.querySelectorAll('[data-inst]').forEach(b => b.addEventListener('click', () => installSchedule(b.dataset.inst, d.os)));
  c.querySelectorAll('[data-uninst]').forEach(b => b.addEventListener('click', () => uninstallSchedule(b.dataset.uninst)));
}
async function previewSchedule(name) {
  const out = $('#sch-out');
  try {
    const d = await api('/api/schedule/' + encodeURIComponent(name) + '/preview');
    out.innerHTML = d.files.map(f => `<div class="panel" style="margin-top:1rem"><h3 class="sub">${esc(f.path)}</h3><pre style="overflow-x:auto;white-space:pre;font-size:.8rem;margin-top:.4rem">${esc(f.body)}</pre></div>`).join('');
  } catch (err) { out.innerHTML = `<div class="err" style="margin-top:1rem">${esc(err.message)}</div>`; }
}
async function installSchedule(name, os) {
  if (!(await confirmSimple('Install schedule?', `Writes a <b>${esc(os)}</b> timer for <b>${esc(name)}</b> under your home directory. It runs the policy on its cadence.`))) return;
  try { await api('/api/schedule/' + encodeURIComponent(name) + '/install', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ confirm: true }) }); route(); }
  catch (err) { $('#sch-out').innerHTML = `<div class="err" style="margin-top:1rem">${esc(err.message)}</div>`; }
}
async function uninstallSchedule(name) {
  if (!(await confirmSimple('Uninstall schedule?', `Removes the scheduled timer for <b>${esc(name)}</b>. The policy itself stays.`))) return;
  try { await api('/api/schedule/' + encodeURIComponent(name) + '/uninstall', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ confirm: true }) }); route(); }
  catch (err) { $('#sch-out').innerHTML = `<div class="err" style="margin-top:1rem">${esc(err.message)}</div>`; }
}

// ---------- agent ----------
let agentRecs = [];
async function viewAgent(c) {
  const st = await api('/api/agent');
  c.innerHTML = `<p class="eyebrow">Agent</p>
    <div class="sub" style="margin-bottom:.8rem">Local heuristics scan your repository and files; ${st.llmConfigured ? 'the model triages the findings into recommendations' : 'no <code>ANTHROPIC_API_KEY</code> is set, so only the local-only scan is available'}. Nothing is applied until you approve it.</div>
    <div class="startbar">
      <input id="aroot" placeholder="root folder to scan (default: current directory)">
      <label class="sub" style="align-self:center"><input id="alocal" type="checkbox" ${st.llmConfigured ? '' : 'checked disabled'}> local-only</label>
      <button id="ascan" class="btn go">Scan</button>
    </div>
    <div id="areason" class="panel" style="margin-top:1rem;display:none"><h3 class="sub">reasoning</h3><pre id="areason-body" style="overflow-x:auto;white-space:pre-wrap;font-size:.82rem;margin-top:.4rem"></pre></div>
    <div id="arecs"></div>`;
  $('#ascan').addEventListener('click', () => scanAgent());
}
async function scanAgent() {
  agentRecs = [];
  const root = $('#aroot').value.trim();
  const localOnly = $('#alocal').checked;
  const reason = $('#areason'), body = $('#areason-body'), recsBox = $('#arecs');
  reason.style.display = ''; body.textContent = ''; recsBox.innerHTML = `<div class="sub" style="margin-top:1rem">scanning…</div>`;
  let res;
  try { res = await api('/api/agent/scan', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ root, localOnly }) }); }
  catch (err) { recsBox.innerHTML = `<div class="err" style="margin-top:1rem">${esc(err.message)}</div>`; return; }
  const ev = new EventSource('/api/op/' + res.opId + '/events');
  ev.addEventListener('token', e => { body.textContent += JSON.parse(e.data).text; body.scrollTop = body.scrollHeight; });
  ev.addEventListener('done', e => { ev.close(); agentRecs = JSON.parse(e.data).recommendations || []; renderRecs(); });
  ev.addEventListener('error', e => { ev.close(); recsBox.innerHTML = `<div class="err" style="margin-top:1rem">${esc(e.data ? JSON.parse(e.data).message : 'scan failed')}</div>`; });
}
function renderRecs() {
  const box = $('#arecs');
  if (!agentRecs.length) { box.innerHTML = `<div class="ok" style="margin-top:1rem">✓ no findings — all clear</div>`; return; }
  const sev = s => s === 'critical' ? 'err' : s === 'warn' ? '' : 'sub';
  const rows = agentRecs.map((r, i) => `<tr class="row">
    <td>${r.action === 'none' ? '' : `<input type="checkbox" data-i="${i}" ${r.action !== 'none' ? 'checked' : ''}>`}</td>
    <td class="${sev(r.severity)}">${esc(r.severity)}</td>
    <td>${esc(r.action)}</td>
    <td class="id">${esc(r.target)}</td>
    <td class="sub">${esc(r.rationale)}</td>
  </tr>`).join('');
  box.innerHTML = `<table style="margin-top:1rem"><thead><tr><th></th><th>Severity</th><th>Action</th><th>Target</th><th>Why</th></tr></thead><tbody>${rows}</tbody></table>
    <div class="startbar"><button id="aapply" class="btn primary">Apply selected</button></div>
    <div id="aout"></div>`;
  $('#aapply').addEventListener('click', () => applyAgent());
}
async function applyAgent() {
  const ids = [...document.querySelectorAll('#arecs input[type=checkbox]:checked')].map(cb => agentRecs[+cb.dataset.i].id);
  if (!ids.length) { $('#aout').innerHTML = `<div class="err" style="margin-top:1rem">select at least one recommendation</div>`; return; }
  if (!(await typedConfirm('apply', 'Apply recommendations?', `Runs <b>${ids.length}</b> approved action(s). Prune and ignore edits change your repository/config.`))) return;
  await doApplyAgent({ ids, confirm: 'apply' });
}
// doApplyAgent posts the apply; if the server refuses because it would empty the
// repo, it asks for a typed "wipe" and retries once — so the wipe prompt only
// appears when it's actually needed.
async function doApplyAgent(payload) {
  const box = progressBox($('#aout'));
  let res;
  try { res = await api('/api/agent/apply', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) }); }
  catch (err) {
    if (/wipe/i.test(err.message) && !payload.wipeConfirm) {
      if (await typedConfirm('wipe', 'Delete every snapshot?', `These actions would remove the <b>last</b> snapshot in the repository. Type "wipe" to authorize deleting everything.`)) {
        return doApplyAgent({ ...payload, wipeConfirm: 'wipe' });
      }
      box.err('cancelled — nothing was applied');
      return;
    }
    box.err(err.message);
    return;
  }
  streamSSE(res.opId, {
    onProgress: box.prog,
    onDone: d => {
      const okN = d.applied.filter(a => a.ok).length;
      box.ok(`<span class="ok">✓ ${okN} applied</span>${d.failed ? ` · <span class="err">${d.failed} failed</span>` : ''}`);
    },
    onError: box.err,
  });
}

// ---------- first-run setup wizard ----------
let SW = null;

async function bootSetup() {
  const st = await api('/api/setup');
  SW = {
    i: 0,
    backend: st.endpointLocked ? 's3-compatible' : (st.backend || 'aws'),
    endpointLocked: st.endpointLocked,
    awsCreds: st.awsCredentialsPresent,
    bucket: st.seed.bucket || '', prefix: st.seed.prefix || '', region: st.seed.region || '',
    profile: st.seed.profile || '', endpointUrl: st.seed.endpointUrl || '',
    createBucket: true, blockPublicAccess: true, defaultEncryption: true, initRepo: true,
    passphrase: '', confirmPass: '', savePassphrase: true,
  };
  $('#unlock').hidden = true; $('#app').hidden = true; $('#setup').hidden = false;
  swRender();
}
function swSteps() {
  const s = ['welcome', 'backend', 'details'];
  if (SW.backend === 'aws') s.push('actions');
  s.push('passphrase', 'review');
  return s;
}
function swForm() {
  return {
    backend: SW.backend, bucket: SW.bucket, prefix: SW.prefix, region: SW.region,
    profile: SW.profile, endpointUrl: SW.backend === 's3-compatible' ? SW.endpointUrl : '',
    createBucket: SW.createBucket, blockPublicAccess: SW.blockPublicAccess,
    defaultEncryption: SW.defaultEncryption, initRepo: SW.initRepo,
  };
}
function swRender() {
  const steps = swSteps();
  if (SW.i >= steps.length) SW.i = steps.length - 1;
  const fns = { welcome: swWelcome, backend: swBackend, details: swDetails, actions: swActions, passphrase: swPassphrase, review: swReview };
  fns[steps[SW.i]]($('#setup-card'));
}
function swNav(nextLabel) {
  const back = SW.i > 0 ? `<button class="btn" id="sw-back">Back</button>` : `<span></span>`;
  return `<div class="mrow" style="margin-top:1.4rem">${back}<button class="btn primary" id="sw-next">${esc(nextLabel || 'Continue')}</button></div>`;
}
function swWire(card, onNext) {
  const b = $('#sw-back', card); if (b) b.addEventListener('click', () => { SW.i--; swRender(); });
  $('#sw-next', card).addEventListener('click', onNext);
}
function swWelcome(card) {
  card.innerHTML = `<div class="logo big">✦ S E N T R A ✦</div>
    <p class="eyebrow" style="margin-top:1rem">First-run setup</p>
    <div class="sub" style="margin:.6rem 0 0;max-width:520px">Let's provision your encrypted, deduplicated backup repository. Sentra uses your machine's <b>existing</b> AWS credentials — it never asks for or stores your access keys.</div>
    ${swNav('Get started')}`;
  swWire(card, () => { SW.i++; swRender(); });
}
function swBackend(card) {
  const locked = SW.endpointLocked, dis = locked ? 'disabled' : '';
  card.innerHTML = `<p class="eyebrow">Storage backend</p>
    <div style="display:flex;flex-direction:column;gap:.6rem;margin-top:.8rem;max-width:520px">
      <label class="sw-opt"><input type="radio" name="be" value="aws" ${SW.backend === 'aws' ? 'checked' : ''} ${dis}> <span><b>AWS S3</b><br><span class="sub">provision a bucket in your AWS account</span></span></label>
      <label class="sw-opt"><input type="radio" name="be" value="s3-compatible" ${SW.backend === 's3-compatible' ? 'checked' : ''} ${dis}> <span><b>S3-compatible</b><br><span class="sub">MinIO, Cloudflare R2, Wasabi, or an existing bucket</span></span></label>
    </div>
    ${locked ? '<div class="sub" style="margin-top:.6rem">A seeded endpoint locks this to S3-compatible.</div>' : ''}
    ${swNav()}`;
  card.querySelectorAll('input[name=be]').forEach(r => r.addEventListener('change', () => { SW.backend = r.value; }));
  swWire(card, () => { SW.i++; swRender(); });
}
function swDetails(card) {
  const s3 = SW.backend === 's3-compatible';
  card.innerHTML = `<p class="eyebrow">Storage details</p>
    <div style="display:flex;flex-direction:column;gap:.6rem;margin-top:.8rem;max-width:520px">
      <input id="sw-bucket" placeholder="bucket name" value="${esc(SW.bucket)}">
      <div id="sw-err" class="err" hidden></div>
      <input id="sw-prefix" placeholder="prefix (optional, e.g. team/)" value="${esc(SW.prefix)}">
      <input id="sw-region" placeholder="region (e.g. us-east-1)" value="${esc(SW.region)}">
      <input id="sw-profile" placeholder="AWS profile (optional)" value="${esc(SW.profile)}">
      ${s3 ? `<input id="sw-endpoint" placeholder="endpoint URL (e.g. http://localhost:9000)" value="${esc(SW.endpointUrl)}">` : ''}
    </div>
    ${swNav()}`;
  swWire(card, async () => {
    SW.bucket = $('#sw-bucket').value.trim();
    SW.prefix = $('#sw-prefix').value.trim();
    SW.region = $('#sw-region').value.trim();
    SW.profile = $('#sw-profile').value.trim();
    if (s3) SW.endpointUrl = $('#sw-endpoint').value.trim();
    let v;
    try { v = await api('/api/setup/validate', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(swForm()) }); }
    catch (err) { const e = $('#sw-err'); e.textContent = err.message; e.hidden = false; return; }
    if (!v.ok) { const e = $('#sw-err'); e.textContent = v.error; e.hidden = false; return; }
    SW.i++; swRender();
  });
}
function swActions(card) {
  card.innerHTML = `<p class="eyebrow">Bucket setup</p>
    <div class="sub" style="margin:.4rem 0 .8rem;max-width:520px">Uses your machine's existing AWS credentials ${SW.awsCreds ? '<span class="ok">(detected ✓)</span>' : '(none detected — you may need to run <code>aws sso login</code> in your terminal, then retry)'}.</div>
    <div style="display:flex;flex-direction:column;gap:.5rem;max-width:520px">
      <label class="sw-opt"><input type="checkbox" id="sw-create" ${SW.createBucket ? 'checked' : ''}> create the bucket if it doesn't exist</label>
      <label class="sw-opt"><input type="checkbox" id="sw-block" ${SW.blockPublicAccess ? 'checked' : ''}> block all public access</label>
      <label class="sw-opt"><input type="checkbox" id="sw-enc" ${SW.defaultEncryption ? 'checked' : ''}> enable default encryption</label>
    </div>
    <button class="btn" id="sw-iam" style="margin-top:.8rem">Show IAM policy</button>
    <div id="sw-iam-out"></div>
    ${swNav()}`;
  $('#sw-iam', card).addEventListener('click', async () => {
    try {
      const r = await api('/api/setup/iam-policy?bucket=' + encodeURIComponent(SW.bucket) + '&prefix=' + encodeURIComponent(SW.prefix));
      $('#sw-iam-out').innerHTML = `<pre style="overflow-x:auto;font-size:.76rem;margin-top:.6rem">${esc(r.policy)}</pre>`;
    } catch (err) { $('#sw-iam-out').innerHTML = `<div class="err" style="margin-top:.6rem">${esc(err.message)}</div>`; }
  });
  swWire(card, () => {
    SW.createBucket = $('#sw-create').checked;
    SW.blockPublicAccess = $('#sw-block').checked;
    SW.defaultEncryption = $('#sw-enc').checked;
    SW.i++; swRender();
  });
}
function swPassphrase(card) {
  card.innerHTML = `<p class="eyebrow">Repository passphrase</p>
    <div class="sub" style="margin:.4rem 0 .8rem;max-width:520px">This encrypts everything. There is <b>no recovery</b> if you lose it — store it in a password manager.</div>
    <div style="display:flex;flex-direction:column;gap:.6rem;max-width:520px">
      <input id="sw-pass" type="password" placeholder="passphrase (min 8 characters)" autocomplete="new-password">
      <input id="sw-pass2" type="password" placeholder="confirm passphrase" autocomplete="new-password">
      <label class="sw-opt"><input type="checkbox" id="sw-keyring" ${SW.savePassphrase ? 'checked' : ''}> save it to my OS keyring so I don't re-type it each run</label>
      <div id="sw-err" class="err" hidden></div>
    </div>
    ${swNav('Review')}`;
  swWire(card, () => {
    SW.passphrase = $('#sw-pass').value;
    SW.confirmPass = $('#sw-pass2').value;
    SW.savePassphrase = $('#sw-keyring').checked;
    const e = $('#sw-err');
    if (SW.passphrase.length < 8) { e.textContent = 'passphrase must be at least 8 characters'; e.hidden = false; return; }
    if (SW.passphrase !== SW.confirmPass) { e.textContent = 'passphrases do not match'; e.hidden = false; return; }
    SW.i++; swRender();
  });
}
function swReview(card) {
  const f = swForm();
  const row = (k, v) => `<div class="sw-row"><span>${k}</span><span>${esc(v || '—')}</span></div>`;
  const prov = [f.createBucket && 'create', f.blockPublicAccess && 'block-public', f.defaultEncryption && 'encrypt'].filter(Boolean).join(', ');
  card.innerHTML = `<p class="eyebrow">Review</p>
    <div style="margin-top:.8rem;max-width:520px">
      ${row('backend', f.backend)}
      ${row('bucket', f.bucket)}
      ${row('prefix', f.prefix)}
      ${row('region', f.region)}
      ${row('profile', f.profile)}
      ${f.backend === 's3-compatible' ? row('endpoint', f.endpointUrl) : row('provision', prov)}
      ${row('keyring', SW.savePassphrase ? 'yes' : 'no')}
    </div>
    <div id="sw-prov"></div>
    ${swNav('Create repository')}`;
  swWire(card, () => swProvision());
  if (SW.provError) { $('#sw-prov').innerHTML = `<div class="err" style="margin-top:1rem;white-space:pre-wrap">${esc(SW.provError)}</div>`; SW.provError = ''; }
}
async function swProvision() {
  const out = $('#sw-prov');
  const next = $('#sw-next'), back = $('#sw-back');
  if (next) next.disabled = true;
  if (back) back.disabled = true;
  const labels = { 'bucket-created': 'Bucket created', 'public-blocked': 'Public access blocked', 'encrypted': 'Default encryption enabled', 'repo-initialized': 'Repository initialized' };
  const done = {};
  const keys = SW.backend === 'aws' ? Object.keys(labels) : ['repo-initialized'];
  const paint = () => { out.innerHTML = `<div style="margin-top:1rem">${keys.map(k => `<div>${done[k] ? '<span class="ok">✓</span>' : '<span class="sub">…</span>'} ${labels[k]}</div>`).join('')}</div>`; };
  paint();
  const body = { ...swForm(), passphrase: SW.passphrase, savePassphrase: SW.savePassphrase };
  let res;
  try { res = await api('/api/setup/apply', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }); }
  catch (err) { swProvError(err.message); return; }
  const ev = new EventSource('/api/op/' + res.opId + '/events');
  ev.addEventListener('token', e => { done[JSON.parse(e.data).text] = true; paint(); });
  ev.addEventListener('done', () => {
    ev.close(); SW.passphrase = SW.confirmPass = '';
    const ok = document.createElement('div');
    ok.className = 'ok'; ok.style.marginTop = '1rem';
    ok.textContent = '✓ Setup complete — loading your dashboard…';
    out.appendChild(ok);
    setTimeout(() => location.reload(), 900);
  });
  ev.addEventListener('error', e => { ev.close(); swProvError(e.data ? JSON.parse(e.data).message : 'setup failed'); });
}
// swProvError re-renders the Review step with the error shown. It does NOT
// mutate the shared nav button (that double-fired the apply) and keeps the
// passphrase so "Create repository" can be retried, or "Back" used to amend.
function swProvError(msg) {
  SW.provError = msg;
  SW.i = swSteps().indexOf('review');
  swRender();
}

boot().catch(err => { document.body.innerHTML = `<pre style="color:#FF6B86;padding:2rem">${esc(err.message)}</pre>`; });
