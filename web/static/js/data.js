// Loads a snapshot and derives the typed arrays the renderer and panels use.

import { hsl } from './util.js';

export async function loadAtlas(onProgress = () => {}) {
  onProgress('Finding the latest map…');
  const metaRes = await fetch('/api/atlas');
  if (!metaRes.ok) throw new Error(`no atlas yet (${metaRes.status}). Run the build first.`);
  const atlas = await metaRes.json();
  const base = `/api/snap/${atlas.id}`;

  onProgress('Loading posts…');
  const [cols, xyBuf] = await Promise.all([
    fetch(`${base}/cols.json`).then((r) => r.json()),
    fetch(`${base}/xy.f32`).then((r) => r.arrayBuffer()),
  ]);
  return derive(atlas, cols, new Float32Array(xyBuf));
}

function derive(atlas, cols, xy) {
  const N = cols.uri.length;
  const topicById = new Map(atlas.topics.map((t) => [t.id, t]));
  const regionById = new Map(atlas.regions.map((r) => [r.id, r]));

  // Colour: one hue per region (spread by the golden angle so neighbours differ), and each
  // topic shifts a little within its region's hue so topics read as members of one family.
  const GOLD = 137.508;
  const order = [...atlas.regions].sort((a, b) => b.n - a.n);
  order.forEach((r, i) => {
    r.hue = (205 + i * GOLD) % 360;
    r.color = hsl(r.hue, 0.72, 0.62);
  });
  for (const r of atlas.regions) {
    const tops = r.topics.map((id) => topicById.get(id));
    tops.forEach((t, i) => {
      const spread = tops.length > 1 ? (i / (tops.length - 1) - 0.5) * 26 : 0;
      t.hue = r.hue + spread;
      t.color = hsl(t.hue, 0.7 + (i % 2) * 0.1, 0.6 + ((i % 3) - 1) * 0.06);
    });
  }
  const noise = { id: -1, title: 'Unclustered', color: [120, 126, 138], hue: 0, n: 0 };

  const topic = Int16Array.from(cols.topic);
  const region = Int16Array.from(cols.region);
  const author = Uint16Array.from(cols.author);
  const ts = Float64Array.from(cols.ts);
  const thread = Int32Array.from(cols.thread);
  const parent = Int32Array.from(cols.parent);
  const likes = Uint16Array.from(cols.likes);
  const replies = Uint16Array.from(cols.replies);
  const reposts = Uint16Array.from(cols.reposts);

  // Engagement 0..1, log-scaled, drives point size.
  const eng = new Float32Array(N);
  let maxE = 1;
  for (let i = 0; i < N; i++) {
    eng[i] = Math.log1p(likes[i] * 1.0 + replies[i] * 1.5 + reposts[i] * 2);
    if (eng[i] > maxE) maxE = eng[i];
  }
  for (let i = 0; i < N; i++) eng[i] /= maxE;

  const members = (arr, id) => {
    const out = [];
    for (let i = 0; i < N; i++) if (arr[i] === id) out.push(i);
    return out;
  };
  const byTopic = new Map();
  const byRegion = new Map();
  for (let i = 0; i < N; i++) {
    (byTopic.get(topic[i]) ?? byTopic.set(topic[i], []).get(topic[i])).push(i);
    (byRegion.get(region[i]) ?? byRegion.set(region[i], []).get(region[i])).push(i);
  }
  const byAuthor = new Map();
  for (let i = 0; i < N; i++) (byAuthor.get(author[i]) ?? byAuthor.set(author[i], []).get(author[i])).push(i);

  const threadMembers = atlas.threads.map(() => []);
  for (let i = 0; i < N; i++) if (thread[i] >= 0) threadMembers[thread[i]].push(i);

  return {
    atlas, cols, xy, N,
    topic, region, author, ts, thread, parent, likes, replies, reposts, eng,
    topicById, regionById, noise, members, byTopic, byRegion, byAuthor, threadMembers,
    authors: atlas.authors,
    text: cols.text,
    textLower: cols.text.map((t) => t.toLowerCase()),
    uriIndex: new Map(cols.uri.map((u, i) => [u, i])),
  };
}

/** Axis-aligned bounds of a set of point indices, in layout units. */
export function boundsOf(D, idx) {
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const i of idx) {
    const x = D.xy[2 * i], y = D.xy[2 * i + 1];
    if (x < x0) x0 = x; if (x > x1) x1 = x;
    if (y < y0) y0 = y; if (y > y1) y1 = y;
  }
  return { x0, y0, x1, y1 };
}

/** Colour of a point's group. */
export function pointColor(D, i) {
  const t = D.topicById.get(D.topic[i]);
  return t ? t.color : D.noise.color;
}
