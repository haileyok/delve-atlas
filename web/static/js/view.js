// Camera, hover picking and the 2D overlay (labels, thread edges, rings).

import { clamp, lerp, smoothstep, rgbCss } from './util.js';

const EXTENT = 1.1; // the layout fills roughly [-1, 1]

export class Camera {
  constructor() {
    this.cx = 0;
    this.cy = 0;
    this.scale = 1; // device px per layout unit
    this.w = 1;
    this.h = 1;
    this.dpr = 1;
    this.anim = null;
    this.onChange = () => {};
  }

  get fitScale() {
    return Math.min(this.w, this.h) / (2 * EXTENT);
  }
  /** 1 when the whole map fits; grows as you zoom in. */
  get zoom() {
    return this.scale / this.fitScale;
  }
  /** Sprite size grows slower than the map, so zoomed-in views separate dense clumps. */
  get zoomSize() {
    return clamp(Math.pow(this.zoom, 0.55), 0.7, 7);
  }

  resize(w, h, dpr) {
    const keepZoom = this.w > 1 ? this.zoom : 1;
    this.w = w;
    this.h = h;
    this.dpr = dpr;
    this.scale = this.fitScale * keepZoom;
    this.onChange();
  }

  clampScale(s) {
    return clamp(s, this.fitScale * 0.7, this.fitScale * 600);
  }

  toScreen(x, y) {
    return [(x - this.cx) * this.scale + this.w / 2, (y - this.cy) * -this.scale + this.h / 2];
  }
  toWorld(px, py) {
    return [(px - this.w / 2) / this.scale + this.cx, -(py - this.h / 2) / this.scale + this.cy];
  }

  panBy(dx, dy) {
    this.cx -= dx / this.scale;
    this.cy += dy / this.scale;
    this.anim = null;
    this.onChange();
  }

  zoomAt(px, py, factor) {
    const [wx, wy] = this.toWorld(px, py);
    this.scale = this.clampScale(this.scale * factor);
    // keep the point under the cursor fixed
    this.cx = wx - (px - this.w / 2) / this.scale;
    this.cy = wy + (py - this.h / 2) / this.scale;
    this.anim = null;
    this.onChange();
  }

  /** Fit a layout-space box into the view, leaving room for panels (padding in device px). */
  fitBounds(b, pad = {}, animate = true, maxZoom = 40) {
    const l = pad.left ?? 60, r = pad.right ?? 60, t = pad.top ?? 60, bt = pad.bottom ?? 60;
    const bw = Math.max(b.x1 - b.x0, 0.04), bh = Math.max(b.y1 - b.y0, 0.04);
    const s = Math.min((this.w - l - r) / bw, (this.h - t - bt) / bh);
    const scale = clamp(this.clampScale(s), this.fitScale * 0.7, this.fitScale * maxZoom);
    // centre of the box, shifted so it sits in the free area between the paddings
    const ox = (r - l) / 2 / scale, oy = (bt - t) / 2 / scale;
    this.flyTo((b.x0 + b.x1) / 2 + ox, (b.y0 + b.y1) / 2 - oy, scale, animate);
  }

  flyTo(cx, cy, scale, animate = true) {
    if (!animate) {
      Object.assign(this, { cx, cy, scale, anim: null });
      this.onChange();
      return;
    }
    // Interpolate scale in log space so the zoom feels even.
    this.anim = {
      t0: performance.now(), dur: 650,
      from: { cx: this.cx, cy: this.cy, ls: Math.log(this.scale) },
      to: { cx, cy, ls: Math.log(scale) },
    };
    this.onChange();
  }

  /** Advance the animation; returns true while it is running. */
  step(now) {
    const a = this.anim;
    if (!a) return false;
    const t = clamp((now - a.t0) / a.dur, 0, 1);
    const e = t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2;
    this.cx = lerp(a.from.cx, a.to.cx, e);
    this.cy = lerp(a.from.cy, a.to.cy, e);
    this.scale = Math.exp(lerp(a.from.ls, a.to.ls, e));
    if (t >= 1) this.anim = null;
    return this.anim != null;
  }

  home(animate = true) {
    this.flyTo(0, 0, this.fitScale, animate);
  }
}

/** Uniform grid over layout space for nearest-point queries. */
export class PickGrid {
  constructor(D, cells = 96) {
    this.D = D;
    this.cells = cells;
    this.min = -EXTENT - 0.1;
    this.size = (EXTENT + 0.1) * 2;
    this.buckets = Array.from({ length: cells * cells }, () => []);
    for (let i = 0; i < D.N; i++) {
      const [cx, cy] = this.cell(D.xy[2 * i], D.xy[2 * i + 1]);
      this.buckets[cy * cells + cx].push(i);
    }
  }
  cell(x, y) {
    const c = this.cells;
    return [
      clamp(Math.floor(((x - this.min) / this.size) * c), 0, c - 1),
      clamp(Math.floor(((y - this.min) / this.size) * c), 0, c - 1),
    ];
  }
  /** Nearest visible point within maxPx screen pixels of (px, py), or -1. */
  nearest(cam, px, py, maxPx, visible) {
    const D = this.D;
    const [wx, wy] = cam.toWorld(px, py);
    const rw = maxPx / cam.scale;
    const [x0, y0] = this.cell(wx - rw, wy - rw);
    const [x1, y1] = this.cell(wx + rw, wy + rw);
    let best = -1, bestD = maxPx * maxPx;
    for (let cy = y0; cy <= y1; cy++) {
      for (let cx = x0; cx <= x1; cx++) {
        for (const i of this.buckets[cy * this.cells + cx]) {
          if (!visible(i)) continue;
          const dx = (D.xy[2 * i] - wx) * cam.scale, dy = (D.xy[2 * i + 1] - wy) * cam.scale;
          // prefer engaging posts slightly when points overlap
          const d = dx * dx + dy * dy - D.eng[i] * 6;
          if (d < bestD) { bestD = d; best = i; }
        }
      }
    }
    return best;
  }
}

/** Wrap a label into lines no wider than maxW (px) at the context's current font. */
function wrap(ctx, text, maxW, maxLines = 3) {
  const words = text.split(/\s+/);
  const lines = [];
  let line = '';
  for (const w of words) {
    const t = line ? line + ' ' + w : w;
    if (line && ctx.measureText(t).width > maxW) {
      lines.push(line);
      line = w;
    } else line = t;
  }
  if (line) lines.push(line);
  if (lines.length > maxLines) {
    lines.length = maxLines;
    lines[maxLines - 1] += '…';
  }
  return lines;
}

export class Overlay {
  constructor(canvas) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');
    this.hit = []; // clickable labels from the last frame: {x0,y0,x1,y1,kind,id}
  }

  /**
   * view state: { cam, D, sel (selection or null), hoverTopic, hoverPoint, selectedPoint,
   *               edges: [[i, j]] | null, focusRegion }
   */
  draw(v) {
    const { cam, D } = v;
    const ctx = this.ctx;
    const dpr = cam.dpr;
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.clearRect(0, 0, this.canvas.width, this.canvas.height);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    this.hit = [];

    const z = cam.zoom;
    const W = cam.w / dpr, H = cam.h / dpr;
    const toS = (x, y) => {
      const [sx, sy] = cam.toScreen(x, y);
      return [sx / dpr, sy / dpr];
    };

    // thread edges
    if (v.edges && v.edges.length) {
      ctx.lineWidth = 1.2;
      ctx.strokeStyle = 'rgba(255,255,255,0.35)';
      ctx.beginPath();
      for (const [a, b] of v.edges) {
        const [ax, ay] = toS(D.xy[2 * a], D.xy[2 * a + 1]);
        const [bx, by] = toS(D.xy[2 * b], D.xy[2 * b + 1]);
        ctx.moveTo(ax, ay);
        ctx.lineTo(bx, by);
      }
      ctx.stroke();
    }

    // rings for hovered / selected points
    const ring = (i, color, width, extra = 0) => {
      if (i == null || i < 0) return;
      const [x, y] = toS(D.xy[2 * i], D.xy[2 * i + 1]);
      ctx.beginPath();
      ctx.arc(x, y, 7 + extra + D.eng[i] * 5, 0, Math.PI * 2);
      ctx.lineWidth = width;
      ctx.strokeStyle = color;
      ctx.stroke();
    };
    ring(v.selectedPoint, 'rgba(255,255,255,0.95)', 2, 2);
    ring(v.hoverPoint, 'rgba(255,255,255,0.7)', 1.5);

    // ---- labels
    const placed = [];
    const overlaps = (r) => placed.some((p) => r.x0 < p.x1 && r.x1 > p.x0 && r.y0 < p.y1 && r.y1 > p.y0);
    const sel = v.sel;
    const focusRegion = sel ? (sel.type === 'region' ? sel.id : sel.type === 'topic' ? D.topicById.get(sel.id)?.region : null) : null;
    const dimOthers = sel && ['region', 'topic'].includes(sel.type);

    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.lineJoin = 'round';

    const drawLabel = (lines, x, y, fontPx, color, alpha, weight, track, kind, id, strong) => {
      ctx.font = `${weight} ${fontPx}px Inter, system-ui, -apple-system, "Segoe UI", sans-serif`;
      if ('letterSpacing' in ctx) ctx.letterSpacing = track;
      const lh = fontPx * 1.18;
      const w = Math.max(...lines.map((l) => ctx.measureText(l).width)) + 6;
      const h = lines.length * lh + 4;
      const at = (yy) => ({ x0: x - w / 2, y0: yy - h / 2, x1: x + w / 2, y1: yy + h / 2, kind, id });
      let rect = at(y);
      if (rect.x1 < 0 || rect.x0 > W || rect.y1 < 0 || rect.y0 > H) return false;
      if (!strong) {
        // Nudge up or down to find a free slot before giving up on the label.
        let found = !overlaps(rect);
        for (const k of found ? [] : [1, -1, 2, -2]) {
          const r2 = at(y + k * h * 0.9);
          if (!overlaps(r2)) { rect = r2; found = true; break; }
        }
        if (!found && !strong) return false;
        y = (rect.y0 + rect.y1) / 2;
      }
      placed.push(rect);
      // Faded-out labels shouldn't swallow clicks meant for the points beneath them.
      if (alpha >= 0.35) this.hit.push(rect);
      ctx.globalAlpha = alpha;
      ctx.lineWidth = Math.max(3, fontPx * 0.32);
      ctx.strokeStyle = 'rgba(8,10,16,0.92)';
      ctx.fillStyle = color;
      lines.forEach((l, k) => {
        const ly = y - ((lines.length - 1) * lh) / 2 + k * lh;
        ctx.strokeText(l, x, ly);
        ctx.fillText(l, x, ly);
      });
      ctx.globalAlpha = 1;
      return true;
    };

    // regions first (largest claim space first), faded out as topics take over
    const regionAlpha = 1 - smoothstep(2.4, 4.2, z);
    if (regionAlpha > 0.02) {
      const regs = [...D.atlas.regions].sort((a, b) => b.n - a.n);
      // Lay the region labels out together: start at each centroid and push overlapping
      // labels apart (a few relaxation passes), pulling gently back toward the centroid.
      const items = regs.map((r) => {
        const [ax, ay] = toS(r.x, r.y);
        const fs = clamp(12 + Math.sqrt(r.n) * 0.22, 13, 22);
        ctx.font = `700 ${fs}px Inter, system-ui, sans-serif`;
        if ('letterSpacing' in ctx) ctx.letterSpacing = '0.08em';
        const lines = wrap(ctx, r.title.toUpperCase(), 190, 2);
        const w = Math.max(...lines.map((l) => ctx.measureText(l).width)) + 14;
        const h = lines.length * fs * 1.18 + 10;
        return { r, fs, lines, w, h, ax, ay, x: ax, y: ay };
      });
      for (let pass = 0; pass < 40; pass++) {
        let moved = false;
        for (let i = 0; i < items.length; i++) {
          for (let j = i + 1; j < items.length; j++) {
            const p = items[i], q = items[j];
            const ox = (p.w + q.w) / 2 - Math.abs(p.x - q.x), oy = (p.h + q.h) / 2 - Math.abs(p.y - q.y);
            if (ox <= 0 || oy <= 0) continue;
            moved = true;
            // separate along the cheaper axis; the smaller region yields more
            const wp = q.r.n / (p.r.n + q.r.n), wq = 1 - wp;
            if (oy < ox) {
              const dir = p.y <= q.y ? -1 : 1;
              p.y += dir * (oy + 1) * wp; q.y -= dir * (oy + 1) * wq;
            } else {
              const dir = p.x <= q.x ? -1 : 1;
              p.x += dir * (ox + 1) * wp; q.x -= dir * (ox + 1) * wq;
            }
          }
        }
        for (const it of items) { it.x += (it.ax - it.x) * 0.02; it.y += (it.ay - it.y) * 0.02; }
        if (!moved) break;
      }
      for (const it of items) {
        const r = it.r;
        let a = regionAlpha;
        if (dimOthers && focusRegion !== r.id) a *= 0.35;
        // a displaced label gets a thin tether to the region's centre
        if (a > 0.3 && Math.hypot(it.x - it.ax, it.y - it.ay) > 26) {
          ctx.globalAlpha = a * 0.5;
          ctx.strokeStyle = rgbCss(r.color, 1);
          ctx.lineWidth = 1;
          ctx.beginPath(); ctx.moveTo(it.ax, it.ay); ctx.lineTo(it.x, it.y); ctx.stroke();
          ctx.beginPath(); ctx.arc(it.ax, it.ay, 2.5, 0, Math.PI * 2); ctx.fillStyle = rgbCss(r.color, 1); ctx.fill();
          ctx.globalAlpha = 1;
        }
        drawLabel(it.lines, it.x, it.y, it.fs, rgbCss(brighten(r.color), 1), a, 700, '0.08em', 'region', r.id, true);
      }
    }

    // topics: appear as their on-screen size grows
    const topicBase = smoothstep(1.15, 1.9, z);
    if (topicBase > 0.02) {
      const tops = [...D.atlas.topics].sort((a, b) => b.n - a.n);
      for (const t of tops) {
        const rpx = (t.r * cam.scale) / dpr;
        let a = topicBase * smoothstep(12, 34, rpx);
        const hovered = v.hoverTopic === t.id;
        const selected = sel?.type === 'topic' && sel.id === t.id;
        if (hovered || selected) a = Math.max(a, 1);
        if (dimOthers && focusRegion != null && t.region !== focusRegion) a *= 0.3;
        if (a < 0.05) continue;
        const [x, y] = toS(t.x, t.y);
        const fs = clamp(10.5 + Math.log2(1 + t.n) * 0.7, 11, 15);
        ctx.font = `600 ${fs}px Inter, system-ui, sans-serif`;
        const lines = wrap(ctx, t.title, 150, 3);
        drawLabel(lines, x, y, fs, rgbCss(brighten(t.color, 0.35), 1), a, 600, '0', 'topic', t.id, hovered || selected);
      }
    }
    if ('letterSpacing' in ctx) ctx.letterSpacing = '0px';
  }

  /** The label under a screen point (CSS px), if any. */
  labelAt(x, y) {
    for (let i = this.hit.length - 1; i >= 0; i--) {
      const r = this.hit[i];
      if (x >= r.x0 && x <= r.x1 && y >= r.y0 && y <= r.y1) return r;
    }
    return null;
  }
}

function brighten([r, g, b], amt = 0.2) {
  const f = (v) => Math.round(v + (255 - v) * amt);
  return [f(r), f(g), f(b)];
}
