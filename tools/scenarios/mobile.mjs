// A phone-sized viewport: the map should load, the sidebar should stay out of the way, and a tap
// on a point should open the detail drawer without errors.
export const options = { width: 390, height: 844 };
export default async (page) => {
  await page.goto('http://127.0.0.1:8088/');
  await page.waitFor('window.__atlas && window.__atlas.D');
  await page.wait(1000);
  const out = {};
  out.layout = await page.eval(`(() => {
    const r = (s) => { const e = document.querySelector(s); if (!e) return null; const b = e.getBoundingClientRect(); return [Math.round(b.left), Math.round(b.top), Math.round(b.width), Math.round(b.height)]; };
    return { stage: r('#stage'), sidebarVisible: getComputedStyle(document.querySelector('#sidebar')).display !== 'none', timeline: r('#timeline-wrap'), header: r('#header') };
  })()`);
  const target = await page.eval(`(() => {
    const { D, cam } = window.__atlas, hits = window.__atlas.overlay.hit;
    const stage = document.querySelector('#stage').getBoundingClientRect();
    const sx = (i) => ((D.xy[2*i] - cam.cx) * cam.scale + cam.w / 2) / cam.dpr, sy = (i) => ((D.xy[2*i+1] - cam.cy) * -cam.scale + cam.h / 2) / cam.dpr;
    const free = (i) => !hits.some((h) => sx(i) >= h.x0 && sx(i) <= h.x1 && sy(i) >= h.y0 && sy(i) <= h.y1) && sy(i) < stage.height - 140;
    let best = -1; for (let i = 0; i < D.N; i++) if (free(i) && (best < 0 || D.eng[i] > D.eng[best])) best = i;
    return { i: best, x: stage.left + sx(best), y: stage.top + sy(best) };
  })()`);
  await page.click(target.x, target.y);
  await page.wait(900);
  out.afterTap = await page.eval(`({ sel: window.__atlas.S.sel?.type, drawerOpen: document.querySelector('#drawer').classList.contains('open'), drawerBox: (() => { const b = document.querySelector('#drawer').getBoundingClientRect(); return [Math.round(b.left), Math.round(b.top), Math.round(b.width), Math.round(b.height)]; })() })`);
  out.logs = page.logs;
  await page.screenshot('/tmp/mobile.png');
  return out;
};
