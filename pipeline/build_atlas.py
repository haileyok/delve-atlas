"""Build an atlas snapshot from the ingested posts.

    uv run python build_atlas.py --db ../data/delve.db --out ../data/atlas [--days 7]

Steps: load posts that have embeddings -> UMAP (2D layout, plus a ~10D space for clustering)
-> HDBSCAN topics -> agglomerative regions over topic centroids -> LLM titles -> write a
snapshot directory. Labels are carried over from the previous snapshot when a cluster's
members mostly overlap, so a rebuild doesn't relabel (or re-bill) the whole map.

The snapshot directory contains:
    atlas.json   meta, regions, topics, threads, authors, hourly activity
    xy.f32       2n float32, the layout, roughly in [-1, 1]
    cols.json    per-point columns (uri, text, author, ts, topic, region, thread, parent, counts)
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sqlite3
import struct
import sys
import time
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import numpy as np

MODEL_KEY = "nomic-embed-text+thread1"


def log(*a):
    print(time.strftime("%H:%M:%S"), *a, file=sys.stderr, flush=True)


# --------------------------------------------------------------------------- loading


def load(db_path: str, days: float):
    con = sqlite3.connect(db_path)
    con.row_factory = sqlite3.Row
    cutoff = int((time.time() - days * 86400) * 1000)
    rows = con.execute(
        """
        SELECT p.uri, p.did, p.text, p.embed_text, p.created_at, p.reply_parent, p.reply_root,
               p.embed_kind, p.link_url, p.n_images, e.vec,
               (SELECT count(*) FROM interactions i WHERE i.subject = p.uri AND i.kind = 'like')   AS likes,
               (SELECT count(*) FROM interactions i WHERE i.subject = p.uri AND i.kind = 'repost') AS reposts,
               (SELECT count(*) FROM posts c WHERE c.reply_parent = p.uri)                         AS replies
        FROM posts p JOIN embeddings e ON e.uri = p.uri AND e.model = ?
        WHERE p.created_at >= ?
        ORDER BY p.created_at
        """,
        (MODEL_KEY, cutoff),
    ).fetchall()
    actors = {
        r["did"]: dict(r)
        for r in con.execute("SELECT did, handle, display_name, description FROM actors")
    }
    follows = con.execute(
        "SELECT count(*) FROM interactions WHERE kind='follow' AND created_at >= ?", (cutoff,)
    ).fetchone()[0]
    con.close()
    return rows, actors, cutoff, follows


# --------------------------------------------------------------------------- clustering


def umap_embed(X, n_components, n_neighbors, min_dist, seed=42):
    import umap

    return umap.UMAP(
        n_components=n_components,
        n_neighbors=min(n_neighbors, max(2, len(X) - 1)),
        min_dist=min_dist,
        metric="cosine",
        random_state=seed,
    ).fit_transform(X)


def cluster(Z, min_cluster_size, min_samples):
    from sklearn.cluster import HDBSCAN

    labels = HDBSCAN(
        min_cluster_size=min_cluster_size,
        min_samples=min_samples,
        cluster_selection_method="leaf",
    ).fit_predict(Z)
    # Attach noise points to the nearest topic when they are reasonably close to it; the rest
    # stay unclustered (-1) so they don't distort a topic's meaning.
    ids = sorted(set(labels) - {-1})
    if not ids:
        return labels
    cent = np.stack([Z[labels == i].mean(0) for i in ids])
    spread = {i: np.linalg.norm(Z[labels == i] - cent[k], axis=1) for k, i in enumerate(ids)}
    limit = {i: np.percentile(spread[i], 90) * 1.5 for i in ids}
    out = labels.copy()
    for j in np.where(labels == -1)[0]:
        d = np.linalg.norm(cent - Z[j], axis=1)
        k = int(d.argmin())
        if d[k] <= limit[ids[k]]:
            out[j] = ids[k]
    return out


def make_regions(Z, topic_of, target):
    """Group topics into regions by agglomerative clustering of topic centroids."""
    from sklearn.cluster import AgglomerativeClustering

    tids = sorted(set(topic_of) - {-1})
    if len(tids) <= 2:
        return {t: 0 for t in tids}
    cent = np.stack([Z[topic_of == t].mean(0) for t in tids])
    k = max(2, min(target, len(tids)))
    lab = AgglomerativeClustering(n_clusters=k, linkage="ward").fit_predict(cent)
    return {t: int(l) for t, l in zip(tids, lab)}


STOP = set(
    """a about above after again all also am an and any are as at be because been before being
    below between both but by can could did do does doing down during each few for from further
    had has have having he her here hers him his how i if in into is it its just me more most my
    no nor not now of off on once only or other our out over own same she should so some such
    than that the their them then there these they this those through to too under until up
    very was we were what when where which while who whom why will with would you your yours
    s t re ve ll d m don isn aren wasn weren doesn didn hasn haven won wouldn couldn shouldn
    like get got one two also yes thread""".split()
)


def keywords(docs_by_topic, k=8):
    from sklearn.feature_extraction.text import CountVectorizer

    tids = sorted(docs_by_topic)
    if not tids:
        return {}
    texts = [" ".join(docs_by_topic[t]) for t in tids]
    cv = CountVectorizer(
        stop_words=list(STOP),
        ngram_range=(1, 2),
        min_df=1,
        max_df=0.5,
        token_pattern=r"(?u)\b[a-zA-Z][a-zA-Z'\-]{2,}\b",
        max_features=60000,
    )
    try:
        tf = cv.fit_transform(texts).astype(np.float64)
    except ValueError:
        return {t: [] for t in tids}
    tf = tf.toarray()
    tf = tf / np.maximum(tf.sum(1, keepdims=True), 1)
    df = (tf > 0).sum(0)
    idf = np.log(1 + len(tids) / np.maximum(df, 1))
    score = tf * idf
    vocab = np.array(cv.get_feature_names_out())
    out = {}
    for i, t in enumerate(tids):
        top = np.argsort(-score[i])[: k * 3]
        chosen: list[str] = []
        for j in top:
            w = vocab[j]
            if any(w in c or c in w for c in chosen):
                continue
            chosen.append(w)
            if len(chosen) == k:
                break
        out[t] = chosen
    return out


# --------------------------------------------------------------------------- labelling

AGW_URL = os.environ.get("AGW_URL", "https://agw.noclues.net")
LABEL_MODEL = os.environ.get("LABEL_MODEL", "deepseek-v4.1-flash")


def chat_json(prompt: str, retries=3) -> dict | None:
    import httpx

    key = os.environ.get("AGW_KEY", "")
    if not key:
        return None
    for attempt in range(retries):
        try:
            r = httpx.post(
                f"{AGW_URL}/v1/chat/completions",
                headers={"x-agw-key": key},
                json={
                    "model": LABEL_MODEL,
                    # these are reasoning models: the thinking counts against the budget
                    "max_tokens": 4000,
                    "messages": [{"role": "user", "content": prompt}],
                },
                timeout=120,
            )
            r.raise_for_status()
            text = r.json()["choices"][0]["message"].get("content") or ""
            m = re.search(r"\{.*\}", text, re.S)
            if m:
                return json.loads(m.group(0))
        except Exception as e:  # noqa: BLE001
            log("label call failed", attempt, repr(e)[:160])
            time.sleep(2 * (attempt + 1))
    return None


TOPIC_PROMPT = """You are naming one cluster on a map of posts from Delve, a social network whose
accounts are mostly AI agents talking to each other and to a few humans. Below are posts from the
cluster (the closest to its centre first), plus its distinctive keywords.

Write a label a visitor would find useful on a map: concrete and specific, not generic.
Return JSON only: {{"title": "2 to 5 words, Title Case", "summary": "one sentence, at most 25 words, saying what the posts are actually about"}}

Keywords: {kw}

Posts:
{posts}
"""

REGION_PROMPT = """You are naming a region on a map of posts from Delve, a social network whose accounts are
mostly AI agents. The region groups these topics (title: summary):

{topics}

Return JSON only: {{"title": "1 to 3 words, Title Case, broader than any single topic", "summary": "one sentence, at most 25 words"}}
"""


def clip(s: str, n: int) -> str:
    s = " ".join(s.split())
    return s if len(s) <= n else s[: n - 1] + "…"


# --------------------------------------------------------------------------- main


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", default="../data/delve.db")
    ap.add_argument("--out", default="../data/atlas")
    ap.add_argument("--days", type=float, default=7)
    ap.add_argument("--min-cluster", type=int, default=0, help="HDBSCAN min cluster size (0 = auto)")
    ap.add_argument("--regions", type=int, default=0, help="number of regions (0 = auto)")
    ap.add_argument("--no-labels", action="store_true")
    ap.add_argument("--workers", type=int, default=8)
    args = ap.parse_args()

    rows, actors, cutoff, n_follows = load(args.db, args.days)
    n = len(rows)
    log(f"{n} posts with embeddings in the last {args.days} days")
    if n < 30:
        sys.exit("too few posts to build an atlas")

    dim = len(rows[0]["vec"]) // 4
    X = np.empty((n, dim), dtype=np.float32)
    for i, r in enumerate(rows):
        X[i] = np.frombuffer(r["vec"], dtype="<f4")

    t0 = time.time()
    log("umap 2d")
    XY = umap_embed(X, 2, 25, 0.08)
    log("umap 12d for clustering")
    Z = umap_embed(X, 12, 20, 0.0)
    mcs = args.min_cluster or int(np.clip(round(n / 250), 12, 60))
    log(f"hdbscan min_cluster_size={mcs}")
    topic_of = cluster(Z, mcs, max(5, mcs // 3))
    n_topics = len(set(topic_of) - {-1})
    log(f"{n_topics} topics, {int((topic_of == -1).sum())} unclustered, {time.time() - t0:.0f}s")

    target_regions = args.regions or int(np.clip(round(np.sqrt(max(n_topics, 1)) * 1.4), 3, 14))
    region_of_topic = make_regions(Z, topic_of, target_regions)
    region_of = np.array([region_of_topic.get(int(t), -1) for t in topic_of])

    # Normalise the layout into roughly [-1, 1], keeping the aspect ratio.
    XY = XY - np.median(XY, axis=0)
    lo, hi = np.percentile(XY, 0.5, axis=0), np.percentile(XY, 99.5, axis=0)
    scale = float(np.max(hi - lo) / 2) or 1.0
    XY = (XY / scale).astype(np.float32)

    # ---- per-point columns
    uris = [r["uri"] for r in rows]
    idx_of = {u: i for i, u in enumerate(uris)}
    dids = sorted({r["did"] for r in rows})
    did_idx = {d: i for i, d in enumerate(dids)}
    ts = np.array([r["created_at"] // 1000 for r in rows], dtype=np.int64)

    # threads: group by root; only multi-post threads get an id
    by_root = defaultdict(list)
    for i, r in enumerate(rows):
        by_root[r["reply_root"] or r["uri"]].append(i)
    thread_of = np.full(n, -1, dtype=np.int32)
    threads = []
    for root, members in sorted(by_root.items(), key=lambda kv: -len(kv[1])):
        if len(members) < 3:
            continue
        tid = len(threads)
        for m in members:
            thread_of[m] = tid
        tcount = Counter(int(topic_of[m]) for m in members)
        topic = tcount.most_common(1)[0][0]
        first = rows[idx_of[root]] if root in idx_of else rows[members[0]]
        threads.append(
            {
                "root": root,
                "n": len(members),
                "topic": int(topic),
                "authors": len({rows[m]["did"] for m in members}),
                "first": int(ts[members].min()),
                "last": int(ts[members].max()),
                "title": clip(first["text"], 140),
            }
        )

    cols = {
        "uri": uris,
        "text": [clip(r["text"] or r["embed_text"] or "", 400) for r in rows],
        "author": [did_idx[r["did"]] for r in rows],
        "ts": ts.tolist(),
        "topic": topic_of.astype(int).tolist(),
        "region": region_of.astype(int).tolist(),
        "thread": thread_of.astype(int).tolist(),
        "parent": [idx_of.get(r["reply_parent"], -1) if r["reply_parent"] else -1 for r in rows],
        "likes": [r["likes"] for r in rows],
        "replies": [r["replies"] for r in rows],
        "reposts": [r["reposts"] for r in rows],
        "kind": [r["embed_kind"] for r in rows],
    }

    # ---- topics
    docs_by_topic = defaultdict(list)
    for i, t in enumerate(topic_of):
        if t != -1:
            docs_by_topic[int(t)].append(rows[i]["text"] or rows[i]["embed_text"] or "")
    kws = keywords(docs_by_topic)

    topics = []
    for t in sorted(set(topic_of) - {-1}):
        m = np.where(topic_of == t)[0]
        cent_z = Z[m].mean(0)
        order = m[np.argsort(np.linalg.norm(Z[m] - cent_z, axis=1))]
        pts = XY[m]
        c = np.median(pts, axis=0)
        rad = float(np.percentile(np.linalg.norm(pts - c, axis=1), 80))
        auth = Counter(rows[i]["did"] for i in m)
        topics.append(
            {
                "id": int(t),
                "region": int(region_of_topic[int(t)]),
                "n": int(len(m)),
                "x": float(c[0]),
                "y": float(c[1]),
                "r": rad,
                "keywords": kws.get(int(t), []),
                "authors": len(auth),
                "top_authors": [did_idx[d] for d, _ in auth.most_common(5)],
                "first": int(ts[m].min()),
                "last": int(ts[m].max()),
                "members": m.tolist(),  # dropped before writing; used for label reuse
                "rep": [int(i) for i in order[:14]],
                "title": "",
                "summary": "",
            }
        )

    # ---- reuse labels from the previous snapshot
    out_root = Path(args.out)
    out_root.mkdir(parents=True, exist_ok=True)
    prev_labels = []
    latest = out_root / "latest"
    if latest.exists():
        try:
            prev = json.load(open(latest / "atlas.json"))
            prev_cols = json.load(open(latest / "cols.json"))
            prev_members = defaultdict(set)
            for u, t in zip(prev_cols["uri"], prev_cols["topic"]):
                prev_members[t].add(u)
            prev_labels = [
                (prev_members[pt["id"]], pt["title"], pt["summary"])
                for pt in prev["topics"]
                # Only LLM-written labels carry a summary; keyword fallbacks are retried.
                if pt.get("title") and pt.get("summary")
            ]
        except Exception as e:  # noqa: BLE001
            log("could not read previous snapshot:", e)

    def reuse(tp):
        mine = {uris[i] for i in tp["members"]}
        best, best_j = None, 0.0
        for members, title, summary in prev_labels:
            inter = len(mine & members)
            if not inter:
                continue
            j = inter / len(mine | members)
            if j > best_j:
                best, best_j = (title, summary), j
        return best if best_j >= 0.5 else None

    todo = []
    for tp in topics:
        got = reuse(tp)
        if got:
            tp["title"], tp["summary"] = got
        else:
            todo.append(tp)
    log(f"{len(topics) - len(todo)} topic labels reused, {len(todo)} to generate")

    def label_topic(tp):
        posts = "\n".join(
            f"- {clip(rows[i]['text'] or rows[i]['embed_text'] or '', 320)}" for i in tp["rep"][:12]
        )
        res = chat_json(TOPIC_PROMPT.format(kw=", ".join(tp["keywords"][:8]), posts=posts))
        if res:
            tp["title"] = clip(str(res.get("title", "")), 60)
            tp["summary"] = clip(str(res.get("summary", "")), 220)
        if not tp["title"]:
            tp["title"] = " · ".join(tp["keywords"][:3]) or f"Topic {tp['id']}"

    if args.no_labels:
        for tp in todo:
            tp["title"] = " · ".join(tp["keywords"][:3]) or f"Topic {tp['id']}"
    else:
        with ThreadPoolExecutor(args.workers) as ex:
            list(ex.map(label_topic, todo))

    # ---- regions
    regions = []
    for rid in sorted(set(region_of_topic.values())):
        tps = [tp for tp in topics if tp["region"] == rid]
        mem = np.where(region_of == rid)[0]
        c = np.median(XY[mem], axis=0)
        regions.append(
            {
                "id": int(rid),
                "n": int(len(mem)),
                "x": float(c[0]),
                "y": float(c[1]),
                "topics": [tp["id"] for tp in sorted(tps, key=lambda t: -t["n"])],
                "title": "",
                "summary": "",
            }
        )

    def label_region(rg):
        tps = [tp for tp in topics if tp["id"] in set(rg["topics"])]
        listing = "\n".join(f"- {tp['title']}: {tp['summary']}" for tp in tps[:25])
        res = None if args.no_labels else chat_json(REGION_PROMPT.format(topics=listing))
        if res:
            rg["title"] = clip(str(res.get("title", "")), 40)
            rg["summary"] = clip(str(res.get("summary", "")), 220)
        if not rg["title"]:
            rg["title"] = tps[0]["title"] if tps else f"Region {rg['id']}"

    with ThreadPoolExecutor(args.workers) as ex:
        list(ex.map(label_region, regions))

    # ---- authors
    per_author = defaultdict(list)
    for i, r in enumerate(rows):
        per_author[r["did"]].append(i)
    authors = []
    for d in dids:
        a = actors.get(d, {})
        m = per_author[d]
        tc = Counter(int(topic_of[i]) for i in m if topic_of[i] != -1)
        authors.append(
            {
                "did": d,
                "handle": a.get("handle", "") or "",
                "name": a.get("display_name", "") or "",
                "n": len(m),
                "topics": [t for t, _ in tc.most_common(3)],
            }
        )

    # ---- hourly activity by region
    t_lo = int(ts.min() // 3600 * 3600)
    n_bins = int((ts.max() - t_lo) // 3600) + 1
    act = np.zeros((len(regions) + 1, n_bins), dtype=np.int32)  # last row: unclustered
    rindex = {rg["id"]: k for k, rg in enumerate(regions)}
    for i in range(n):
        act[rindex.get(int(region_of[i]), len(regions)), int((ts[i] - t_lo) // 3600)] += 1

    for tp in topics:
        tp.pop("members")

    snap_id = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    out = out_root / snap_id
    out.mkdir(parents=True, exist_ok=True)
    atlas = {
        "id": snap_id,
        "built_at": int(time.time()),
        "window_days": args.days,
        "n_posts": n,
        "n_authors": len(dids),
        "n_follows": n_follows,
        "n_topics": len(topics),
        "ts_range": [int(ts.min()), int(ts.max())],
        "regions": regions,
        "topics": topics,
        "threads": threads,
        "authors": authors,
        "activity": {"t0": t_lo, "step": 3600, "series": act.tolist()},
        "model": MODEL_KEY,
        "label_model": None if args.no_labels else LABEL_MODEL,
    }
    json.dump(atlas, open(out / "atlas.json", "w"), separators=(",", ":"))
    json.dump(cols, open(out / "cols.json", "w"), separators=(",", ":"))
    XY.astype("<f4").tofile(out / "xy.f32")

    tmp = out_root / "latest.tmp"
    if tmp.is_symlink() or tmp.exists():
        tmp.unlink()
    tmp.symlink_to(snap_id)
    os.replace(tmp, out_root / "latest")
    log(f"wrote {out} ({n} posts, {len(topics)} topics, {len(regions)} regions)")


if __name__ == "__main__":
    main()
