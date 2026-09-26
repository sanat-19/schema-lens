// SchemaLens UI: loads the schema, draws it, and keeps it in step with the
// database as the server reports changes.
//
// Everything that comes from the database (names, comments, SQL) is put on
// the page with textContent, never innerHTML: a table comment can contain
// anything.

import { createGraph, tableId, rowsText } from './graph.js';
import { connectLive } from './live.js';

const $ = (id) => document.getElementById(id);

const state = {
  schema: null,
  version: null,     // version of the schema we're showing
  selected: null,    // table ID
  query: '',
  loading: null,     // the fetch in flight, so events don't pile up requests
  wantVersion: null, // newest version the server told us about
};

const graph = createGraph($('graph'), {
  onSelect: (id) => selectTable(id, { focus: false }),
  onClear: () => clearSelection(),
});

// --- loading ----------------------------------------------------------------

async function fetchSchema({ refresh = false } = {}) {
  const res = await fetch('/api/schema' + (refresh ? '?refresh=1' : ''));
  if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
  const version = Number(res.headers.get('X-Schema-Version'));
  applySchema(await res.json(), version);
}

// catchUp fetches the schema if the server has a newer version than the one
// on screen. If a fetch is already running, it runs once more afterwards.
async function catchUp() {
  if (state.loading) return;
  while (state.wantVersion !== null && state.wantVersion !== state.version) {
    state.loading = fetchSchema().catch((err) => toast(`Couldn't load the schema: ${err.message}`));
    await state.loading;
    state.loading = null;
    if (state.wantVersion !== state.version) await sleep(500); // server moved on meanwhile
  }
}

function applySchema(schema, version) {
  const first = state.schema === null;
  state.schema = schema;
  state.version = version;

  if (first && schema.tables.length > 60) graph.setShowColumns(false); // keep big schemas readable
  const rememberedMode = load('showColumns');
  if (first && rememberedMode !== null) graph.setShowColumns(rememberedMode === 'true');
  syncModeButtons();

  const changes = graph.update(schema);

  renderHeader();
  renderTableList();
  renderFindings();
  applySearch();

  if (state.selected && !schema.tables.some((t) => tableId(t) === state.selected)) {
    closeDetails(); // the selected table was dropped
  } else if (state.selected) {
    graph.select(state.selected);
    renderDetails(state.selected);
  }

  $('empty').hidden = schema.tables.length > 0;
  $('empty').textContent = 'No tables in the selected schemas yet. Create one and it will appear here.';

  if (!first) announce(changes);
}

// announce tells the user what just changed in the database.
function announce({ added, removed, changed }) {
  const parts = [];
  if (added.length) parts.push(`${plural(added.length, 'table')} added`);
  if (removed.length) parts.push(`${plural(removed.length, 'table')} dropped`);
  if (changed.length) parts.push(`${plural(changed.length, 'table')} changed`);
  if (parts.length) toast('Schema updated: ' + parts.join(', '));
}

// --- sidebar ----------------------------------------------------------------

function renderHeader() {
  const s = state.schema;
  $('db-name').textContent = s.database;
  $('db-meta').textContent = `PostgreSQL ${s.serverVersion.split(' ')[0]} · ${s.schemas.join(', ')} · read ${time(s.capturedAt)}`;
}

function renderStatus(status) {
  const dot = $('status-dot');
  const text = $('status-text');
  dot.className = 'dot ' + status.state;
  text.title = status.error || '';
  switch (status.state) {
    case 'live':
      text.textContent = `Live · last change ${time(status.changedAt)}`;
      break;
    case 'reconnecting':
      text.textContent = 'Database unreachable, retrying…';
      break;
    case 'offline':
      text.textContent = 'SchemaLens server unreachable, retrying…';
      break;
    case 'snapshot':
      text.textContent = 'Snapshot (not live)';
      break;
  }
}

function renderTableList() {
  const severity = worstSeverity();
  const list = $('table-list');
  list.replaceChildren(...state.schema.tables.map((t) => {
    const id = tableId(t);
    const li = el('li', { dataset: { id }, title: t.comment || id, onclick: () => selectTable(id, { focus: true }) },
      el('span', { className: 'sev-dot ' + (severity.get(id) || '') }),
      el('span', { className: 'name' },
        t.schema === 'public' ? '' : el('span', { className: 'schema' }, t.schema + '.'),
        t.name),
      el('span', { className: 'stats' }, `${rowsText(t)} · ${bytes(t.totalBytes)}`));
    li.classList.toggle('selected', id === state.selected);
    return li;
  }));
  $('table-count').textContent = state.schema.tables.length;
}

function renderFindings() {
  const findings = state.schema.findings || [];
  $('finding-count').textContent = findings.length;
  const box = $('findings');
  if (!findings.length) {
    box.replaceChildren(el('p', { className: 'no-findings' }, 'No structural problems found.'));
    return;
  }
  const groups = ['high', 'medium', 'low'].map((sev) => {
    const items = findings.filter((f) => f.severity === sev);
    if (!items.length) return '';
    return el('div', { className: 'sev-group' },
      el('h3', {}, el('span', { className: 'sev-dot ' + sev }), `${capitalize(sev)} (${items.length})`),
      ...items.map((f) => findingCard(f, { showTable: true })));
  });
  box.replaceChildren(...groups);
}

// findingCard shows one finding. Opening it in the sidebar also jumps to
// its table.
function findingCard(f, { showTable }) {
  const summary = el('summary', {},
    f.title,
    showTable ? el('span', { className: 'where' }, f.table) : '');
  const card = el('details', { className: 'finding ' + f.severity },
    summary,
    el('div', { className: 'body' },
      el('p', {}, f.detail),
      f.suggestion ? el('pre', {}, f.suggestion) : '',
      f.suggestion ? el('button', { type: 'button', onclick: () => copy(f.suggestion) }, 'Copy SQL') : ''));
  if (showTable) card.addEventListener('toggle', () => { if (card.open) selectTable(f.table, { focus: true }); });
  return card;
}

// --- search -----------------------------------------------------------------

function applySearch() {
  const q = state.query.trim().toLowerCase();
  const matches = q ? new Set(state.schema.tables.map(tableId).filter((id) => id.toLowerCase().includes(q))) : null;
  graph.search(matches);
  for (const li of $('table-list').children) {
    li.hidden = matches !== null && !matches.has(li.dataset.id);
  }
}

// --- selection and details ---------------------------------------------------

function selectTable(id, { focus }) {
  state.selected = id;
  graph.select(id);
  for (const li of $('table-list').children) li.classList.toggle('selected', li.dataset.id === id);
  renderDetails(id);
  if (focus) graph.focus(id); // after the panel opens, so we centre in the space that's left
}

function clearSelection() {
  state.selected = null;
  graph.clearSelection();
  for (const li of $('table-list').children) li.classList.remove('selected');
  $('details').hidden = true;
}

function closeDetails() {
  clearSelection();
}

function renderDetails(id) {
  const s = state.schema;
  const t = s.tables.find((x) => tableId(x) === id);
  if (!t) return;

  const findings = (s.findings || []).filter((f) => f.table === id);
  const indexTags = new Map();
  for (const f of findings) {
    if (f.index) indexTags.set(f.index, f.kind.replace('_index', ''));
  }
  const outgoing = (s.relations || []).filter((r) => r.from === id);
  const incoming = (s.relations || []).filter((r) => r.to === id && r.from !== id);
  const loaded = new Set(s.tables.map(tableId));
  const external = (t.foreignKeys || []).filter((fk) => !loaded.has(`${fk.refSchema}.${fk.refTable}`));

  const body = [
    el('h2', {}, id),
    t.comment ? el('p', { className: 'comment muted' }, t.comment) : '',
    el('div', { className: 'badges' },
      el('span', { className: 'badge' }, rowsText(t)),
      el('span', { className: 'badge' }, bytes(t.totalBytes)),
      t.partitioned ? el('span', { className: 'badge' }, 'partitioned') : '',
      (t.primaryKey || []).length ? '' : el('span', { className: 'badge bad' }, 'no primary key')),

    el('h3', {}, `Columns (${t.columns.length})`),
    columnsTable(t),

    el('h3', {}, `Indexes (${(t.indexes || []).length})`),
    ...(t.indexes || []).map((ix) => indexCard(ix, indexTags.get(ix.name))),
    (t.indexes || []).length ? '' : el('p', { className: 'muted small' }, 'No indexes.'),

    el('h3', {}, 'References'),
    linkList([
      ...outgoing.map((r) => relationLink(r.to, r, 'to')),
      ...external.map((fk) => el('li', {},
        el('code', {}, `${fk.refSchema}.${fk.refTable}`), ` (${fk.columns.join(', ')}) `,
        el('span', { className: 'badge' }, 'external, schema not loaded'))),
    ], 'Doesn’t reference other tables.'),

    el('h3', {}, 'Referenced by'),
    linkList(incoming.map((r) => relationLink(r.from, r, 'from')), 'No other table references this one.'),

    el('h3', {}, `Findings (${findings.length})`),
    ...findings.map((f) => findingCard(f, { showTable: false })),
    findings.length ? '' : el('p', { className: 'no-findings' }, 'Nothing to fix here.'),
  ];

  $('details-body').replaceChildren(...body);
  $('details').hidden = false;
}

function columnsTable(t) {
  return el('table', { className: 'columns' },
    el('thead', {}, el('tr', {}, ...['Name', 'Type', 'Null', ''].map((h) => el('th', {}, h)))),
    el('tbody', {}, ...t.columns.map((c) =>
      el('tr', { className: c.nullable ? 'nullable' : '', title: c.comment || '' },
        // Defaults can be long (nextval(...)), so they sit under the name.
        el('td', { className: 'col-name' }, c.name, c.default ? el('span', { className: 'default' }, '= ' + c.default) : ''),
        el('td', { className: 'type' }, c.type),
        el('td', {}, c.nullable ? 'yes' : ''),
        el('td', { className: 'flags' },
          [c.isPK && '🔑', c.isFK && '🔗', c.isUnique && !c.isPK && 'U'].filter(Boolean).join(' '))))));
}

function indexCard(ix, tag) {
  const kinds = [ix.primary && 'primary', ix.unique && !ix.primary && 'unique', ix.partial && 'partial',
    ix.method !== 'btree' && ix.method].filter(Boolean);
  const scans = ix.scans < 0 ? 'scans unknown' : ix.scans === 0 ? 'never scanned' : `${ix.scans.toLocaleString()} scans`;
  return el('div', { className: 'index' },
    el('div', { className: 'index-head' },
      el('span', { className: 'index-name' }, ix.name),
      ...kinds.map((k) => el('span', { className: 'badge' }, k)),
      tag ? el('span', { className: 'badge warn' }, tag) : ''),
    el('code', {}, ix.definition),
    el('div', { className: 'index-stats' }, `${bytes(ix.bytes)} · ${scans}`));
}

function relationLink(target, r, direction) {
  const card = `${r.cardinality === 'one-to-one' ? '1' : 'N'}:${r.optional ? '0..1' : '1'}`;
  return el('li', {},
    direction === 'to' ? '→ ' : '← ',
    el('button', { type: 'button', className: 'jump', onclick: () => selectTable(target, { focus: true }) }, target),
    ` (${r.fromCols.join(', ')}) ${card} `, // FK columns always live on the child
    r.inferred ? el('span', { className: 'badge warn' }, 'guessed, no FK') : '');
}

function linkList(items, emptyText) {
  return items.length ? el('ul', { className: 'links' }, ...items) : el('p', { className: 'muted small' }, emptyText);
}

// --- toolbar ------------------------------------------------------------------

function syncModeButtons() {
  $('mode-columns').classList.toggle('on', graph.showColumns);
  $('mode-names').classList.toggle('on', !graph.showColumns);
}

function setMode(showColumns) {
  graph.setShowColumns(showColumns);
  save('showColumns', String(showColumns));
  syncModeButtons();
}

$('mode-names').onclick = () => setMode(false);
$('mode-columns').onclick = () => setMode(true);
$('layout').onchange = (e) => graph.setLayout(e.target.value);
$('fit').onclick = () => graph.fit();
$('png').onclick = async () => {
  const blob = await graph.png();
  const a = el('a', { href: URL.createObjectURL(blob), download: `${state.schema?.database || 'schema'}.png` });
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 10_000);
};
$('refresh').onclick = async () => {
  $('refresh').disabled = true;
  try {
    await fetchSchema({ refresh: true });
    toast('Re-read the database');
  } catch (err) {
    toast(`Refresh failed: ${err.message}`);
  } finally {
    $('refresh').disabled = false;
  }
};
$('close-details').onclick = closeDetails;
$('search').oninput = (e) => { state.query = e.target.value; applySearch(); };

document.addEventListener('keydown', (e) => {
  const typing = e.target.matches('input, select, textarea');
  if (e.key === '/' && !typing) {
    e.preventDefault();
    $('search').focus();
  } else if (e.key === 'Escape') {
    if (typing && state.query) {
      e.target.value = state.query = '';
      applySearch();
    } else {
      closeDetails();
    }
  }
});

// --- small helpers -------------------------------------------------------------

// el builds a DOM element. Children that are strings become text nodes, so
// nothing from the database is ever parsed as HTML.
function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === 'dataset') Object.assign(node.dataset, v);
    else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else node[k] = v;
  }
  node.append(...children.filter((c) => c !== '' && c != null && c !== false));
  return node;
}

function worstSeverity() {
  const rank = { high: 3, medium: 2, low: 1 };
  const worst = new Map();
  for (const f of state.schema.findings || []) {
    if ((rank[f.severity] || 0) > (rank[worst.get(f.table)] || 0)) worst.set(f.table, f.severity);
  }
  return worst;
}

async function copy(text) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // Clipboard API needs a secure context; fall back for http://<lan-ip>.
    const area = el('textarea', { value: text });
    document.body.append(area);
    area.select();
    document.execCommand('copy');
    area.remove();
  }
  toast('SQL copied');
}

let toastTimer;
function toast(message) {
  const box = $('toast');
  box.textContent = message;
  box.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { box.hidden = true; }, 2600);
}

function bytes(n) {
  if (n < 1024) return `${n} B`;
  const units = ['kB', 'MB', 'GB', 'TB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
  return `${n.toFixed(n < 10 ? 1 : 0)} ${units[i]}`;
}

function time(iso) {
  return iso ? new Date(iso).toLocaleTimeString() : '';
}

const capitalize = (s) => s[0].toUpperCase() + s.slice(1);
const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Per-browser preferences only; the page works fine without storage.
function load(key) {
  try { return localStorage.getItem('schemalens.' + key); } catch { return null; }
}
function save(key, value) {
  try { localStorage.setItem('schemalens.' + key, value); } catch { /* private mode */ }
}

// --- start ---------------------------------------------------------------------

fetchSchema().catch((err) => {
  $('empty').hidden = false;
  $('empty').textContent = `Couldn't load the schema: ${err.message}`;
});

connectLive({
  onStatus: renderStatus,
  onNewVersion: (version) => {
    state.wantVersion = version;
    catchUp();
  },
});
