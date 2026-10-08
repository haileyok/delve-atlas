// Alignment of rendered dots vs the overlay's projection AFTER real wheel and drag input,
// at the display's pixel ratio (DPR=2 node tools/cdp.mjs … for a retina-like screen).
export const options = { dpr: Number(process.env.DPR || 1) };

const PROBE = `(() => {
  const { D, cam, renderer } = window.__atlas;
  const gl = renderer.gl, W = gl.canvas.width, H = gl.canvas.height;
  renderer.draw({ cx: cam.cx, cy: cam.cy, scale: cam.scale, w: cam.w, h: cam.h, dpr: cam.dpr, zoomSize: cam.zoomSize });
  const px = new Uint8Array(W * H * 4);
  gl.readPixels(0, 0, W, H, gl.RGBA, gl.UNSIGNED_BYTE, px);
  const lit = (x, y) => {
    let best = 0;
    for (let dy = -3; dy <= 3; dy++) for (let dx = -3; dx <= 3; dx++) {
      const xx = Math.round(x) + dx, yy = Math.round(y) + dy;
      if (xx < 0 || yy < 0 || xx >= W || yy >= H) continue;
      const o = ((H - 1 - yy) * W + xx) * 4;
      best = Math.max(best, px[o] + px[o + 1] + px[o + 2]);
    }
    return best > 200;
  };
  let n = 0, here = 0, mirrorY = 0, mirrorX = 0;
  for (let i = 0; i < D.N && n < 400; i += 5) {
    const [sx, sy] = cam.toScreen(D.xy[2 * i], D.xy[2 * i + 1]);
    if (sx < 40 || sy < 40 || sx > W - 40 || sy > H - 40) continue;
    n++;
    if (lit(sx, sy)) here++;
    if (lit(sx, H - sy)) mirrorY++;
    if (lit(W - sx, sy)) mirrorX++;
  }
  return { dpr: cam.dpr, canvas: [W, H], zoom: +cam.zoom.toFixed(2), centre: [+cam.cx.toFixed(3), +cam.cy.toFixed(3)], sampled: n,
    atProjection: n ? +(here / n).toFixed(2) : null, atYMirror: n ? +(mirrorY / n).toFixed(2) : null, atXMirror: n ? +(mirrorX / n).toFixed(2) : null };
})()`;

export default async (page) => {
  await page.goto('http://127.0.0.1:8088/');
  await page.waitFor('window.__atlas && window.__atlas.D');
  await page.wait(800);
  const r = await page.eval(`(() => { const b = document.querySelector('#stage').getBoundingClientRect(); return [b.left, b.top, b.width, b.height]; })()`);
  const [sx, sy, sw, sh] = r;
  const steps = [];
  const probe = async (name) => { await page.wait(250); steps.push({ step: name, ...(await page.eval(PROBE)) }); };

  await probe('initial');
  // zoom in with the wheel at an off-centre cursor (upper-left third)
  for (let k = 0; k < 4; k++) { await page.wheel(sx + sw * 0.3, sy + sh * 0.3, -240); await page.wait(40); }
  await probe('after wheel zoom at off-centre cursor');
  // drag the map down-right then up-left, like panning around
  await page.drag(sx + sw * 0.4, sy + sh * 0.4, sx + sw * 0.6, sy + sh * 0.7);
  await probe('after drag down-right');
  await page.drag(sx + sw * 0.7, sy + sh * 0.7, sx + sw * 0.3, sy + sh * 0.35);
  await probe('after drag up-left');
  // zoom back out at the opposite corner
  for (let k = 0; k < 3; k++) { await page.wheel(sx + sw * 0.8, sy + sh * 0.8, 240); await page.wait(40); }
  await probe('after wheel zoom out at lower-right cursor');

  // and: does the content move the same way as the labels? Compare one label and one dot before/after a drag.
  const before = await page.eval(`(() => { const { D, cam, overlay } = window.__atlas; const i = 0; const [x, y] = cam.toScreen(D.xy[0], D.xy[1]); return { x, y }; })()`);
  await page.drag(sx + sw * 0.5, sy + sh * 0.5, sx + sw * 0.5 + 100, sy + sh * 0.5 + 60);
  await page.wait(200);
  const after = await page.eval(`(() => { const { D, cam } = window.__atlas; const [x, y] = cam.toScreen(D.xy[0], D.xy[1]); return { x, y }; })()`);
  return { steps, dragMovedPointBy: { dx: +(after.x - before.x).toFixed(1), dy: +(after.y - before.y).toFixed(1), expectedDevicePx: [100 * (steps[0].dpr), 60 * (steps[0].dpr)] }, logs: page.logs };
};
