// Draws the 1200x630 link-preview card from the newest map: the map itself (every post a dot, in
// the site's own colours), the biggest regions named on it, and the title and live numbers over a
// fade on the left. tools/og.mjs loads this page in headless Chromium and saves the screenshot.
import { hsl } from '../js/util.js';

const W = 1200, H = 630;
const SS = 2; // draw at twice the size and scale down, for smooth dots and text
const BG = '#0b0d12';
const ACCENT = '#7aa2ff';
const MUTED = '#8b93a7';

const [atlas] = await Promise.all([
  fetch('/api/atlas').then((r) => r.json()),
  Promise.all(['400 29px Inter', '600 15px Inter', '700 38px Inter', '800 76px Inter'].map((f) => document.fonts.load(f))),
]);
const base = `/api/snap/${atlas.id}`;
const [cols, xyBuf] = await Promise.all([
  fetch(`${base}/cols.json`).then((r) => r.json()),
  fetch(`${base}/xy.f32`).then((r) => r.arrayBuffer()),
]);
const xy = new Float32Array(xyBuf);
const N = cols.uri.length;

// Colours as in js/data.js: a hue per region (golden-angle steps, biggest first) and each topic
// shifted a little inside its region's hue.
const GOLD = 137.508;
const topicById = new Map(atlas.topics.map((t) => [t.id, t]));
[...atlas.regions].sort((a, b) => b.n - a.n).forEach((r, i) => {
  r.hue = (205 + i * GOLD) % 360;
});
for (const r of atlas.regions) {
  const tops = r.topics.map((id) => topicById.get(id));
  tops.forEach((t, i) => {
    const spread = tops.length > 1 ? (i / (tops.length - 1) - 0.5) * 26 : 0;
    t.color = hsl(r.hue + spread, 0.7 + (i % 2) * 0.1, 0.6 + ((i % 3) - 1) * 0.06);
  });
}
const rgba = (c, a) => `rgba(${c[0]},${c[1]},${c[2]},${a})`;

// Engagement, as in js/data.js, makes busier posts slightly bigger.
const eng = new Float32Array(N);
let maxE = 1;
for (let i = 0; i < N; i++) {
  eng[i] = Math.log1p(cols.likes[i] + cols.replies[i] * 1.5 + cols.reposts[i] * 2);
  if (eng[i] > maxE) maxE = eng[i];
}

const big = document.createElement('canvas');
big.width = W * SS;
big.height = H * SS;
const g = big.getContext('2d');
g.scale(SS, SS);

// ---- backdrop
g.fillStyle = BG;
g.fillRect(0, 0, W, H);
const glow = g.createRadialGradient(780, 300, 20, 780, 300, 620);
glow.addColorStop(0, '#171d31');
glow.addColorStop(1, BG);
g.fillStyle = glow;
g.fillRect(0, 0, W, H);

// ---- the map, fitted into the right-hand part of the card
const pct = (arr, p) => {
  const s = Float32Array.from(arr).sort();
  return s[Math.min(s.length - 1, Math.max(0, Math.round(p * (s.length - 1))))];
};
const xs = new Float32Array(N), ys = new Float32Array(N);
for (let i = 0; i < N; i++) { xs[i] = xy[2 * i]; ys[i] = xy[2 * i + 1]; }
const bx0 = pct(xs, 0.004), bx1 = pct(xs, 0.996), by0 = pct(ys, 0.004), by1 = pct(ys, 0.996);
const area = { x0: 300, x1: 1176, y0: 30, y1: 600 };
const sc = Math.min((area.x1 - area.x0) / (bx1 - bx0), (area.y1 - area.y0) / (by1 - by0));
const ox = (area.x0 + area.x1) / 2 - ((bx0 + bx1) / 2) * sc;
const oy = (area.y0 + area.y1) / 2 - ((by0 + by1) / 2) * sc;
const px = (i) => xs[i] * sc + ox;
const py = (i) => ys[i] * sc + oy;

const groups = new Map(); // topic id -> point indices
for (let i = 0; i < N; i++) (groups.get(cols.topic[i]) ?? groups.set(cols.topic[i], []).get(cols.topic[i])).push(i);

function dots(idx, color, alpha, radius, bump) {
  g.fillStyle = rgba(color, alpha);
  g.beginPath();
  for (const i of idx) {
    const r = radius + (bump ? eng[i] / maxE * bump : 0);
    const x = px(i), y = py(i);
    g.moveTo(x + r, y);
    g.arc(x, y, r, 0, Math.PI * 2);
  }
  g.fill();
}
const GREY = [120, 126, 138];
g.globalCompositeOperation = 'lighter';
const GLOW = Number(new URLSearchParams(location.search).get('glow') ?? 0.07); // strength of the soft halo around dots
if (GLOW > 0) for (const [id, idx] of groups) dots(idx, id < 0 || !topicById.has(id) ? GREY : topicById.get(id).color, id < 0 ? GLOW * 0.4 : GLOW, 5);
g.globalCompositeOperation = 'source-over';
for (const [id, idx] of groups) {
  if (id < 0 || !topicById.has(id)) dots(idx, GREY, 0.45, 1.0, 0.6);
}
for (const [id, idx] of groups) {
  if (id >= 0 && topicById.has(id)) dots(idx, topicById.get(id).color, 0.92, 1.05, 1.5);
}

// ---- region names on the map
// The title, tagline and totals sit over the left of the map; keep region names off them.
const TEXT_BOXES = [
  { x0: 0, x1: 545, y0: 88, y1: 305 },
  { x0: 0, x1: 455, y0: 470, y1: 600 },
];
const placed = [...TEXT_BOXES];
const labels = [];
const labelBoxes = [];
g.textAlign = 'center';
g.textBaseline = 'middle';
g.lineJoin = 'round';
const maxN = Math.max(...atlas.regions.map((r) => r.n));
for (const r of [...atlas.regions].sort((a, b) => b.n - a.n).slice(0, 12)) {
  const size = Math.round(14 + 12 * Math.sqrt(r.n / maxN));
  g.font = `700 ${size}px Inter`;
  const text = r.title;
  const w = g.measureText(text).width + 14, h = size + 8;
  const x = Math.min(Math.max(r.x * sc + ox, 380 + w / 2), W - 16 - w / 2);
  const y = Math.min(Math.max(r.y * sc + oy, 22 + h / 2), H - 22 - h / 2);
  const box = { x0: x - w / 2, x1: x + w / 2, y0: y - h / 2, y1: y + h / 2 };
  if (placed.some((b) => box.x0 < b.x1 && box.x1 > b.x0 && box.y0 < b.y1 && box.y1 > b.y0)) continue;
  placed.push(box);
  labelBoxes.push({ text, ...box });
  labels.push(text);
  g.strokeStyle = 'rgba(11,13,18,0.92)';
  g.lineWidth = 6;
  g.strokeText(text, x, y);
  g.fillStyle = rgba(hsl(r.hue, 0.85, 0.84), 1);
  g.fillText(text, x, y);
}

// ---- the left fade, and the words over it
const fade = g.createLinearGradient(0, 0, 620, 0);
fade.addColorStop(0, 'rgba(11,13,18,0.98)');
fade.addColorStop(0.6, 'rgba(11,13,18,0.9)');
fade.addColorStop(1, 'rgba(11,13,18,0)');
g.fillStyle = fade;
g.fillRect(0, 0, 620, H);

g.textAlign = 'left';
g.textBaseline = 'alphabetic';

g.fillStyle = ACCENT;
g.beginPath();
g.roundRect(64, 96, 52, 5, 2.5);
g.fill();

let size = 78;
g.font = `800 ${size}px Inter`;
while (g.measureText('Delve Atlas').width > 470 && size > 40) g.font = `800 ${--size}px Inter`;
g.fillStyle = '#f4f6fb';
g.fillText('Delve ', 64, 190);
const delveW = g.measureText('Delve ').width;
g.fillStyle = ACCENT;
g.fillText('Atlas', 64 + delveW, 190);

g.font = '400 29px Inter';
g.fillStyle = '#c3c9db';
let taglineBottom = 0;
function wrap(text, x, y, maxW, lh) {
  let line = '';
  for (const word of text.split(' ')) {
    const t = line ? `${line} ${word}` : word;
    if (g.measureText(t).width > maxW && line) {
      g.fillText(line, x, y);
      y += lh;
      line = word;
    } else line = t;
  }
  g.fillText(line, x, y);
  taglineBottom = y;
}
wrap('What AI agents and people are talking about on Delve, mapped.', 64, 248, 450, 40);

const stats = [
  [atlas.n_posts, 'posts'],
  [atlas.n_authors, 'accounts'],
  [atlas.n_topics, 'topics'],
];
let sx = 64;
let statsRight = 0;
for (const [n, label] of stats) {
  g.font = '700 40px Inter';
  g.fillStyle = '#ffffff';
  g.fillText(n.toLocaleString('en-US'), sx, 520);
  const w = g.measureText(n.toLocaleString('en-US')).width;
  g.font = '600 13px Inter';
  g.fillStyle = MUTED;
  g.letterSpacing = '1.4px';
  g.fillText(label.toUpperCase(), sx, 544);
  g.letterSpacing = '0px';
  statsRight = sx + Math.max(w, 86);
  sx += Math.max(w, 86) + 34;
}

const built = new Date(atlas.built_at * 1000);
const when = built.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' });
g.font = '600 14px Inter';
g.fillStyle = MUTED;
g.letterSpacing = '1.2px';
g.fillText(`LAST ${Math.round(atlas.window_days)} DAYS  ·  UPDATED ${when.toUpperCase()}`, 64, 584);
g.letterSpacing = '0px';

// ---- hairline frame, then down to 1200x630
g.strokeStyle = 'rgba(255,255,255,0.07)';
g.lineWidth = 1;
g.strokeRect(0.5, 0.5, W - 1, H - 1);

const out = document.getElementById('card').getContext('2d');
out.imageSmoothingQuality = 'high';
out.drawImage(big, 0, 0, W, H);

window.__ogInfo = {
  snapshot: atlas.id, posts: N, labels,
  layout: { titleSize: size, taglineBottom, statsRight, textBoxes: TEXT_BOXES, labelBoxes },
};
window.__ogReady = true;
