export default async (page) => {
  await page.goto((process.env.BASE ?? 'http://127.0.0.1:8088') + '/');
  await page.waitFor('window.__atlas && window.__atlas.D');
  await page.wait(800);
  return page.eval(`(() => {
    const { overlay, D } = window.__atlas;
    const hits = overlay.hit, out = [];
    for (let i = 0; i < hits.length; i++) for (let j = i + 1; j < hits.length; j++) {
      const a = hits[i], b = hits[j];
      if (a.x0 < b.x1 && a.x1 > b.x0 && a.y0 < b.y1 && a.y1 > b.y0) out.push([a, b].map((h) => ({ title: D.regionById.get(h.id)?.title, n: D.regionById.get(h.id)?.n, rect: [h.x0, h.y0, h.x1, h.y1].map(Math.round) })));
    }
    return { all: hits.map((h) => [D.regionById.get(h.id)?.title, Math.round(h.x0), Math.round(h.y0), Math.round(h.x1), Math.round(h.y1)]), out };
  })()`);
};
