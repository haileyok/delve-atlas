// Sidebar outline, detail drawer and the live activity tab.

import { el, fmt, compact, ago, fmtDateTime, fmtDay, rgbCss, authorName, authorHandle, postUrl, clamp } from './util.js';

const score = (D, i) => D.likes[i] + 1.5 * D.replies[i] + 2 * D.reposts[i];
const dot = (color) => el('span', { class: 'dot', style: { background: rgbCss(color) } });

// ------------------------------------------------------------------ sidebar outline

export class Outline {
  constructor(host, D, actions) {
    this.D = D;
    this.actions = actions;
    this.host = host;
    this.open = new Set();
    this.sel = null;
    this.render();
  }

  setSelection(sel) {
    this.sel = sel;
    const D = this.D;
    if (sel?.type === 'topic') this.open.add(D.topicById.get(sel.id)?.region);
    if (sel?.type === 'region') this.open.add(sel.id);
    this.render();
  }

  render() {
    const D = this.D, { host } = this;
    const maxTopic = Math.max(...D.atlas.topics.map((t) => t.n), 1);
    const regs = [...D.atlas.regions].sort((a, b) => b.n - a.n);
    const list = el('div', { class: 'outline' });
    for (const r of regs) {
      const isOpen = this.open.has(r.id);
      const selR = this.sel?.type === 'region' && this.sel.id === r.id;
      const head = el(
        'div',
        { class: 'o-region' + (selR ? ' selected' : '') },
        el('button', {
          class: 'chev' + (isOpen ? ' open' : ''),
          title: isOpen ? 'Collapse' : 'Show topics',
          onclick: (e) => { e.stopPropagation(); isOpen ? this.open.delete(r.id) : this.open.add(r.id); this.render(); },
        }, '›'),
        dot(r.color),
        el('span', { class: 'o-title', onclick: () => this.actions.select({ type: 'region', id: r.id }) }, r.title),
        el('span', { class: 'o-n' }, fmt.format(r.n)),
      );
      list.append(head);
      if (isOpen) {
        for (const tid of r.topics) {
          const t = D.topicById.get(tid);
          const selT = this.sel?.type === 'topic' && this.sel.id === tid;
          list.append(
            el('div', {
              class: 'o-topic' + (selT ? ' selected' : ''),
              onclick: () => this.actions.select({ type: 'topic', id: tid }),
              onmouseenter: () => this.actions.hoverTopic(tid),
              onmouseleave: () => this.actions.hoverTopic(null),
              title: t.summary,
            },
              el('span', { class: 'bar', style: { width: `${(t.n / maxTopic) * 100}%`, background: rgbCss(t.color, 0.22) } }),
              dot(t.color),
              el('span', { class: 'o-title' }, t.title),
              el('span', { class: 'o-n' }, fmt.format(t.n)),
            ),
          );
        }
      }
    }
    host.replaceChildren(list);
  }
}

// ------------------------------------------------------------------ detail drawer

export class Drawer {
  constructor(host, D, actions) {
    this.D = D;
    this.actions = actions;
    this.host = host;
    this.token = 0;
  }

  hide() {
    this.host.classList.remove('open');
  }

  /** ctx: { range: [t0,t1], matches?: number[] (search results) } */
  show(sel, ctx) {
    const D = this.D;
    this.host.classList.add('open');
    const body = el('div', { class: 'drawer-body' });
    const close = el('button', { class: 'close', title: 'Close (Esc)', onclick: () => this.actions.select(null) }, '×');
    const [r0, r1] = ctx.range;
    const inR = (i) => D.ts[i] >= r0 && D.ts[i] <= r1;
    const my = ++this.token;

    switch (sel.type) {
      case 'region': this.region(body, sel.id, inR); break;
      case 'topic': this.topic(body, sel.id, inR); break;
      case 'thread': this.thread(body, sel.id, my); break;
      case 'post': this.post(body, sel.id, my, inR); break;
      case 'author': this.author(body, sel.id, inR); break;
      case 'search': this.search(body, sel.id, ctx.matches ?? [], inR); break;
    }
    this.host.replaceChildren(close, body);
    this.host.scrollTop = 0;
  }

  // ---- building blocks

  crumbs(...items) {
    return el('div', { class: 'crumbs' }, items.filter(Boolean).flatMap((c, i) => (i ? [el('span', { class: 'sep' }, '›'), c] : [c])));
  }
  regionCrumb(rid) {
    const r = this.D.regionById.get(rid);
    return r ? el('a', { onclick: () => this.actions.select({ type: 'region', id: rid }) }, r.title) : null;
  }
  topicCrumb(tid) {
    const t = this.D.topicById.get(tid);
    return t ? el('a', { onclick: () => this.actions.select({ type: 'topic', id: tid }) }, t.title) : null;
  }

  stats(pairs) {
    return el('div', { class: 'stats' }, pairs.map(([k, v]) => el('div', { class: 'stat' }, el('b', {}, v), el('span', {}, k))));
  }

  authorChip(a) {
    const D = this.D;
    const au = D.authors[a];
    return el('a', { class: 'chip', onclick: () => this.actions.select({ type: 'author', id: a }), title: authorHandle(au) }, authorName(au));
  }

  postItem(i, { showAuthor = true, full = false } = {}) {
    const D = this.D;
    const t = D.topicById.get(D.topic[i]);
    const au = D.authors[D.author[i]];
    const eng = [];
    if (D.likes[i]) eng.push(`♥ ${D.likes[i]}`);
    if (D.replies[i]) eng.push(`↩ ${D.replies[i]}`);
    if (D.reposts[i]) eng.push(`⟳ ${D.reposts[i]}`);
    return el('div', { class: 'post', onclick: () => this.actions.select({ type: 'post', id: i }), onmouseenter: () => this.actions.hoverPoint(i), onmouseleave: () => this.actions.hoverPoint(null) },
      el('div', { class: 'post-head' },
        dot(t ? t.color : D.noise.color),
        showAuthor && el('b', {}, authorName(au)),
        el('span', { class: 'muted' }, fmtDateTime(D.ts[i])),
        eng.length > 0 && el('span', { class: 'eng' }, eng.join('  ')),
      ),
      el('div', { class: 'post-text' + (full ? ' full' : '') }, D.text[i] || el('i', { class: 'muted' }, '(no text)')),
    );
  }

  postList(title, indices, opts) {
    if (!indices.length) return null;
    return el('section', {}, el('h3', {}, title), el('div', { class: 'posts' }, indices.map((i) => this.postItem(i, opts))));
  }

  topPosts(idx, n = 6) {
    return [...idx].sort((a, b) => score(this.D, b) - score(this.D, a) || this.D.ts[b] - this.D.ts[a]).filter((i) => score(this.D, i) > 0).slice(0, n);
  }
  recentPosts(idx, n = 6) {
    return [...idx].sort((a, b) => this.D.ts[b] - this.D.ts[a]).slice(0, n);
  }

  topAuthors(idx, n = 8) {
    const c = new Map();
    for (const i of idx) c.set(this.D.author[i], (c.get(this.D.author[i]) ?? 0) + 1);
    return [...c.entries()].sort((a, b) => b[1] - a[1]).slice(0, n);
  }

  authorChips(idx) {
    const top = this.topAuthors(idx);
    if (!top.length) return null;
    return el('section', {}, el('h3', {}, 'Who is talking'),
      el('div', { class: 'chips' }, top.map(([a, n]) => el('span', { class: 'chip-wrap' }, this.authorChip(a), el('small', {}, n)))));
  }

  threadList(threads, n = 8) {
    const D = this.D;
    if (!threads.length) return null;
    return el('section', {}, el('h3', {}, 'Conversations'),
      el('div', { class: 'threads' }, threads.slice(0, n).map(({ t, k }) =>
        el('div', { class: 'thread', onclick: () => this.actions.select({ type: 'thread', id: k }) },
          el('div', { class: 'thread-title' }, t.title || '(untitled)'),
          el('div', { class: 'muted small' }, `${t.n} posts · ${t.authors} ${t.authors === 1 ? 'account' : 'accounts'} · ${fmtDay(t.first)}${fmtDay(t.last) !== fmtDay(t.first) ? '–' + fmtDay(t.last) : ''}`),
        ))));
  }

  // ---- regions

  region(body, id, inR) {
    const D = this.D;
    const r = D.regionById.get(id);
    const idx = (D.byRegion.get(id) ?? []).filter(inR);
    body.append(
      this.crumbs(el('span', {}, 'Region')),
      el('h2', {}, dot(r.color), r.title),
      el('p', { class: 'summary' }, r.summary),
      this.stats([['posts', fmt.format(idx.length)], ['accounts', new Set(idx.map((i) => D.author[i])).size], ['topics', r.topics.length]]),
      el('section', {}, el('h3', {}, 'Topics'),
        el('div', { class: 'topics' }, r.topics.map((tid) => {
          const t = D.topicById.get(tid);
          const n = (D.byTopic.get(tid) ?? []).filter(inR).length;
          return el('div', { class: 'topic-row', onclick: () => this.actions.select({ type: 'topic', id: tid }), onmouseenter: () => this.actions.hoverTopic(tid), onmouseleave: () => this.actions.hoverTopic(null) },
            dot(t.color), el('div', {}, el('b', {}, t.title), el('div', { class: 'muted small' }, t.summary)), el('span', { class: 'o-n' }, fmt.format(n)));
        }))),
      this.authorChips(idx),
      this.postList('Most engaged', this.topPosts(idx)),
      this.postList('Latest', this.recentPosts(idx)),
    );
  }

  // ---- topics

  topic(body, id, inR) {
    const D = this.D;
    const t = D.topicById.get(id);
    const idx = (D.byTopic.get(id) ?? []).filter(inR);
    const threads = D.atlas.threads.map((th, k) => ({ t: th, k })).filter(({ t: th }) => th.topic === id).sort((a, b) => b.t.n - a.t.n);
    body.append(
      this.crumbs(this.regionCrumb(t.region), el('span', {}, 'Topic')),
      el('h2', {}, dot(t.color), t.title),
      el('p', { class: 'summary' }, t.summary),
      t.keywords.length > 0 && el('div', { class: 'kw' }, t.keywords.slice(0, 8).map((k) => el('span', { class: 'kw-chip' }, k))),
      this.stats([['posts', fmt.format(idx.length)], ['accounts', new Set(idx.map((i) => D.author[i])).size], [threads.length === 1 ? 'conversation' : 'conversations', threads.length]]),
      this.authorChips(idx),
      this.threadList(threads),
      this.postList('Most engaged', this.topPosts(idx)),
      this.postList('Latest', this.recentPosts(idx, 8)),
    );
  }

  // ---- threads

  async thread(body, k, token) {
    const D = this.D;
    const th = D.atlas.threads[k];
    const members = D.threadMembers[k].slice().sort((a, b) => D.ts[a] - D.ts[b]);
    // Big conversations are shown in pages so the panel stays quick.
    const PAGE = 120;
    let shown = 0;
    const list = el('div', { class: 'posts' });
    const more = el('button', { class: 'btn' });
    let byUri = new Map();
    const addPosts = () => {
      const next = members.slice(shown, shown + PAGE);
      for (const i of next) {
        const item = this.postItem(i, { full: true });
        const full = byUri.get(D.cols.uri[i]);
        if (full?.text) item.querySelector('.post-text').textContent = full.text;
        list.append(item);
      }
      shown += next.length;
      more.textContent = `Show ${Math.min(PAGE, members.length - shown)} more (${members.length - shown} left)`;
      more.style.display = shown < members.length ? '' : 'none';
    };
    more.onclick = addPosts;
    body.append(
      this.crumbs(this.regionCrumb(D.topicById.get(th.topic)?.region), this.topicCrumb(th.topic), el('span', {}, 'Conversation')),
      el('h2', { class: 'thread-h' }, th.title),
      this.stats([['posts', th.n], ['accounts', th.authors], ['started', fmtDay(th.first)]]),
      el('section', {}, el('h3', {}, 'In order'), list, more),
    );
    addPosts();
    // Swap in full text for posts when the server has it.
    try {
      const rows = await fetch('/api/thread?root=' + encodeURIComponent(th.root)).then((r) => r.json());
      if (token !== this.token) return;
      byUri = new Map(rows.map((p) => [p.uri, p]));
      list.querySelectorAll('.post').forEach((n, j) => {
        const p = byUri.get(D.cols.uri[members[j]]);
        if (p?.text) n.querySelector('.post-text').textContent = p.text;
      });
    } catch { /* the 400-character text stays */ }
  }

  // ---- single post

  async post(body, i, token, inR) {
    const D = this.D;
    const au = D.authors[D.author[i]];
    const t = D.topicById.get(D.topic[i]);
    const textEl = el('div', { class: 'big-text' }, D.text[i] || '(no text)');
    const k = D.thread[i];
    const parent = D.parent[i] >= 0 ? D.parent[i] : null;
    body.append(
      this.crumbs(this.regionCrumb(t?.region), this.topicCrumb(D.topic[i]), el('span', {}, 'Post')),
      el('div', { class: 'who' }, el('b', {}, authorName(au)), ' ', el('a', { class: 'muted', onclick: () => this.actions.select({ type: 'author', id: D.author[i] }) }, authorHandle(au))),
      textEl,
      el('div', { class: 'muted small' }, fmtDateTime(D.ts[i]), ' · ', el('a', { href: postUrl(D.cols.uri[i]), target: '_blank', rel: 'noopener' }, 'open on delve.town ↗')),
      this.stats([['likes', D.likes[i]], ['replies', D.replies[i]], ['reposts', D.reposts[i]]]),
      parent != null && el('section', {}, el('h3', {}, 'Replying to'), el('div', { class: 'posts' }, this.postItem(parent))),
      k >= 0 && el('button', { class: 'btn', onclick: () => this.actions.select({ type: 'thread', id: k }) }, `View the conversation (${D.atlas.threads[k].n} posts)`),
    );
    try {
      const p = await fetch('/api/post?uri=' + encodeURIComponent(D.cols.uri[i])).then((r) => (r.ok ? r.json() : null));
      if (token !== this.token || !p) return;
      textEl.textContent = p.text || '(no text)';
      if (p.embed_text) body.insertBefore(el('div', { class: 'embed' }, el('div', { class: 'muted small' }, p.embed_kind || 'embed'), p.link_url && el('a', { href: p.link_url, target: '_blank', rel: 'noopener' }, p.link_url), el('div', {}, p.embed_text)), textEl.nextSibling);
    } catch { /* keep the preview */ }
  }

  // ---- accounts

  author(body, a, inR) {
    const D = this.D;
    const au = D.authors[a];
    const idx = (D.byAuthor.get(a) ?? []).filter(inR);
    const byTopic = new Map();
    for (const i of idx) byTopic.set(D.topic[i], (byTopic.get(D.topic[i]) ?? 0) + 1);
    const tops = [...byTopic.entries()].sort((x, y) => y[1] - x[1]).slice(0, 8);
    body.append(
      this.crumbs(el('span', {}, 'Account')),
      el('h2', {}, authorName(au)),
      el('div', { class: 'muted' }, authorHandle(au)),
      this.stats([['posts', fmt.format(idx.length)], ['replies', idx.filter((i) => D.cols.reply?.[i]).length], ['likes received', idx.reduce((s, i) => s + D.likes[i], 0)]]),
      tops.length > 0 && el('section', {}, el('h3', {}, 'Talks about'),
        el('div', { class: 'topics' }, tops.map(([tid, n]) => {
          const t = D.topicById.get(tid) ?? D.noise;
          return el('div', { class: 'topic-row', onclick: () => tid >= 0 && this.actions.select({ type: 'topic', id: tid }) }, dot(t.color), el('b', {}, t.title), el('span', { class: 'o-n' }, n));
        }))),
      this.postList('Most engaged', this.topPosts(idx), { showAuthor: false }),
      this.postList('Latest', this.recentPosts(idx, 8), { showAuthor: false }),
    );
  }

  // ---- search

  search(body, q, matches, inR) {
    const D = this.D;
    const idx = matches.filter(inR);
    const byTopic = new Map();
    for (const i of idx) byTopic.set(D.topic[i], (byTopic.get(D.topic[i]) ?? 0) + 1);
    const tops = [...byTopic.entries()].sort((x, y) => y[1] - x[1]).slice(0, 6);
    body.append(
      this.crumbs(el('span', {}, 'Search')),
      el('h2', {}, `“${q}”`),
      this.stats([['matching posts', fmt.format(idx.length)], ['accounts', new Set(idx.map((i) => D.author[i])).size]]),
      tops.length > 0 && el('section', {}, el('h3', {}, 'Where it shows up'),
        el('div', { class: 'topics' }, tops.map(([tid, n]) => {
          const t = D.topicById.get(tid) ?? D.noise;
          return el('div', { class: 'topic-row', onclick: () => tid >= 0 && this.actions.select({ type: 'topic', id: tid }) }, dot(t.color), el('b', {}, t.title), el('span', { class: 'o-n' }, n));
        }))),
      this.postList('Matches', this.topPosts(idx, 4).concat(this.recentPosts(idx, 16)).filter((v, k, a) => a.indexOf(v) === k)),
    );
  }
}

// ------------------------------------------------------------------ activity tab

function spark(values, color, w = 280, h = 38) {
  const c = el('canvas', { class: 'spark' });
  const dpr = window.devicePixelRatio || 1;
  c.width = w * dpr; c.height = h * dpr;
  c.style.width = w + 'px'; c.style.height = h + 'px';
  const ctx = c.getContext('2d');
  ctx.scale(dpr, dpr);
  const max = Math.max(...values, 1);
  const bw = w / values.length;
  ctx.fillStyle = color;
  values.forEach((v, i) => {
    const bh = (v / max) * (h - 2);
    ctx.fillRect(i * bw, h - bh, Math.max(bw - 0.5, 0.5), bh);
  });
  return c;
}

export class ActivityPanel {
  constructor(host, D, actions) {
    this.host = host;
    this.D = D;
    this.actions = actions;
    this.loaded = false;
  }

  async load(force = false) {
    if (this.loaded && !force) return;
    this.host.replaceChildren(el('p', { class: 'muted pad' }, 'Loading live activity…'));
    try {
      const a = await fetch('/api/activity').then((r) => r.json());
      this.render(a);
      this.loaded = true;
    } catch (e) {
      this.host.replaceChildren(el('p', { class: 'muted pad' }, 'Could not load activity: ' + e.message));
    }
  }

  render(a) {
    const D = this.D;
    const tot = a.totals;
    const series = [
      ['Posts', a.hours.posts, '#7aa2ff', tot.posts - tot.replies],
      ['Replies', a.hours.replies, '#5ad1b3', tot.replies],
      ['Likes', a.hours.likes, '#ff8fb1', tot.likes],
      ['Follows', a.hours.follows, '#ffc66d', tot.follows],
      ['New accounts', a.hours.new_users, '#c3a6ff', a.hours.new_users.reduce((s, v) => s + v, 0)],
    ];
    const root = el('div', { class: 'activity' },
      el('div', { class: 'live' }, el('span', { class: 'pulse' }), `Live from Jetstream · last ${Math.round((a.now - a.since) / 86400)} days · updated ${ago(a.now)}`),
      el('div', { class: 'stats big' },
        [['posts', tot.posts], ['likes', tot.likes], ['follows', tot.follows], ['reposts', tot.reposts], ['active accounts', tot.accounts], ['posters', tot.posters]]
          .map(([k, v]) => el('div', { class: 'stat' }, el('b', {}, compact.format(v)), el('span', {}, k)))),
      el('section', {}, el('h3', {}, 'Per hour'),
        series.map(([name, vals, color, total]) => el('div', { class: 'series' },
          el('div', { class: 'series-head' }, el('span', {}, name), el('span', { class: 'muted' }, fmt.format(total))),
          spark(vals, color)))),
      el('section', {}, el('h3', {}, 'Most active accounts'),
        el('div', { class: 'posters' }, a.top_posters.map((p) => {
          const ai = D.authors.findIndex((x) => x.did === p.did);
          return el('div', { class: 'poster', onclick: () => ai >= 0 && this.actions.select({ type: 'author', id: ai }) },
            el('div', {}, el('b', {}, p.name || p.handle || p.did.slice(-8)), el('div', { class: 'muted small' }, p.handle ? '@' + p.handle : '')),
            el('div', { class: 'muted small right' }, `${fmt.format(p.posts)} posts`, el('br'), `${fmt.format(p.likes_received)} ♥`));
        }))),
      el('section', {}, el('h3', {}, 'Most liked posts'),
        el('div', { class: 'posts' }, a.top_posts.map((p) => {
          const i = D.uriIndex.get(p.uri);
          return el('div', { class: 'post', onclick: () => i != null && this.actions.select({ type: 'post', id: i }) },
            el('div', { class: 'post-head' }, el('b', {}, p.name || p.handle || p.did.slice(-8)), el('span', { class: 'muted' }, ago(p.created_at / 1000)), el('span', { class: 'eng' }, `♥ ${p.likes}  ↩ ${p.replies}`)),
            el('div', { class: 'post-text' }, p.text));
        }))),
      el('section', {}, el('h3', {}, 'What exists on the network'),
        el('table', { class: 'coll' }, a.collections.map((c) => el('tr', {}, el('td', {}, c.collection.replace('town.delve.', '')), el('td', { class: 'right' }, fmt.format(c.creates)), el('td', { class: 'right muted' }, c.deletes ? `−${fmt.format(c.deletes)}` : ''))))),
    );
    this.host.replaceChildren(root);
  }
}
