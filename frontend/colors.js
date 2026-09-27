// Schema colours. Every schema other than public gets its own colour, so
// the tables that belong together stand out on the graph, in the sidebar
// and in the legend.
//
// Colours go out in alphabetical order of schema name, so the same database
// always gets the same colours (billing is red when it's the only other
// schema). After eight schemas the palette starts again.

export const PALETTE = [
  '#e5484d', // red
  '#3e7bfa', // blue
  '#30a46c', // green
  '#8e4ec6', // purple
  '#12a594', // teal
  '#e8a317', // amber
  '#d6409f', // pink
  '#a0714f', // brown
];

// schemaColors maps each schema name to its colour. public gets none: it's
// the default, and leaving it plain is what makes the others stand out.
export function schemaColors(schemaNames) {
  const colors = new Map();
  const others = [...new Set(schemaNames)].filter((s) => s !== 'public').sort();
  others.forEach((name, i) => colors.set(name, PALETTE[i % PALETTE.length]));
  return colors;
}

// mix blends colour a into colour b; amount 0.3 means 30% a, 70% b. Mixing
// with the theme's card colour gives tints that work in light and dark mode.
export function mix(a, b, amount) {
  const [r1, g1, b1] = rgb(a);
  const [r2, g2, b2] = rgb(b);
  const blend = (x, y) => Math.round(x * amount + y * (1 - amount));
  return '#' + [blend(r1, r2), blend(g1, g2), blend(b1, b2)]
    .map((n) => n.toString(16).padStart(2, '0')).join('');
}

function rgb(hex) {
  const h = hex.replace('#', '');
  const full = h.length === 3 ? [...h].map((c) => c + c).join('') : h;
  return [0, 2, 4].map((i) => parseInt(full.slice(i, i + 2), 16));
}
