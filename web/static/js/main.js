import { $, el, fmt, ago, fmtDateTime, clamp, debounce, hsl, rgbCss, authorName, authorHandle } from './util.js';
import { loadAtlas, pointColor } from './data.js';
import { PointRenderer } from './gl.js';
import { Camera, PickGrid, Overlay } from './view.js';
import { Timeline } from './timeline.js';
import { Outline, Drawer, ActivityPanel } from './panels.js';

async function main() {
  const status = $('#status');
  let D;
  try {
    D = await loadAtlas((m) => (status.textContent = m));
  } catch (e) {
    status.textContent = e.message;
    status.classList.add('error');
    return;
  }
  status.remove();
  start(D);
}

function start(D) {
  const stage = $('#stage');
  const glCanvas = $('#gl');
  const ovCanvas = $('#overlay');
  const tooltip = $('#tooltip');

  const cam = new Camera();
  const renderer = new PointRenderer(glCanvas, D);
  const overlay = new Overlay(ovCanvas);
  const grid = new PickGrid(D);

  const S = {
    sel: null,
    hoverTopic: null,
    hoverPoint: -1,
    selectedPoint: -1,
    edges: null,
    matches: [],
    colorMode: 'topic',
    range: [D.atlas.ts_range[0], D.atlas.ts_range[1]],
    hot: null,
  };

  // On a phone the outline is an overlay: start with it closed so the map is what you see first.
  const isSmall = () => window.matchMedia('(max-width: 800px)').matches;
  if (isSmall()) document.body.classList.add('no-sidebar');

  // ---------------------------------------------------------------- header
  const a = D.atlas;
  $('#meta').textContent = `${fmt.format(a.n_posts)} posts · ${fmt.format(a.n_authors)} accounts · ${a.window_days} days · map built ${ago(a.built_at)}`;

  // ---------------------------------------------------------------- drawing
  let dirty = true;
  const requestDraw = () => { dirty = true; };
  cam.onChange = requestDraw;

  function frame(now) {
    if (cam.step(now)) dirty = true;
    if (dirty) {
      dirty = false;
      renderer.draw({ cx: cam.cx, cy: cam.cy, scale: cam.scale, w: cam.w, h: cam.h, dpr: cam.dpr, zoomSize: cam.zoomSize });
      overlay.draw({ cam, D, sel: S.sel, hoverTopic: S.hoverTopic, hoverPoint: S.hoverPoint, selectedPoint: S.selectedPoint, edges: S.edges });
    }
    requestAnimationFrame(frame);
  }

  function resize() {
    const r = stage.getBoundingClientRect();
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const w = Math.round(r.width * dpr), h = Math.round(r.height * dpr);
    renderer.resize(w, h);
    ovCanvas.width = w;
    ovCanvas.height = h;
    cam.resize(w, h, dpr);
    requestDraw();
  }
  new ResizeObserver(resize).observe(stage);
  resize();
  cam.home(false);
  requestAnimationFrame(frame);

  // ---------------------------------------------------------------- colours + highlight state
  const colors = new Uint8Array(D.N * 4);
  function recolor() {
    const [tMin, tMax] = a.ts_range;
    const span = Math.max(tMax - tMin, 1);
    for (let i = 0; i < D.N; i++) {
      let c;
      if (S.colorMode === 'age') {
        const t = (D.ts[i] - tMin) / span;
        c = hsl(225 - t * 190, 0.75, 0.38 + t * 0.3);
      } else if (S.colorMode === 'engagement') {
        const e = Math.pow(D.eng[i], 0.6);
        c = hsl(210 - e * 190, 0.35 + e * 0.6, 0.36 + e * 0.32);
      } else c = pointColor(D, i);
      colors[4 * i] = c[0]; colors[4 * i + 1] = c[1]; colors[4 * i + 2] = c[2]; colors[4 * i + 3] = 255;
    }
    renderer.setColors(colors);
    requestDraw();
  }

  const stateArr = new Uint8Array(D.N);
  function applyHighlight() {
    const hot = S.hot;
    if (!hot) stateArr.fill(1);
    else {
      stateArr.fill(S.sel && S.sel.type === 'post' ? 1 : 0);
      for (const i of hot) stateArr[i] = 2;
    }
    renderer.setState(stateArr);
    requestDraw();
  }

  const inRange = (i) => D.ts[i] >= S.range[0] && D.ts[i] <= S.range[1];

  // ---------------------------------------------------------------- selection
  const actions = {
    select: (sel, opts) => select(sel, opts),
    hoverTopic: (id) => { S.hoverTopic = id; requestDraw(); },
    hoverPoint: (i) => { S.hoverPoint = i ?? -1; requestDraw(); },
  };
  const outline = new Outline($('#outline'), D, actions);
  const drawer = new Drawer($('#drawer'), D, actions);
  const activity = new ActivityPanel($('#activity'), D, actions);

  const timeline = new Timeline($('#timeline'), D, (t0, t1) => {
    S.range = [t0, t1];
    renderer.setTimeRange(t0, t1);
    requestDraw();
    refreshAfterRange();
  });
  const playBtn = $('#play');
  timeline.onPlayState = (p) => { playBtn.textContent = p ? '⏸' : '▶'; playBtn.title = p ? 'Pause' : 'Replay the week'; };
  playBtn.onclick = () => timeline.togglePlay();

  const refreshAfterRange = debounce(() => {
    updateRangeText();
    if (S.sel) drawer.show(S.sel, { range: S.range, matches: S.matches });
  }, 120);

  function updateRangeText() {
    const n = (() => { let c = 0; for (let i = 0; i < D.N; i++) if (inRange(i)) c++; return c; })();
    const full = timeline.isFull();
    $('#range-info').textContent = full ? '' : `${fmt.format(n)} posts in view`;
  }

  function threadEdges(k) {
    const out = [];
    for (const m of D.threadMembers[k]) {
      const p = D.parent[m];
      if (p >= 0 && D.thread[p] === k) out.push([p, m]);
    }
    return out;
  }

  /** Bounds ignoring the outer 5% of points so a few strays don't dictate the zoom. */
  function robustBounds(idx) {
    const xs = [], ys = [];
    for (const i of idx) { xs.push(D.xy[2 * i]); ys.push(D.xy[2 * i + 1]); }
    xs.sort((p, q) => p - q); ys.sort((p, q) => p - q);
    const lo = idx.length > 20 ? Math.floor(idx.length * 0.04) : 0;
    const hi = idx.length - 1 - lo;
    return { x0: xs[lo], x1: xs[hi], y0: ys[lo], y1: ys[hi] };
  }

  function pad() {
    const dpr = cam.dpr;
    const drawerOpen = $('#drawer').classList.contains('open');
    return { left: 50 * dpr, right: (drawerOpen ? 440 : 50) * dpr, top: 50 * dpr, bottom: 150 * dpr };
  }

  function select(sel, { fly = true, hash = true } = {}) {
    if (sel && isSmall()) document.body.classList.add('no-sidebar'); // get the overlay out of the way
    S.sel = sel;
    S.hot = null;
    S.edges = null;
    S.selectedPoint = -1;
    let fitIdx = null;
    let overlayIdx = null;

    if (sel) {
      switch (sel.type) {
        case 'region': S.hot = D.byRegion.get(sel.id) ?? []; fitIdx = S.hot; break;
        case 'topic': S.hot = D.byTopic.get(sel.id) ?? []; fitIdx = S.hot; break;
        case 'thread': S.hot = D.threadMembers[sel.id]; S.edges = threadEdges(sel.id); fitIdx = S.hot; break;
        case 'author': S.hot = D.byAuthor.get(sel.id) ?? []; fitIdx = S.hot; break;
        case 'search': S.hot = S.matches; fitIdx = S.matches; break;
        case 'post': {
          const i = sel.id;
          S.selectedPoint = i;
          const k = D.thread[i];
          S.hot = k >= 0 ? D.threadMembers[k] : [i];
          if (k >= 0) S.edges = threadEdges(k);
          break;
        }
      }
      overlayIdx = S.hot;
    }
    applyHighlight();
    timeline.setOverlay(overlayIdx && sel?.type !== 'post' ? timeline.histogram(overlayIdx) : null);
    outline.setSelection(sel && ['region', 'topic'].includes(sel.type) ? sel : sel?.type === 'post' ? { type: 'topic', id: D.topic[sel.id] } : null);

    const el$ = $('#drawer');
    if (sel) {
      el$.classList.add('open');
      drawer.show(sel, { range: S.range, matches: S.matches });
    } else drawer.hide();

    if (fly) {
      if (sel?.type === 'post') {
        const i = sel.id;
        const target = Math.max(cam.scale, cam.fitScale * 14);
        const p = pad();
        const ox = (p.right - p.left) / 2 / target, oy = (p.bottom - p.top) / 2 / target;
        cam.flyTo(D.xy[2 * i] + ox, D.xy[2 * i + 1] - oy, target);
      } else if (fitIdx && fitIdx.length) cam.fitBounds(robustBounds(fitIdx), pad(), true, 30);
      else if (!sel) { /* keep the view where the user left it */ }
    }
    if (hash) writeHash(sel);
    requestDraw();
  }

  // ---------------------------------------------------------------- URL hash
  function writeHash(sel) {
    let h = '';
    if (sel) {
      const id = sel.type === 'thread' ? a.threads[sel.id].root
        : sel.type === 'post' ? D.cols.uri[sel.id]
        : sel.type === 'author' ? D.authors[sel.id].did
        : sel.id;
      h = `#${sel.type}/${encodeURIComponent(id)}`;
    }
    history.replaceState(null, '', location.pathname + h);
  }
  function readHash() {
    const m = /^#(\w+)\/(.+)$/.exec(location.hash);
    if (!m) return null;
    const id = decodeURIComponent(m[2]);
    switch (m[1]) {
      case 'region': return D.regionById.has(+id) ? { type: 'region', id: +id } : null;
      case 'topic': return D.topicById.has(+id) ? { type: 'topic', id: +id } : null;
      case 'thread': { const k = a.threads.findIndex((t) => t.root === id); return k >= 0 ? { type: 'thread', id: k } : null; }
      case 'post': { const i = D.uriIndex.get(id); return i != null ? { type: 'post', id: i } : null; }
      case 'author': { const k = D.authors.findIndex((x) => x.did === id); return k >= 0 ? { type: 'author', id: k } : null; }
      case 'search': runSearch(id); return null;
    }
    return null;
  }

  // ---------------------------------------------------------------- search
  const searchBox = $('#search');
  function runSearch(q) {
    q = q.trim();
    searchBox.value = q;
    if (q.length < 2) {
      S.matches = [];
      if (S.sel?.type === 'search') select(null, { fly: false });
      return;
    }
    const ql = q.toLowerCase();
    const authorHits = new Set();
    D.authors.forEach((au, k) => { if ((au.handle + ' ' + au.name).toLowerCase().includes(ql)) authorHits.add(k); });
    const out = [];
    for (let i = 0; i < D.N; i++) if (D.textLower[i].includes(ql) || authorHits.has(D.author[i])) out.push(i);
    S.matches = out;
    select({ type: 'search', id: q }, { fly: true });
  }
  searchBox.addEventListener('input', debounce(() => runSearch(searchBox.value), 220));
  searchBox.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { searchBox.value = ''; searchBox.blur(); runSearch(''); }
  });

  // ---------------------------------------------------------------- colour mode, tabs, buttons
  $('#color-mode').addEventListener('change', (e) => { S.colorMode = e.target.value; recolor(); });
  document.querySelectorAll('.tab').forEach((t) => t.addEventListener('click', () => {
    document.querySelectorAll('.tab').forEach((x) => x.classList.toggle('active', x === t));
    document.querySelectorAll('.tabpane').forEach((p) => p.classList.toggle('active', p.id === t.dataset.pane));
    if (t.dataset.pane === 'activity') activity.load();
  }));
  $('#zoom-in').onclick = () => cam.zoomAt(cam.w / 2, cam.h / 2, 1.6);
  $('#zoom-out').onclick = () => cam.zoomAt(cam.w / 2, cam.h / 2, 1 / 1.6);
  $('#home').onclick = () => { select(null, { fly: false }); cam.home(); };
  $('#sidebar-toggle').onclick = () => { document.body.classList.toggle('no-sidebar'); };

  // ---------------------------------------------------------------- pointer interaction
  const pointers = new Map();
  let dragMoved = 0, pinchDist = 0;
  const cssPos = (e) => { const r = ovCanvas.getBoundingClientRect(); return [e.clientX - r.left, e.clientY - r.top]; };

  ovCanvas.addEventListener('pointerdown', (e) => {
    ovCanvas.setPointerCapture(e.pointerId);
    pointers.set(e.pointerId, [e.clientX, e.clientY]);
    dragMoved = 0;
    if (pointers.size === 2) { const [p, q] = [...pointers.values()]; pinchDist = Math.hypot(p[0] - q[0], p[1] - q[1]); }
  });
  ovCanvas.addEventListener('pointermove', (e) => {
    const prev = pointers.get(e.pointerId);
    if (prev) {
      pointers.set(e.pointerId, [e.clientX, e.clientY]);
      if (pointers.size === 1) {
        const dx = e.clientX - prev[0], dy = e.clientY - prev[1];
        dragMoved += Math.abs(dx) + Math.abs(dy);
        cam.panBy(dx * cam.dpr, dy * cam.dpr);
        hideTip();
      } else if (pointers.size === 2) {
        const [p, q] = [...pointers.values()];
        const d = Math.hypot(p[0] - q[0], p[1] - q[1]);
        const r = ovCanvas.getBoundingClientRect();
        if (pinchDist > 0) cam.zoomAt(((p[0] + q[0]) / 2 - r.left) * cam.dpr, ((p[1] + q[1]) / 2 - r.top) * cam.dpr, d / pinchDist);
        pinchDist = d;
        dragMoved += 10;
      }
      return;
    }
    hover(e);
  });
  const endPointer = (e) => {
    const wasClick = pointers.size === 1 && dragMoved < 5;
    pointers.delete(e.pointerId);
    pinchDist = 0;
    if (wasClick && e.type === 'pointerup') click(e);
  };
  ovCanvas.addEventListener('pointerup', endPointer);
  ovCanvas.addEventListener('pointercancel', endPointer);
  ovCanvas.addEventListener('pointerleave', () => { if (!pointers.size) { hideTip(); S.hoverPoint = -1; requestDraw(); } });
  ovCanvas.addEventListener('wheel', (e) => {
    e.preventDefault();
    const [x, y] = cssPos(e);
    const f = Math.exp(-e.deltaY * (e.ctrlKey ? 0.012 : 0.0018));
    cam.zoomAt(x * cam.dpr, y * cam.dpr, f);
  }, { passive: false });
  ovCanvas.addEventListener('dblclick', (e) => {
    const [x, y] = cssPos(e);
    cam.zoomAt(x * cam.dpr, y * cam.dpr, 2);
  });

  const visible = (i) => inRange(i) && (!S.hot || stateArr[i] > 0 || S.sel?.type === 'post');

  function pick(e) {
    const [x, y] = cssPos(e);
    return grid.nearest(cam, x * cam.dpr, y * cam.dpr, 11 * cam.dpr, visible);
  }

  function hover(e) {
    const [x, y] = cssPos(e);
    const label = overlay.labelAt(x, y);
    if (label) {
      ovCanvas.style.cursor = 'pointer';
      if (label.kind === 'topic') { S.hoverTopic = label.id; }
      hideTip();
      S.hoverPoint = -1;
      requestDraw();
      return;
    }
    if (S.hoverTopic != null && !document.querySelector('.o-topic:hover')) { S.hoverTopic = null; requestDraw(); }
    const i = pick(e);
    ovCanvas.style.cursor = i >= 0 ? 'pointer' : 'grab';
    if (i !== S.hoverPoint) { S.hoverPoint = i; requestDraw(); }
    if (i >= 0) showTip(i, x, y); else hideTip();
  }

  function click(e) {
    const [x, y] = cssPos(e);
    const label = overlay.labelAt(x, y);
    if (label) { select({ type: label.kind, id: label.id }); return; }
    const i = pick(e);
    if (i >= 0) select({ type: 'post', id: i });
    else if (S.sel) select(null, { fly: false });
  }

  function showTip(i, x, y) {
    const au = D.authors[D.author[i]];
    const t = D.topicById.get(D.topic[i]);
    tooltip.replaceChildren(
      el('div', { class: 'tip-head' }, el('b', {}, authorName(au)), el('span', { class: 'muted' }, ' ' + authorHandle(au))),
      el('div', { class: 'tip-text' }, D.text[i].length > 260 ? D.text[i].slice(0, 260) + '…' : D.text[i] || '(no text)'),
      el('div', { class: 'tip-foot muted' }, `${fmtDateTime(D.ts[i])}${t ? ' · ' + t.title : ''}${D.likes[i] ? ' · ♥ ' + D.likes[i] : ''}`),
    );
    tooltip.style.display = 'block';
    const w = tooltip.offsetWidth, h = tooltip.offsetHeight, sw = stage.clientWidth, sh = stage.clientHeight;
    tooltip.style.left = clamp(x + 16, 8, sw - w - 8) + 'px';
    tooltip.style.top = clamp(y + 16 > sh - h - 130 ? y - h - 14 : y + 16, 8, sh - h - 8) + 'px';
  }
  function hideTip() { tooltip.style.display = 'none'; }

  // ---------------------------------------------------------------- keyboard
  window.addEventListener('keydown', (e) => {
    if (e.target === searchBox) return;
    if (e.key === 'Escape') select(null, { fly: false });
    else if (e.key === '/') { e.preventDefault(); searchBox.focus(); }
    else if (e.key === ' ' && e.target === document.body) { e.preventDefault(); timeline.togglePlay(); }
  });
  window.addEventListener('hashchange', () => { const s = readHash(); if (s) select(s, { hash: false }); });

  // ---------------------------------------------------------------- go
  recolor();
  applyHighlight();
  outline.render();
  const first = readHash();
  if (first) select(first, { hash: false });
  window.__atlas = { D, cam, S, select, timeline, renderer, overlay };
}

main();
