"""Conversations as the unit the atlas clusters.

A reply says little on its own ("o7", "yes, and..."), and one long thread can be one conversation
or a chain that wanders through many subjects. So posts are grouped into conversation *units*:

  * a unit is a connected piece of a thread: a post joins its parent's unit until that unit holds
    `max_posts`, then starts a new one. Short threads are one unit; a 1,400-post chain becomes
    a series of consecutive stretches, each of which stays on one subject;
  * each unit gets one vector, either from a transcript of its posts embedded as a whole, or by
    averaging its posts' own vectors;
  * topics are found among units (so a topic needs several separate conversations, not one huge
    thread), and each post inherits its unit's topic;
  * on the map each unit is a small spiral of its posts in time order, so a conversation is a
    visible cluster.
"""

from __future__ import annotations

import hashlib
import math
import os
import sqlite3
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed

import numpy as np


def log(*a):
    print(time.strftime("%H:%M:%S"), *a, file=sys.stderr, flush=True)


# --------------------------------------------------------------------------- units


def segment(rows, max_posts: int = 20):
    """Split posts (oldest first) into conversation units.

    Returns (unit_of, members): unit_of[i] is post i's unit, members[u] its posts oldest first.
    """
    idx = {r["uri"]: i for i, r in enumerate(rows)}
    unit_of = np.full(len(rows), -1, dtype=np.int32)
    members: list[list[int]] = []
    for i, r in enumerate(rows):
        p = idx.get(r["reply_parent"]) if r["reply_parent"] else None
        if p is not None and p < i and len(members[unit_of[p]]) < max_posts:
            u = int(unit_of[p])
        else:
            u = len(members)
            members.append([])
        unit_of[i] = u
        members[u].append(i)
    return unit_of, members


def _clip(s: str, n: int) -> str:
    s = " ".join((s or "").split())
    return s if len(s) <= n else s[: n - 1] + "…"


def post_text(r) -> str:
    text = (r["text"] or "").strip()
    extra = (r["embed_text"] or "").strip()
    if text and extra:
        return text + " " + _clip(extra, 160)
    return text or extra or "(no text)"


def transcripts(rows, members, post_chars: int = 280, max_chars: int = 4000, root_chars: int = 200):
    """One document per unit: a header naming the thread (when the unit does not start it), then
    the posts in order. Account names are left out on purpose: they pull a topic toward one voice."""
    docs = []
    for m in members:
        head = rows[m[0]]
        lines = []
        if head["reply_root"] and head["reply_root"] != head["uri"] and (head["root_text"] or "").strip():
            lines.append(f"[thread: {_clip(head['root_text'], root_chars)}]")
        budget = max_chars - sum(len(x) for x in lines)
        for i in m:
            line = "- " + _clip(post_text(rows[i]), post_chars)
            if budget - len(line) < 0 and len(lines) > 1:
                break
            lines.append(line)
            budget -= len(line)
        docs.append("\n".join(lines))
    return docs


def pooled_vectors(X, members, weight: str = "mean"):
    """Unit vector = normalised average of its posts' vectors (optionally weighted by length)."""
    out = np.zeros((len(members), X.shape[1]), dtype=np.float32)
    for u, m in enumerate(members):
        out[u] = X[m].mean(0)
    out /= np.maximum(np.linalg.norm(out, axis=1, keepdims=True), 1e-9)
    return out


# --------------------------------------------------------------------------- embedding units

UNIT_TABLE = """
CREATE TABLE IF NOT EXISTS unit_embeddings (
  key   TEXT NOT NULL,   -- sha1 of model, prefix and document text
  model TEXT NOT NULL,
  dim   INTEGER NOT NULL,
  vec   BLOB NOT NULL,
  PRIMARY KEY (key, model)
) WITHOUT ROWID"""


def default_prefix(model: str) -> str:
    m = model.lower()
    if m.startswith("nomic-embed-text"):
        return "clustering: "
    if m.startswith("embeddinggemma"):
        return "task: clustering | query: "
    return ""


def embed_documents(db_path: str, model: str, docs: list[str], prefix: str | None = None,
                    batch: int = 8, workers: int = 4, prune: bool = False):
    """Embed documents with Ollama, caching by content so an unchanged conversation is never
    embedded twice. Returns a normalised float32 matrix, one row per document. With `prune`,
    cached vectors for this model that no current document uses are deleted (a conversation that
    gained a reply has a new text, so its old vector is garbage)."""
    import httpx

    prefix = default_prefix(model) if prefix is None else prefix
    url = os.environ.get("OLLAMA_URL") or "http://127.0.0.1:11434"
    keys = [hashlib.sha1(f"{model}\0{prefix}\0{d}".encode()).hexdigest() for d in docs]
    con = sqlite3.connect(db_path, timeout=120)
    con.execute("PRAGMA busy_timeout=120000")
    con.execute(UNIT_TABLE)
    have: dict[str, np.ndarray] = {}
    for i in range(0, len(keys), 500):
        chunk = keys[i : i + 500]
        q = ",".join("?" * len(chunk))
        for k, v in con.execute(f"SELECT key, vec FROM unit_embeddings WHERE model=? AND key IN ({q})", [model, *chunk]):
            have[k] = np.frombuffer(v, dtype="<f4")
    todo = [i for i, k in enumerate(keys) if k not in have]
    # Longest first so the slow requests start early.
    todo.sort(key=lambda i: -len(docs[i]))
    log(f"unit embeddings ({model}): {len(docs) - len(todo)} cached, {len(todo)} to embed")

    def work(chunk):
        r = httpx.post(
            f"{url}/api/embed",
            json={"model": model, "input": [prefix + docs[i] for i in chunk], "truncate": True},
            timeout=600,
        )
        r.raise_for_status()
        vs = np.asarray(r.json()["embeddings"], dtype=np.float32)
        vs /= np.maximum(np.linalg.norm(vs, axis=1, keepdims=True), 1e-9)
        return chunk, vs

    chunks = [todo[i : i + batch] for i in range(0, len(todo), batch)]
    done = 0
    t0 = time.time()
    with ThreadPoolExecutor(workers) as ex:
        futs = [ex.submit(work, c) for c in chunks]
        for f in as_completed(futs):
            chunk, vs = f.result()
            con.executemany(
                "INSERT OR REPLACE INTO unit_embeddings(key,model,dim,vec) VALUES (?,?,?,?)",
                [(keys[i], model, vs.shape[1], vs[j].astype("<f4").tobytes()) for j, i in enumerate(chunk)],
            )
            con.commit()
            for j, i in enumerate(chunk):
                have[keys[i]] = vs[j]
            done += len(chunk)
            if done % 200 < len(chunk):
                log(f"  embedded {done}/{len(todo)} ({done / max(time.time() - t0, 1e-9):.1f}/s)")
    if prune:
        live = set(keys)
        stale = [k for (k,) in con.execute("SELECT key FROM unit_embeddings WHERE model=?", (model,)) if k not in live]
        for i in range(0, len(stale), 500):
            con.executemany("DELETE FROM unit_embeddings WHERE key=? AND model=?", [(k, model) for k in stale[i : i + 500]])
        con.commit()
        if stale:
            log(f"pruned {len(stale)} unused conversation vectors")
    con.close()
    return np.stack([have[k] for k in keys]).astype(np.float32)


# --------------------------------------------------------------------------- layout


def spiral_layout(xy_units, members, n_posts: int, fill: float = 0.20, iters: int = 1500,
                  pad: float = 1.15, pull: float = 0.002, seed: int = 0):
    """Place each unit's posts on a spiral (oldest at the centre) around the unit's position, then
    push overlapping units apart. Coordinates come out in the same units as xy_units (about
    [-1, 1]); `fill` is the share of the map the units' discs cover, which sets the point spacing.
    """
    from scipy.spatial import cKDTree

    s = math.sqrt(fill / max(n_posts, 1))
    sizes = np.array([len(m) for m in members], dtype=np.float64)
    radii = s * np.sqrt(sizes + 0.5) * pad
    start = np.asarray(xy_units, dtype=np.float64)
    pos = start.copy()
    rmax = float(radii.max())
    for _ in range(iters):
        tree = cKDTree(pos)
        pairs = np.array(sorted(tree.query_pairs(2 * rmax)), dtype=np.int64)
        if len(pairs) == 0:
            break
        a, b = pairs[:, 0], pairs[:, 1]
        d = pos[a] - pos[b]
        dist = np.linalg.norm(d, axis=1)
        need = radii[a] + radii[b]
        over = need - dist
        hit = over > 0
        if not hit.any() or over[hit].max() < 0.02 * s:
            break
        a, b, d, dist, over = a[hit], b[hit], d[hit], dist[hit], over[hit]
        # Coincident units get a random direction.
        rng = np.random.default_rng(seed)
        zero = dist < 1e-9
        if zero.any():
            ang = rng.uniform(0, 2 * np.pi, zero.sum())
            d[zero] = np.stack([np.cos(ang), np.sin(ang)], 1) * 1e-6
            dist[zero] = 1e-6
        push = (d / dist[:, None]) * (over[:, None] * 0.5 * 0.9)
        np.add.at(pos, a, push)
        np.add.at(pos, b, -push)
        pos += (start - pos) * pull
    # Posts on a golden-angle spiral, oldest at the centre.
    out = np.zeros((n_posts, 2), dtype=np.float64)
    golden = math.pi * (3 - math.sqrt(5))
    for u, m in enumerate(members):
        rot = (hash(u) % 1000) / 1000 * 2 * math.pi
        for k, i in enumerate(m):
            rr = s * math.sqrt(k + 0.5)
            th = k * golden + rot
            out[i, 0] = pos[u, 0] + rr * math.cos(th)
            out[i, 1] = pos[u, 1] + rr * math.sin(th)
    return out.astype(np.float32), pos
