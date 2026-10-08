// WebGL2 point renderer: one sprite per post, colour/state/time filtering all on the GPU.

const VERT = `#version 300 es
in vec2 a_pos;
in float a_eng;
in float a_ts;
in vec4 a_color;
in float a_state;
uniform vec2 u_center;
uniform vec2 u_scale;      // clip units per layout unit; both axes point the same way as the layout
uniform float u_size;      // base sprite size in device px
uniform float u_zoom;
uniform float u_t0;
uniform float u_t1;
uniform int u_pass;        // 0: everything not hot, 1: hot points
uniform float u_dimAlpha;
out vec4 v_color;
void main() {
  bool hot = a_state > 1.5;
  bool inTime = a_ts >= u_t0 && a_ts <= u_t1;
  if (((u_pass == 1) != hot) || !inTime) {
    gl_Position = vec4(2.0, 2.0, 2.0, 1.0);
    gl_PointSize = 0.0;
    v_color = vec4(0.0);
    return;
  }
  gl_Position = vec4((a_pos - u_center) * u_scale, 0.0, 1.0);
  float size = u_size * (0.75 + 1.25 * a_eng) * u_zoom;
  if (hot) size *= 1.45;
  gl_PointSize = clamp(size, 1.6, 44.0);
  float alpha = a_state < 0.5 ? u_dimAlpha : (hot ? 1.0 : 0.8);
  v_color = vec4(a_color.rgb, alpha);
}`;

const FRAG = `#version 300 es
precision mediump float;
in vec4 v_color;
out vec4 outColor;
void main() {
  vec2 c = gl_PointCoord * 2.0 - 1.0;
  float d = dot(c, c);
  if (d > 1.0) discard;
  float a = v_color.a * smoothstep(1.0, 0.55, d);
  outColor = vec4(v_color.rgb * a, a);   // premultiplied
}`;

function compile(gl, type, src) {
  const s = gl.createShader(type);
  gl.shaderSource(s, src);
  gl.compileShader(s);
  if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s));
  return s;
}

export class PointRenderer {
  constructor(canvas, D) {
    const gl = canvas.getContext('webgl2', { antialias: false, alpha: false, premultipliedAlpha: true });
    if (!gl) throw new Error('WebGL2 is not available in this browser');
    this.gl = gl;
    this.N = D.N;
    const prog = gl.createProgram();
    gl.attachShader(prog, compile(gl, gl.VERTEX_SHADER, VERT));
    gl.attachShader(prog, compile(gl, gl.FRAGMENT_SHADER, FRAG));
    gl.linkProgram(prog);
    if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(prog));
    this.prog = prog;
    this.u = {};
    for (const n of ['u_center', 'u_scale', 'u_size', 'u_zoom', 'u_t0', 'u_t1', 'u_pass', 'u_dimAlpha']) {
      this.u[n] = gl.getUniformLocation(prog, n);
    }
    this.vao = gl.createVertexArray();
    gl.bindVertexArray(this.vao);

    const buf = (data, name, size, type, normalized, usage) => {
      const b = gl.createBuffer();
      gl.bindBuffer(gl.ARRAY_BUFFER, b);
      gl.bufferData(gl.ARRAY_BUFFER, data, usage);
      const loc = gl.getAttribLocation(prog, name);
      gl.enableVertexAttribArray(loc);
      gl.vertexAttribPointer(loc, size, type, normalized, 0, 0);
      return b;
    };
    buf(D.xy, 'a_pos', 2, gl.FLOAT, false, gl.STATIC_DRAW);
    buf(D.eng, 'a_eng', 1, gl.FLOAT, false, gl.STATIC_DRAW);
    this.tsMin = D.atlas.ts_range[0];
    const rel = new Float32Array(D.N);
    for (let i = 0; i < D.N; i++) rel[i] = D.ts[i] - this.tsMin;
    buf(rel, 'a_ts', 1, gl.FLOAT, false, gl.STATIC_DRAW);
    this.colorBuf = buf(new Uint8Array(D.N * 4), 'a_color', 4, gl.UNSIGNED_BYTE, true, gl.DYNAMIC_DRAW);
    this.stateBuf = buf(new Uint8Array(D.N), 'a_state', 1, gl.UNSIGNED_BYTE, false, gl.DYNAMIC_DRAW);

    this.t0 = 0;
    this.t1 = 1e12;
    this.dimAlpha = 0.07;
    gl.disable(gl.DEPTH_TEST);
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA);
  }

  setColors(rgba) {
    const gl = this.gl;
    gl.bindBuffer(gl.ARRAY_BUFFER, this.colorBuf);
    gl.bufferSubData(gl.ARRAY_BUFFER, 0, rgba);
  }

  /** state: Uint8Array, 0 = dimmed, 1 = normal, 2 = highlighted. */
  setState(state) {
    const gl = this.gl;
    gl.bindBuffer(gl.ARRAY_BUFFER, this.stateBuf);
    gl.bufferSubData(gl.ARRAY_BUFFER, 0, state);
  }

  /** Show only posts created within [t0, t1] (unix seconds). */
  setTimeRange(t0, t1) {
    this.t0 = t0 - this.tsMin;
    this.t1 = t1 - this.tsMin;
  }

  resize(w, h) {
    const c = this.gl.canvas;
    if (c.width !== w || c.height !== h) {
      c.width = w;
      c.height = h;
    }
    this.gl.viewport(0, 0, w, h);
  }

  /** cam: { cx, cy, scale (device px per layout unit), w, h, zoom }. */
  draw(cam, bg = [0.043, 0.051, 0.071]) {
    const gl = this.gl;
    gl.clearColor(bg[0], bg[1], bg[2], 1);
    gl.clear(gl.COLOR_BUFFER_BIT);
    gl.useProgram(this.prog);
    gl.bindVertexArray(this.vao);
    gl.uniform2f(this.u.u_center, cam.cx, cam.cy);
    // Clip space already has y pointing up, the same as the layout and Camera.toScreen, so
    // neither axis is flipped here. (Flipping y put the dots at the mirror image of the labels.)
    gl.uniform2f(this.u.u_scale, (2 * cam.scale) / cam.w, (2 * cam.scale) / cam.h);
    gl.uniform1f(this.u.u_size, 3.2 * cam.dpr);
    gl.uniform1f(this.u.u_zoom, cam.zoomSize);
    gl.uniform1f(this.u.u_t0, this.t0 - 0.5);
    gl.uniform1f(this.u.u_t1, this.t1 + 0.5);
    gl.uniform1f(this.u.u_dimAlpha, this.dimAlpha);
    gl.uniform1i(this.u.u_pass, 0);
    gl.drawArrays(gl.POINTS, 0, this.N);
    gl.uniform1i(this.u.u_pass, 1);
    gl.drawArrays(gl.POINTS, 0, this.N);
  }
}
