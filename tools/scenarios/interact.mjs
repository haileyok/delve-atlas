// Drives the UI like a user would and reports what each step did.
export default async (page) => {
  const out = {};
  await page.goto('http://127.0.0.1:8088/');
  await page.waitFor('window.__atlas && window.__atlas.D');
  await page.wait(1200);

  const stageRect = await page.eval(`(() => { const r = document.querySelector('#stage').getBoundingClientRect(); return [r.left, r.top, r.width, r.height]; })()`);
  const [sx, sy, sw, sh] = stageRect;
  // screen position (page coords) of the layout point at (x, y)
  const toPage = (cam, x, y) => {
    const px = ((x - cam.cx) * cam.scale + cam.w / 2) / cam.dpr, py = ((y - cam.cy) * -cam.scale + cam.h / 2) / cam.dpr;
    return [sx + px, sy + py];
  };
  const camState = () => page.eval(`(() => { const c = window.__atlas.cam; return { cx: c.cx, cy: c.cy, scale: c.scale, w: c.w, h: c.h, dpr: c.dpr, zoom: c.zoom }; })()`);
  const labels = () => page.eval(`(() => {
    const o = document.querySelector('#overlay'); // labels are tracked on the Overlay instance via hit rects
    return null;
  })()`);

  // expose the overlay for inspection
  await page.eval(`(() => { const a = window.__atlas; })()`);

  const overlayInfo = (label) => page.eval(`(() => {
    const hits = window.__atlasOverlay ? window.__atlasOverlay.hit : [];
    return hits.length;
  })()`);

  // 1. home view: region labels
  out.home = await page.eval(`(() => ({ zoom: window.__atlas.cam.zoom, sel: window.__atlas.S.sel }))()`);

  // 2. zoom in at the centre with the wheel; topics should start to get labels
  for (let k = 0; k < 6; k++) { await page.wheel(sx + sw / 2, sy + sh / 2, -300); await page.wait(60); }
  await page.wait(300);
  out.zoomed = await camState();

  // 3. click a region label by finding it through the overlay hit list
  await page.eval(`window.__atlas.cam.home(false)`);
  await page.wait(300);
  const regionLabel = await page.eval(`(() => {
    const ov = window.__atlas.overlay;
    const r = ov.hit.find((h) => h.kind === 'region');
    return r ? { x: (r.x0 + r.x1) / 2, y: (r.y0 + r.y1) / 2, id: r.id, count: ov.hit.length } : null;
  })()`);
  out.regionLabel = regionLabel;
  if (regionLabel) {
    await page.click(sx + regionLabel.x, sy + regionLabel.y);
    await page.wait(1100);
    out.afterRegionClick = await page.eval(`(() => ({
      sel: window.__atlas.S.sel, zoom: window.__atlas.cam.zoom,
      drawerOpen: document.querySelector('#drawer').classList.contains('open'),
      drawerTitle: document.querySelector('#drawer h2')?.textContent,
      hash: location.hash,
      topicLabels: window.__atlas.overlay.hit.filter((h) => h.kind === 'topic').length,
    }))()`);
  }

  // 4. escape clears
  await page.key('Escape');
  await page.wait(200);
  out.afterEsc = await page.eval(`({ sel: window.__atlas.S.sel, drawerOpen: document.querySelector('#drawer').classList.contains('open') })`);

  // 5. hover then click a point near the middle of the busiest topic
  await page.eval(`window.__atlas.cam.home(false)`);
  await page.wait(200);
  const cam = await camState();
  const target = await page.eval(`(() => {
    const { D } = window.__atlas;
    // the most engaged post that is not under a label (labels take clicks first)
    const hits = window.__atlas.overlay.hit, c = window.__atlas.cam;
    const underLabel = (i) => {
      const x = ((D.xy[2 * i] - c.cx) * c.scale + c.w / 2) / c.dpr, y = ((D.xy[2 * i + 1] - c.cy) * -c.scale + c.h / 2) / c.dpr;
      return hits.some((h) => x >= h.x0 && x <= h.x1 && y >= h.y0 && y <= h.y1);
    };
    let best = -1; for (let i = 0; i < D.N; i++) if (!underLabel(i) && (best < 0 || D.eng[i] > D.eng[best])) best = i;
    return { i: best, x: D.xy[2 * best], y: D.xy[2 * best + 1], text: D.text[best].slice(0, 80) };
  })()`);
  const [px, py] = toPage(cam, target.x, target.y);
  await page.move(px, py);
  await page.wait(250);
  out.hover = await page.eval(`(() => { const t = document.querySelector('#tooltip'); return { visible: t.style.display === 'block', text: t.textContent.slice(0, 120) }; })()`);
  await page.click(px, py);
  await page.wait(900);
  out.afterPointClick = await page.eval(`(() => ({
    sel: window.__atlas.S.sel, drawerOpen: document.querySelector('#drawer').classList.contains('open'),
    bigText: document.querySelector('.big-text')?.textContent.slice(0, 80), hash: location.hash.slice(0, 60),
  }))()`);
  out.target = target;

  // 6. search
  await page.key('Escape');
  await page.eval(`(() => { const s = document.querySelector('#search'); s.value = 'random'; s.dispatchEvent(new Event('input')); })()`);
  await page.wait(1200);
  out.search = await page.eval(`(() => ({ sel: window.__atlas.S.sel, matches: window.__atlas.S.matches.length, title: document.querySelector('#drawer h2')?.textContent }))()`);

  // 7. timeline brush: drag across the middle of the timeline
  await page.key('Escape');
  await page.eval(`(() => { const s = document.querySelector('#search'); s.value = ''; s.dispatchEvent(new Event('input')); })()`);
  await page.wait(400);
  const tl = await page.eval(`(() => { const r = document.querySelector('.timeline-canvas').getBoundingClientRect(); return [r.left, r.top, r.width, r.height]; })()`);
  await page.drag(tl[0] + tl[2] * 0.3, tl[1] + tl[3] * 0.5, tl[0] + tl[2] * 0.6, tl[1] + tl[3] * 0.5);
  await page.wait(500);
  out.brush = await page.eval(`(() => ({ range: window.__atlas.S.range, full: window.__atlas.timeline.isFull(), info: document.querySelector('#range-info').textContent }))()`);

  await page.screenshot('/tmp/interact.png');
  out.logs = page.logs;
  return out;
};
