// Opens every kind of detail view and the activity tab; reports what each rendered.
export default async (page) => {
  await page.goto((process.env.BASE ?? 'http://127.0.0.1:8088') + '/');
  await page.waitFor('window.__atlas && window.__atlas.D');
  await page.wait(800);
  const rows = [];
  const probe = async (name, sel) => {
    await page.eval(`window.__atlas.select(${JSON.stringify(sel)}, { fly: false })`);
    await page.wait(700);
    rows.push({
      name,
      title: await page.eval(`document.querySelector('#drawer h2')?.textContent ?? null`),
      sections: await page.eval(`[...document.querySelectorAll('#drawer h3')].map((h) => h.textContent)`),
      posts: await page.eval(`document.querySelectorAll('#drawer .post').length`),
      stats: await page.eval(`[...document.querySelectorAll('#drawer .stat')].map((s) => s.textContent)`),
    });
  };
  const ids = await page.eval(`(() => {
    const { D } = window.__atlas;
    const k = D.atlas.threads.findIndex((t) => t.n >= 5);
    const i = D.threadMembers[k]?.[0] ?? 0;
    return { region: D.atlas.regions[0].id, topic: D.atlas.topics[0].id, thread: k, post: i, author: D.author[i] };
  })()`);
  await page.eval(`window.__atlas.S.matches = (() => { const { D } = window.__atlas; const o = []; for (let i = 0; i < D.N; i++) if (D.textLower[i].includes('random')) o.push(i); return o; })()`);
  await probe('region', { type: 'region', id: ids.region });
  await probe('topic', { type: 'topic', id: ids.topic });
  await probe('thread', { type: 'thread', id: ids.thread });
  await probe('post', { type: 'post', id: ids.post });
  await probe('author', { type: 'author', id: ids.author });
  await probe('search', { type: 'search', id: 'random' });

  // activity tab
  await page.eval(`document.querySelector('[data-pane=activity]').click()`);
  await page.waitFor(`document.querySelector('#activity .stat')`, 8000);
  rows.push({
    name: 'activity',
    stats: await page.eval(`[...document.querySelectorAll('#activity .stats .stat')].map((s) => s.textContent)`),
    sections: await page.eval(`[...document.querySelectorAll('#activity h3')].map((h) => h.textContent)`),
    sparks: await page.eval(`document.querySelectorAll('#activity canvas.spark').length`),
    posters: await page.eval(`document.querySelectorAll('#activity .poster').length`),
  });
  await page.screenshot('/tmp/drawers.png');
  return { rows, logs: page.logs };
};
