// Bottom timeline: stacked hourly activity by region, with a brush that filters the map by time.

import { clamp, fmtDay, fmtDateTime, rgbCss } from './util.js';

export class Timeline {
  constructor(host, D, onRange) {
    this.D = D;
    this.onRange = onRange;
    const act = D.atlas.activity;
    this.t0 = act.t0;
    this.step = act.step;
    this.bins = act.series[0].length;
    this.tEnd = this.t0 + this.bins * this.step;
    this.fullMin = D.atlas.ts_range[0];
    this.fullMax = D.atlas.ts_range[1];
    this.range = [this.fullMin, this.fullMax];
    this.overlay = null;
    this.playing = false;

    // Stack order: biggest region at the bottom.
    const regs = D.atlas.regions.map((r, k) => ({ r, k })).sort((a, b) => b.r.n - a.r.n);
    this.layers = regs.map(({ r, k }) => ({ color: r.color, data: act.series[k], id: r.id }));
    this.layers.push({ color: D.noise.color, data: act.series[act.series.length - 1], id: -1 });
    this.stackMax = 1;
    for (let b = 0; b < this.bins; b++) {
      let s = 0;
      for (const l of this.layers) s += l.data[b];
      this.stackMax = Math.max(this.stackMax, s);
    }

    this.canvas = document.createElement('canvas');
    this.canvas.className = 'timeline-canvas';
    host.append(this.canvas);
    this.ctx = this.canvas.getContext('2d');
    this.drag = null;
    this.pad = { l: 44, r: 14, t: 8, b: 20 };

    const c = this.canvas;
    c.addEventListener('pointerdown', (e) => this.down(e));
    c.addEventListener('pointermove', (e) => this.move(e));
    c.addEventListener('pointerup', (e) => this.up(e));
    c.addEventListener('pointerleave', () => { if (!this.drag) { c.style.cursor = 'crosshair'; } });
    c.addEventListener('dblclick', () => this.reset());
    new ResizeObserver(() => this.resize()).observe(host);
    this.resize();
  }

  resize() {
    const r = this.canvas.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    this.cw = r.width;
    this.ch = r.height;
    this.canvas.width = Math.round(r.width * dpr);
    this.canvas.height = Math.round(r.height * dpr);
    this.dpr = dpr;
    this.draw();
  }

  get plotW() { return this.cw - this.pad.l - this.pad.r; }
  get plotH() { return this.ch - this.pad.t - this.pad.b; }
  tToX(t) { return this.pad.l + ((t - this.t0) / (this.tEnd - this.t0)) * this.plotW; }
  xToT(x) { return this.t0 + ((x - this.pad.l) / this.plotW) * (this.tEnd - this.t0); }

  setOverlay(hist) {
    this.overlay = hist;
    this.draw();
  }

  /** hist of timestamps (point indices) into this timeline's bins */
  histogram(indices) {
    const h = new Array(this.bins).fill(0);
    for (const i of indices) {
      const b = Math.floor((this.D.ts[i] - this.t0) / this.step);
      if (b >= 0 && b < this.bins) h[b]++;
    }
    return h;
  }

  setRange(a, b, silent = false) {
    this.range = [clamp(Math.min(a, b), this.fullMin, this.fullMax), clamp(Math.max(a, b), this.fullMin, this.fullMax)];
    this.draw();
    if (!silent) this.onRange(this.range[0], this.range[1]);
  }

  reset() {
    this.stop();
    this.setRange(this.fullMin, this.fullMax);
  }

  isFull() {
    return this.range[0] <= this.fullMin + 1 && this.range[1] >= this.fullMax - 1;
  }

  // ---- playback: the map fills in over time
  togglePlay() {
    this.playing ? this.stop() : this.play();
    return this.playing;
  }
  play() {
    this.playing = true;
    const start = performance.now();
    const dur = 16000;
    const t0 = this.fullMin, span = this.fullMax - this.fullMin;
    const tick = (now) => {
      if (!this.playing) return;
      const p = clamp((now - start) / dur, 0, 1);
      this.setRange(t0, t0 + Math.max(3600, span * p));
      if (p >= 1) { this.stop(); return; }
      this.raf = requestAnimationFrame(tick);
    };
    this.raf = requestAnimationFrame(tick);
    this.onPlayState?.(true);
  }
  stop() {
    this.playing = false;
    cancelAnimationFrame(this.raf);
    this.onPlayState?.(false);
  }

  // ---- brush
  hitTest(x) {
    const x0 = this.tToX(this.range[0]), x1 = this.tToX(this.range[1]);
    if (Math.abs(x - x0) < 7) return 'left';
    if (Math.abs(x - x1) < 7) return 'right';
    if (x > x0 && x < x1 && !this.isFull()) return 'move';
    return 'new';
  }
  pos(e) {
    const r = this.canvas.getBoundingClientRect();
    return e.clientX - r.left;
  }
  down(e) {
    this.stop();
    const x = this.pos(e);
    const mode = this.hitTest(x);
    this.canvas.setPointerCapture(e.pointerId);
    this.drag = { mode, x0: x, range: [...this.range], anchor: this.xToT(x) };
    if (mode === 'new') this.drag.range = [this.xToT(x), this.xToT(x)];
  }
  move(e) {
    const x = this.pos(e);
    if (!this.drag) {
      const m = this.hitTest(x);
      this.canvas.style.cursor = m === 'left' || m === 'right' ? 'ew-resize' : m === 'move' ? 'grab' : 'crosshair';
      this.hover = x;
      this.draw();
      return;
    }
    const d = this.drag, t = this.xToT(x);
    const min = 3600;
    if (d.mode === 'left') this.setRange(Math.min(t, d.range[1] - min), d.range[1]);
    else if (d.mode === 'right') this.setRange(d.range[0], Math.max(t, d.range[0] + min));
    else if (d.mode === 'move') {
      const dt = t - d.anchor, span = d.range[1] - d.range[0];
      const a = clamp(d.range[0] + dt, this.fullMin, this.fullMax - span);
      this.setRange(a, a + span);
    } else this.setRange(d.anchor, t);
  }
  up(e) {
    if (!this.drag) return;
    const d = this.drag;
    this.drag = null;
    // a plain click on the chart (no drag) clears the filter
    if (d.mode === 'new' && Math.abs(this.pos(e) - d.x0) < 3) this.reset();
    else if (this.range[1] - this.range[0] < 3600) this.reset();
  }

  draw() {
    const { ctx, dpr, pad } = this;
    const W = this.cw, H = this.ch;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, W, H);
    const bw = this.plotW / this.bins;
    const base = pad.t + this.plotH;
    const sy = this.plotH / this.stackMax;

    // stacked bars
    const acc = new Float64Array(this.bins);
    for (const l of this.layers) {
      ctx.fillStyle = rgbCss(l.color, 0.85);
      for (let b = 0; b < this.bins; b++) {
        const v = l.data[b];
        if (!v) continue;
        const y1 = base - acc[b] * sy, y0 = y1 - v * sy;
        ctx.fillRect(this.pad.l + b * bw, y0, Math.max(bw - 0.4, 0.6), y1 - y0);
        acc[b] += v;
      }
    }

    // selection overlay as a white outline of its own hourly volume
    if (this.overlay) {
      ctx.beginPath();
      ctx.strokeStyle = 'rgba(255,255,255,0.95)';
      ctx.lineWidth = 1.6;
      for (let b = 0; b < this.bins; b++) {
        const x = this.pad.l + b * bw, y = base - this.overlay[b] * sy;
        b === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y);
        ctx.lineTo(x + bw, y);
      }
      ctx.stroke();
    }

    // day ticks
    ctx.fillStyle = 'rgba(160,168,184,0.8)';
    ctx.font = '11px Inter, system-ui, sans-serif';
    ctx.textBaseline = 'top';
    ctx.textAlign = 'left';
    const day0 = new Date(this.t0 * 1000);
    day0.setHours(24, 0, 0, 0);
    ctx.strokeStyle = 'rgba(160,168,184,0.18)';
    ctx.lineWidth = 1;
    for (let t = day0.getTime() / 1000; t < this.tEnd; t += 86400) {
      const x = this.tToX(t);
      ctx.beginPath();
      ctx.moveTo(x, pad.t);
      ctx.lineTo(x, base);
      ctx.stroke();
      ctx.fillText(fmtDay(t), x + 3, base + 4);
    }
    ctx.textAlign = 'right';
    ctx.textBaseline = 'middle';
    ctx.fillText(`${this.stackMax}/h`, pad.l - 6, pad.t + 6);
    ctx.fillText('0', pad.l - 6, base - 4);

    // brush
    if (!this.isFull()) {
      const x0 = this.tToX(this.range[0]), x1 = this.tToX(this.range[1]);
      ctx.fillStyle = 'rgba(8,10,16,0.62)';
      ctx.fillRect(pad.l, pad.t, Math.max(0, x0 - pad.l), this.plotH);
      ctx.fillRect(x1, pad.t, Math.max(0, pad.l + this.plotW - x1), this.plotH);
      ctx.strokeStyle = 'rgba(255,255,255,0.9)';
      ctx.lineWidth = 2;
      ctx.beginPath();
      ctx.moveTo(x0, pad.t); ctx.lineTo(x0, base);
      ctx.moveTo(x1, pad.t); ctx.lineTo(x1, base);
      ctx.stroke();
      ctx.fillStyle = '#fff';
      ctx.textAlign = 'center';
      ctx.textBaseline = 'top';
      ctx.font = '600 11px Inter, system-ui, sans-serif';
      const label = `${fmtDateTime(this.range[0])} → ${fmtDateTime(this.range[1])}`;
      const cx = clamp((x0 + x1) / 2, pad.l + 110, W - pad.r - 110);
      const tw = ctx.measureText(label).width;
      ctx.fillStyle = 'rgba(8,10,16,0.8)';
      ctx.fillRect(cx - tw / 2 - 5, pad.t + 1, tw + 10, 16);
      ctx.fillStyle = '#fff';
      ctx.fillText(label, cx, pad.t + 4);
    } else if (this.hover != null && !this.drag) {
      const t = this.xToT(this.hover);
      if (t >= this.t0 && t <= this.tEnd) {
        ctx.strokeStyle = 'rgba(255,255,255,0.35)';
        ctx.beginPath();
        ctx.moveTo(this.hover, pad.t);
        ctx.lineTo(this.hover, base);
        ctx.stroke();
      }
    }
  }
}
