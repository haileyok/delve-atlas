// Do the dots the GPU draws sit where the overlay/hit-testing projection says they do?
// For sample points in several camera positions, read back the rendered pixels at the
// projected location, at the vertically mirrored location (the bug), and at a shifted control.
export default async (page) => {
  await page.goto('http://127.0.0.1:8088/');
  await page.waitFor('window.__atlas && window.__atlas.D');
  await page.wait(800);
  const rows = [];
  for (const view of [
    { name: 'home', cx: 0, cy: 0, zoom: 1 },
    { name: 'panned up/right, zoom 3', cx: 0.35, cy: 0.4, zoom: 3 },
    { name: 'panned down/left, zoom 6', cx: -0.3, cy: -0.35, zoom: 6 },
  ]) {
    rows.push(await page.eval(`(() => {
      const { D, cam, renderer } = window.__atlas;
      cam.flyTo(${view.cx}, ${view.cy}, cam.fitScale * ${view.zoom}, false);
      const gl = renderer.gl, W = gl.canvas.width, H = gl.canvas.height;
      renderer.draw({ cx: cam.cx, cy: cam.cy, scale: cam.scale, w: cam.w, h: cam.h, dpr: cam.dpr, zoomSize: cam.zoomSize });
      const px = new Uint8Array(W * H * 4);
      gl.readPixels(0, 0, W, H, gl.RGBA, gl.UNSIGNED_BYTE, px);
      const lit = (x, y) => {           // x, y in canvas px measured from the top-left
        let best = 0;
        for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) {
          const xx = Math.round(x) + dx, yy = Math.round(y) + dy;
          if (xx < 0 || yy < 0 || xx >= W || yy >= H) continue;
          const o = ((H - 1 - yy) * W + xx) * 4;   // readPixels rows run bottom-up
          best = Math.max(best, px[o] + px[o + 1] + px[o + 2]);
        }
        return best > 200;
      };
      let n = 0, here = 0, mirrored = 0, control = 0;
      for (let i = 0; i < D.N && n < 400; i += 7) {
        const [sx, sy] = cam.toScreen(D.xy[2 * i], D.xy[2 * i + 1]);   // what labels and hover use
        if (sx < 30 || sy < 30 || sx > W - 30 || sy > H - 30) continue;
        n++;
        if (lit(sx, sy)) here++;
        if (lit(sx, H - sy)) mirrored++;            // the flipped position
        if (lit(sx + 17, sy + 11)) control++;       // shifted control: how often empty space is "lit"
      }
      return { view: ${JSON.stringify(view.name)}, sampled: n, atProjection: +(here / n).toFixed(2), atMirror: +(mirrored / n).toFixed(2), control: +(control / n).toFixed(2) };
    })()`));
  }
  return { rows, logs: page.logs };
};
