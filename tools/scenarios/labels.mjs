// Reports how many labels are visible at each zoom level and whether any overlap.
export default async (page) => {
  await page.goto('http://127.0.0.1:8088/');
  await page.waitFor('window.__atlas && window.__atlas.D');
  await page.wait(1000);
  const rows = [];
  for (const z of [1, 1.6, 2.2, 3, 4.5, 7, 12, 25]) {
    const r = await page.eval(`(async () => {
      const { cam, overlay } = window.__atlas;
      cam.flyTo(0, 0, cam.fitScale * ${z}, false);
      await new Promise((res) => setTimeout(res, 150));
      const hits = overlay.hit;
      let overlaps = 0;
      for (let i = 0; i < hits.length; i++) for (let j = i + 1; j < hits.length; j++) {
        const a = hits[i], b = hits[j];
        if (a.x0 < b.x1 && a.x1 > b.x0 && a.y0 < b.y1 && a.y1 > b.y0) overlaps++;
      }
      return { zoom: ${z}, region: hits.filter((h) => h.kind === 'region').length, topic: hits.filter((h) => h.kind === 'topic').length, overlaps };
    })()`);
    rows.push(r);
  }
  return { rows, logs: page.logs };
};
