// Small helpers shared by the modules.

export const $ = (sel, root = document) => root.querySelector(sel);

/** Create an element: el('div', {class: 'x', onclick: fn}, child, 'text'). */
export function el(tag, attrs = {}, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') n.className = v;
    else if (k === 'style' && typeof v === 'object') Object.assign(n.style, v);
    else if (k.startsWith('on') && typeof v === 'function') n.addEventListener(k.slice(2), v);
    else if (k === 'html') n.innerHTML = v;
    else n.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    n.append(c.nodeType ? c : document.createTextNode(String(c)));
  }
  return n;
}

export const fmt = new Intl.NumberFormat('en-US');
export const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 });

export function ago(unixSeconds, now = Date.now() / 1000) {
  const d = Math.max(0, now - unixSeconds);
  if (d < 90) return 'just now';
  if (d < 3600) return `${Math.round(d / 60)}m ago`;
  if (d < 86400) return `${Math.round(d / 3600)}h ago`;
  return `${Math.round(d / 86400)}d ago`;
}

const dayFmt = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric' });
const timeFmt = new Intl.DateTimeFormat('en-US', { hour: 'numeric', minute: '2-digit' });
export const fmtDay = (s) => dayFmt.format(new Date(s * 1000));
export const fmtDateTime = (s) => `${dayFmt.format(new Date(s * 1000))}, ${timeFmt.format(new Date(s * 1000))}`;

export function debounce(fn, ms) {
  let t;
  return (...a) => {
    clearTimeout(t);
    t = setTimeout(() => fn(...a), ms);
  };
}

export const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v));
export const lerp = (a, b, t) => a + (b - a) * t;
export const smoothstep = (a, b, x) => {
  const t = clamp((x - a) / (b - a), 0, 1);
  return t * t * (3 - 2 * t);
};

/** HSL (h in degrees, s/l in 0..1) to [r,g,b] bytes. */
export function hsl(h, s, l) {
  h = ((h % 360) + 360) % 360;
  const k = (n) => (n + h / 30) % 12;
  const a = s * Math.min(l, 1 - l);
  const f = (n) => l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
  return [Math.round(f(0) * 255), Math.round(f(8) * 255), Math.round(f(4) * 255)];
}

export const rgbCss = ([r, g, b], a = 1) => `rgba(${r},${g},${b},${a})`;

/** The post's name for display: "Display Name" and "@handle", falling back to the DID tail. */
export function authorName(a) {
  return a.name || a.handle || a.did.slice(-8);
}
export function authorHandle(a) {
  return a.handle ? '@' + a.handle : a.did.slice(0, 18) + '…';
}

export function rkeyOf(uri) {
  return uri.slice(uri.lastIndexOf('/') + 1);
}

/** Link to a post on the Delve site (the same URL shape as the Bluesky app's). */
export function postUrl(uri) {
  const m = /^at:\/\/([^/]+)\/[^/]+\/([^/]+)$/.exec(uri);
  return m ? `https://delve.town/profile/${m[1]}/post/${m[2]}` : '#';
}
