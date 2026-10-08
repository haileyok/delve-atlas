// Loads the atlas, checks for errors, and reports what is on screen.
export default async (page) => {
  await page.goto((process.env.BASE ?? 'http://127.0.0.1:8088') + '/');
  await page.waitFor('window.__atlas && window.__atlas.D');
  await page.wait(1500);
  const info = await page.eval(`(() => {
    const { D, cam, renderer } = window.__atlas;
    // read back the canvas to see that points were really drawn
    const gl = renderer.gl, w = gl.canvas.width, h = gl.canvas.height;
    // redraw and read back within the same task (the buffer is cleared once composited)
    renderer.draw({ cx: cam.cx, cy: cam.cy, scale: cam.scale, w: cam.w, h: cam.h, dpr: cam.dpr, zoomSize: cam.zoomSize });
    const px = new Uint8Array(w * h * 4);
    gl.readPixels(0, 0, w, h, gl.RGBA, gl.UNSIGNED_BYTE, px);
    let lit = 0;
    for (let i = 0; i < px.length; i += 4) if (px[i] + px[i + 1] + px[i + 2] > 120) lit++;
    return {
      points: D.N, topics: D.atlas.topics.length, regions: D.atlas.regions.length,
      canvas: [w, h], zoom: cam.zoom, litPixels: lit, litFraction: +(lit / (w * h)).toFixed(4),
      outlineRows: document.querySelectorAll('.o-region').length,
      meta: document.querySelector('#meta').textContent,
    };
  })()`);
  await page.screenshot('/tmp/smoke.png');
  return { info, logs: page.logs };
};
