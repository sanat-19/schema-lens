// The relationship graph: one node per table, one edge per relation,
// pointing from the child table (the one holding the FK) to its parent.
//
// Cytoscape can't lay out HTML inside a node, so each table is drawn as a
// small SVG "ER card" (header plus one row per column) and used as the
// node's background image. In "names only" mode nodes are just labels,
// which is what keeps a 200-table schema fast and readable.

import { schemaColors, mix } from './colors.js';

cytoscape.use(cytoscapeDagre);

const MONO = 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace';
const SANS = 'system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';
const ROW_H = 18;
const HEADER_H = 30;
const PAD = 10;
const MARKER_W = 34;        // room for "🔑🔗"
const MAX_ROWS = 30;        // longer tables show "… N more columns"

const LAYOUTS = {
  dagre: { name: 'dagre', rankDir: 'LR', nodeSep: 28, rankSep: 90, edgeSep: 12, padding: 30 },
  cose: { name: 'cose', idealEdgeLength: 140, nodeRepulsion: 14000, padding: 30, animate: false },
};

export function createGraph(container, { onSelect, onClear }) {
  const cy = cytoscape({
    container,
    minZoom: 0.05,
    maxZoom: 3,
    boxSelectionEnabled: false,
    autounselectify: true, // we manage "selected" ourselves
  });

  let showColumns = true;
  let layoutName = 'dagre';
  let loaded = false;
  let theme = readTheme();
  let colors = new Map(); // schema name → colour, for the schemas on screen
  cy.style(stylesheet(theme));

  // look is how one table is drawn right now: theme, mode and schema colour.
  const look = (table) => nodeLook(table, theme, showColumns, colors.get(table.schema));

  // Opening the details panel narrows the graph; Cytoscape has to be told.
  new ResizeObserver(() => cy.resize()).observe(container);

  cy.on('tap', 'node', (e) => onSelect(e.target.id()));
  cy.on('dbltap', () => onClear());

  // Follow the OS theme: the SVG cards have colours baked in, so redraw them.
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    theme = readTheme();
    cy.style(stylesheet(theme));
    cy.nodes().forEach((n) => n.data(look(n.data('table'))));
  });

  // update brings the graph in line with a new schema. The first time it
  // lays everything out (or puts tables back where a saved graph had them);
  // after that it patches: new tables fade in next to their neighbours,
  // dropped ones fade out, changed ones flash, and nothing that already
  // exists moves.
  function update(schema, { positions } = {}) {
    const severity = worstSeverityByTable(schema.findings || []);
    colors = schemaColors(schema.tables.map((t) => t.schema));
    const tables = new Map(schema.tables.map((t) => [tableId(t), t]));
    const relations = new Map((schema.relations || []).map((r) => [r.id, r]));
    const added = [];
    const changed = [];
    const removed = [];

    cy.batch(() => {
      cy.edges().forEach((e) => { if (!relations.has(e.id())) e.remove(); });
      cy.nodes().forEach((n) => { if (!tables.has(n.id())) removed.push(n); });

      for (const [id, table] of tables) {
        // The colour is part of the signature: a new schema can shift colours.
        const sig = JSON.stringify(table) + '|' + (severity.get(id) || '') + '|' + (colors.get(table.schema) || '');
        const node = cy.getElementById(id);
        const data = { table, sig, ...look(table) };
        if (node.empty()) {
          added.push(cy.add({ group: 'nodes', data: { id, ...data } }));
        } else if (node.data('sig') !== sig) {
          node.data(data);
          changed.push(node);
        }
        // Toggle only the classes we own; selection and search set others.
        const n = cy.getElementById(id);
        n.toggleClass('sev-high', severity.get(id) === 'high');
        n.toggleClass('sev-medium', severity.get(id) === 'medium');
      }

      for (const [id, rel] of relations) {
        if (!tables.has(rel.from) || !tables.has(rel.to)) continue;
        const edge = cy.getElementById(id);
        if (edge.empty()) {
          cy.add({ group: 'edges', data: { id, source: rel.from, target: rel.to, label: edgeLabel(rel) } });
        } else {
          edge.data('label', edgeLabel(rel));
          if (edge.data('source') !== rel.from || edge.data('target') !== rel.to) {
            edge.move({ source: rel.from, target: rel.to }); // FK re-pointed, same name
          }
        }
        cy.getElementById(id)
          .toggleClass('inferred', rel.inferred)
          .toggleClass('loop', rel.from === rel.to);
      }
    });

    if (!loaded) {
      loaded = true;
      if (positions && Object.keys(positions).length) {
        restore(positions);
      } else {
        runLayout();
      }
      return { added: [], changed: [], removed: [] };
    }

    for (const n of removed) {
      n.animate({ style: { opacity: 0 } }, { duration: 400, complete: () => n.remove() });
    }
    placeNear(added);
    for (const n of added) {
      n.style('opacity', 0);
      n.animate({ style: { opacity: 1 } }, { duration: 400 });
    }
    flash([...added, ...changed]);

    return {
      added: added.map((n) => n.id()),
      changed: changed.map((n) => n.id()),
      removed: removed.map((n) => n.id()),
    };
  }

  // placeNear puts each new table where the layout would have: a child to
  // the left of the table it references (edges run child → parent, left to
  // right), a parent to the right. It tries spots up and down that column,
  // then the other side, and takes the first free one. A table with no
  // relations goes to the right of everything.
  function placeNear(nodes) {
    const isNew = new Set(nodes.map((n) => n.id()));
    for (const node of nodes) {
      const spots = [];
      const friend = node.neighborhood('node').filter((n) => !isNew.has(n.id()))[0];
      if (friend) {
        const gap = friend.width() / 2 + node.width() / 2 + 90;
        const sides = node.edgesTo(friend).length ? [-1, 1] : [1, -1];
        for (const side of sides) {
          for (let k = 0; k < 12; k++) {
            const dy = (k % 2 ? 1 : -1) * Math.ceil(k / 2) * 70; // 0, +70, -70, +140, ...
            spots.push({ x: friend.position('x') + side * gap, y: friend.position('y') + dy });
          }
        }
      }
      const others = cy.nodes().filter((n) => n !== node && !isNew.has(n.id()));
      const bb = others.length ? others.boundingBox() : { x2: 0, y1: 0 };
      spots.push({ x: bb.x2 + node.width() / 2 + 80, y: bb.y1 + node.height() / 2 });

      node.position(spots.find((p) => isFree(node, p)) || spots.at(-1));
    }
  }

  // isFree reports whether node, put at pos, keeps 20px clear of every other node.
  function isFree(node, pos) {
    const w = node.width() / 2 + 20;
    const h = node.height() / 2 + 20;
    return !cy.nodes().some((n) => {
      if (n === node) return false;
      const b = n.boundingBox();
      return pos.x - w < b.x2 && pos.x + w > b.x1 && pos.y - h < b.y2 && pos.y + h > b.y1;
    });
  }

  // restore puts tables where a saved graph had them. Tables the saved
  // layout doesn't know about go next to their neighbours, as for a live
  // update.
  function restore(positions) {
    const unknown = [];
    cy.nodes().forEach((n) => {
      const p = positions[n.id()];
      if (p) n.position({ x: p.x, y: p.y });
      else unknown.push(n);
    });
    placeNear(unknown);
    cy.fit(undefined, 30);
    container.dataset.layoutMs = 0;
  }

  // reset clears the graph for a different database or saved graph, so the
  // next update starts from scratch instead of patching.
  function reset() {
    cy.elements().remove();
    loaded = false;
  }

  // positions reports where every table is now, for saving.
  function positions() {
    const out = {};
    cy.nodes().forEach((n) => {
      out[n.id()] = { x: Math.round(n.position('x')), y: Math.round(n.position('y')) };
    });
    return out;
  }

  function flash(nodes) {
    if (!nodes.length) return;
    const all = cy.collection(nodes);
    all.addClass('flash');
    setTimeout(() => all.removeClass('flash'), 1600);
  }

  function runLayout() {
    const started = performance.now();
    const layout = cy.layout({ ...LAYOUTS[layoutName], fit: true });
    layout.one('layoutstop', () => {
      const ms = Math.round(performance.now() - started);
      container.dataset.layoutMs = ms; // read by the performance check
      console.debug(`SchemaLens: ${layoutName} layout of ${cy.nodes().length} tables took ${ms} ms`);
    });
    layout.run();
  }

  // --- selection and search -------------------------------------------------

  function select(id) {
    const node = cy.getElementById(id);
    if (node.empty()) return;
    cy.elements().removeClass('dim highlight selected');
    cy.elements().not(node.closedNeighborhood()).addClass('dim');
    node.connectedEdges().addClass('highlight');
    node.addClass('selected');
  }

  function clearSelection() {
    cy.elements().removeClass('dim highlight selected');
  }

  function focus(id) {
    const node = cy.getElementById(id);
    if (node.empty()) return;
    cy.resize();
    cy.animate({ center: { eles: node }, zoom: Math.max(cy.zoom(), 0.8) }, { duration: 300 });
  }

  function search(matches) {
    cy.batch(() => {
      cy.nodes().forEach((n) => n.toggleClass('search-miss', matches !== null && !matches.has(n.id())));
      cy.edges().forEach((e) => e.toggleClass('search-miss',
        e.source().hasClass('search-miss') || e.target().hasClass('search-miss')));
    });
  }

  // --- view controls ----------------------------------------------------------

  function setShowColumns(on) {
    if (on === showColumns) return;
    showColumns = on;
    cy.batch(() => cy.nodes().forEach((n) => n.data(look(n.data('table')))));
    if (loaded) runLayout(); // node sizes changed a lot
  }

  function setLayout(name) {
    layoutName = name;
    if (loaded) runLayout();
  }

  function png() {
    return cy.png({ output: 'blob', full: true, scale: 2, bg: theme.bg });
  }

  return {
    update, reset, positions, select, clearSelection, focus, search, setShowColumns, setLayout, png,
    fit: () => cy.animate({ fit: { padding: 30 } }, { duration: 300 }),
    get showColumns() { return showColumns; },
  };
}

// --- how things look ----------------------------------------------------------

export function tableId(t) {
  return `${t.schema}.${t.name}`;
}

function readTheme() {
  const css = getComputedStyle(document.documentElement);
  const v = (name) => css.getPropertyValue(name).trim();
  return {
    bg: v('--bg'), text: v('--text'), muted: v('--muted'), accent: v('--accent'),
    high: v('--high'), medium: v('--medium'),
    nodeBg: v('--node-bg'), nodeHeader: v('--node-header'), nodeBorder: v('--node-border'),
    edge: v('--edge'),
  };
}

function stylesheet(t) {
  return [
    {
      selector: 'node',
      style: {
        shape: 'round-rectangle',
        width: 'data(w)',
        height: 'data(h)',
        'background-color': 'data(bg)',
        'background-image': 'data(image)',
        'background-fit': 'none',
        'background-clip': 'node',
        'background-width': 'data(w)',
        'background-height': 'data(h)',
        'background-image-smoothing': 'yes',
        'border-width': 1,
        'border-color': 'data(border)',
        label: 'data(text)',
        color: t.text,
        'font-family': SANS,
        'font-size': 13,
        'font-weight': 600,
        'text-valign': 'center',
        'text-halign': 'center',
        'transition-property': 'opacity, border-color, border-width',
        'transition-duration': 250,
      },
    },
    { selector: 'node.sev-high', style: { 'border-color': t.high, 'border-width': 3 } },
    { selector: 'node.sev-medium', style: { 'border-color': t.medium, 'border-width': 3 } },
    { selector: 'node.selected', style: { 'border-color': t.accent, 'border-width': 3 } },
    { selector: 'node.flash', style: { 'border-color': t.accent, 'border-width': 6 } },
    {
      selector: 'edge',
      style: {
        width: 1.5,
        'curve-style': 'bezier',
        'line-color': t.edge,
        'target-arrow-color': t.edge,
        'target-arrow-shape': 'triangle',
        'arrow-scale': 0.9,
        label: 'data(label)',
        'font-family': MONO,
        'font-size': 10,
        color: t.muted,
        'text-background-color': t.bg,
        'text-background-opacity': 0.85,
        'text-background-padding': 2,
        'text-background-shape': 'round-rectangle',
      },
    },
    { selector: 'edge.inferred', style: { 'line-style': 'dashed', 'line-dash-pattern': [6, 4] } },
    // Self-references (categories.parent_id) loop out of the top edge; aimed
    // at a corner, the loop can end up inside a big card and not be drawn.
    { selector: 'edge.loop', style: { 'loop-direction': '0deg', 'loop-sweep': '-40deg', 'control-point-step-size': 80 } },
    { selector: 'edge.highlight', style: { 'line-color': t.accent, 'target-arrow-color': t.accent, width: 2.5, color: t.text } },
    { selector: '.dim, .search-miss', style: { opacity: 0.12 } },
  ];
}

// nodeLook returns the node's size, picture and colours for the current
// mode. A table in a coloured schema gets a clear tint on its header (or on
// the whole box in names-only mode), a soft tint on the body, and a border
// in the same colour; findings still override the border.
function nodeLook(table, t, showColumns, color) {
  const tint = color
    ? { header: mix(color, t.nodeBg, 0.32), body: mix(color, t.nodeBg, 0.1), border: mix(color, t.nodeBg, 0.6) }
    : { header: t.nodeHeader, body: t.nodeBg, border: t.nodeBorder };

  if (!showColumns) {
    const text = table.schema === 'public' ? table.name : tableId(table);
    return { text, image: 'none', w: textWidth(text, `600 13px ${SANS}`) + 28, h: 34,
      bg: color ? tint.header : t.nodeBg, border: tint.border };
  }
  const card = tableCard(table, t, tint.header);
  return { text: '', image: card.url, w: card.width, h: card.height, bg: tint.body, border: tint.border };
}

// tableCard draws one table as an SVG: a header with the name and row count,
// then a row per column with its PK/FK/unique markers and type.
function tableCard(table, t, headerColor) {
  const title = table.schema === 'public' ? table.name : tableId(table);
  const meta = rowsText(table);
  const headerFont = `600 13px ${SANS}`;
  const rowFont = `12px ${MONO}`;
  const metaFont = `11px ${SANS}`;

  let rows = table.columns.map((c) => ({
    marker: (c.isPK ? '🔑' : '') + (c.isFK ? '🔗' : ''),
    name: c.name,
    unique: c.isUnique && !c.isPK,
    type: shortType(c.type) + (c.nullable ? '?' : ''),
    nullable: c.nullable,
  }));
  let more = 0;
  if (rows.length > MAX_ROWS) {
    more = rows.length - MAX_ROWS;
    rows = rows.slice(0, MAX_ROWS);
  }

  const nameW = Math.max(0, ...rows.map((r) => textWidth(r.name, rowFont) + (r.unique ? 16 : 0)));
  const typeW = Math.max(0, ...rows.map((r) => textWidth(r.type, rowFont)));
  const width = Math.ceil(Math.max(
    PAD + MARKER_W + nameW + 18 + typeW + PAD,
    PAD + textWidth(title, headerFont) + 16 + textWidth(meta, metaFont) + PAD,
    140,
  ));
  const height = HEADER_H + (rows.length + (more ? 1 : 0)) * ROW_H + 6;

  const parts = [
    `<rect width="${width}" height="${HEADER_H}" fill="${headerColor}"/>`,
    `<text x="${PAD}" y="20" font-family='${SANS}' font-size="13" font-weight="600" fill="${t.text}">${esc(title)}</text>`,
    `<text x="${width - PAD}" y="20" text-anchor="end" font-family='${SANS}' font-size="11" fill="${t.muted}">${esc(meta)}</text>`,
  ];
  rows.forEach((r, i) => {
    const y = HEADER_H + i * ROW_H + 15;
    const nameColor = r.nullable ? t.muted : t.text;
    parts.push(`<text x="${PAD}" y="${y}" font-size="11">${r.marker}</text>`);
    parts.push(`<text x="${PAD + MARKER_W}" y="${y}" font-family='${MONO}' font-size="12" fill="${nameColor}">${esc(r.name)}</text>`);
    if (r.unique) {
      const x = PAD + MARKER_W + textWidth(r.name, rowFont) + 5;
      parts.push(`<text x="${x}" y="${y}" font-family='${SANS}' font-size="10" font-weight="700" fill="${t.accent}">U</text>`);
    }
    parts.push(`<text x="${width - PAD}" y="${y}" text-anchor="end" font-family='${MONO}' font-size="12" fill="${t.muted}">${esc(r.type)}</text>`);
  });
  if (more) {
    const y = HEADER_H + rows.length * ROW_H + 15;
    parts.push(`<text x="${PAD + MARKER_W}" y="${y}" font-family='${SANS}' font-size="11" font-style="italic" fill="${t.muted}">… ${more} more columns</text>`);
  }

  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}">${parts.join('')}</svg>`;
  return { url: 'data:image/svg+xml;utf8,' + encodeURIComponent(svg), width, height };
}

// "user_id · N:1": the FK columns, then how many children per parent and
// whether the parent is required (1) or optional (0..1).
function edgeLabel(rel) {
  const many = rel.cardinality === 'one-to-one' ? '1' : 'N';
  const parent = rel.optional ? '0..1' : '1';
  return `${rel.fromCols.join(', ')} · ${many}:${parent}`;
}

// worstSeverityByTable gives the border colour: red if a table has any high
// finding, orange for medium. Low findings don't colour the border.
function worstSeverityByTable(findings) {
  const rank = { high: 2, medium: 1 };
  const worst = new Map();
  for (const f of findings) {
    if (!rank[f.severity]) continue;
    if (!worst.has(f.table) || rank[f.severity] > rank[worst.get(f.table)]) worst.set(f.table, f.severity);
  }
  return worst;
}

// shortType keeps cards narrow: "timestamp with time zone" → "timestamptz".
function shortType(type) {
  return type
    .replace('timestamp with time zone', 'timestamptz')
    .replace('timestamp without time zone', 'timestamp')
    .replace('character varying', 'varchar')
    .replace('double precision', 'float8');
}

export function rowsText(table) {
  if (table.rowEstimate < 0) return 'never analyzed';
  return compactNumber(table.rowEstimate) + ' rows';
}

export function compactNumber(n) {
  return new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(n);
}

const measure = document.createElement('canvas').getContext('2d');
function textWidth(text, font) {
  measure.font = font;
  // Emoji measure oddly in some fonts; the marker column has a fixed width.
  return measure.measureText(text).width;
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
