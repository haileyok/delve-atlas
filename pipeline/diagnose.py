"""Measure how good a snapshot's clustering is, so changes can be compared with numbers.

    uv run python diagnose.py ../data/atlas/latest --db ../data/delve.db
        [--judge 40] [--judge-pairs 25] [--compare ../data/atlas/other] [--json out.json]

What it reports (the first group needs only the snapshot and the database):

  shape          topics, unclustered share, size spread
  conversations  how many separate threads feed a topic, how often one thread IS the topic,
                 how often a thread (or a reply and its parent) is split across topics
  accounts       how concentrated a topic is in one account (an account's voice, not a subject)
  short posts    share of very short posts in clustered and unclustered posts
  keywords       NPMI coherence of each topic's top words over the snapshot's own posts; it does
                 not depend on the embedding model, so it is comparable across embedders
  redundancy     topics whose centroids are nearly the same (one subject split in two)

With vectors (--db and the model the snapshot was built with):

  space          how close posts sit to their own topic centroid compared with the nearest other
                 topic, in the full embedding space. Only comparable between runs that use the
                 same embedding model.

Optional, spends a few LLM calls (needs AGW_KEY):

  --judge N      asks a model, for N sampled topics, how coherent ten of its posts are, how many
                 do not belong, and whether the topic is a subject, one account's persona, or
                 social chatter. Weighted by topic size it is the closest thing here to "does the
                 topic feel right", and it is independent of the embedding model.
  --judge-pairs  asks whether the most similar pairs of topics are really the same subject.

--compare DIR measures stability against another snapshot (adjusted Rand index and how many topics
have a counterpart with at least half the same posts).
"""

from __future__ import annotations

import argparse
import json
import math
import random
import re
import sqlite3
import sys
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import numpy as np

TOKEN = re.compile(r"(?u)\b[a-zA-Z][a-zA-Z'\-]{2,}\b")


def log(*a):
    print(*a, file=sys.stderr, flush=True)


# --------------------------------------------------------------------------- loading


def load_snapshot(d: Path):
    atlas = json.load(open(d / "atlas.json"))
    cols = json.load(open(d / "cols.json"))
    return atlas, cols


def load_posts(db: str, uris: list[str], model: str | None):
    """Own text, thread root and (if the model is known) the vector for each snapshot post."""
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    info: dict[str, dict] = {}
    vecs: dict[str, bytes] = {}
    for i in range(0, len(uris), 500):
        chunk = uris[i : i + 500]
        q = ",".join("?" * len(chunk))
        for r in con.execute(
            f"""SELECT p.uri, p.did, p.text, p.embed_text, p.reply_root, p.reply_parent,
                       COALESCE(r.text, '') AS root_text
                FROM posts p LEFT JOIN posts r ON r.uri = p.reply_root AND p.reply_root <> p.uri
                WHERE p.uri IN ({q})""",
            chunk,
        ):
            info[r["uri"]] = dict(r)
        if model:
            for r in con.execute(f"SELECT uri, vec FROM embeddings WHERE model = ? AND uri IN ({q})", [model, *chunk]):
                vecs[r["uri"]] = r["vec"]
    con.close()
    X = None
    if vecs:
        dim = len(next(iter(vecs.values()))) // 4
        X = np.zeros((len(uris), dim), dtype=np.float32)
        for i, u in enumerate(uris):
            if u in vecs:
                X[i] = np.frombuffer(vecs[u], dtype="<f4")
        n = np.linalg.norm(X, axis=1, keepdims=True)
        X = X / np.maximum(n, 1e-9)
    return info, X


# --------------------------------------------------------------------------- metrics


def eff_n(counts) -> float:
    c = np.array(list(counts), dtype=float)
    p = c / c.sum()
    return float(math.exp(-(p * np.log(p)).sum()))


def shape(topic):
    cl = topic[topic >= 0]
    sizes = np.bincount(cl)
    sizes = sizes[sizes > 0]
    return {
        "posts": int(len(topic)),
        "topics": int(len(sizes)),
        "unclustered_pct": round(100 * float((topic < 0).mean()), 1),
        "size_p10": int(np.percentile(sizes, 10)),
        "size_median": int(np.median(sizes)),
        "size_p90": int(np.percentile(sizes, 90)),
        "size_max": int(sizes.max()),
        "largest_topic_pct": round(100 * float(sizes.max() / len(topic)), 1),
    }


def conversations(topic, roots, parents_idx):
    """roots[i] is the conversation (thread root) of post i."""
    by_topic = defaultdict(list)
    for i, t in enumerate(topic):
        if t >= 0:
            by_topic[int(t)].append(roots[i])
    distinct, top_share, eff = [], [], []
    for t, rs in by_topic.items():
        c = Counter(rs)
        distinct.append(len(c))
        top_share.append(c.most_common(1)[0][1] / len(rs))
        eff.append(eff_n(c.values()))
    by_thread = defaultdict(list)
    for i, r in enumerate(roots):
        by_thread[r].append(int(topic[i]))
    big = [v for v in by_thread.values() if len(v) >= 3]
    purity = [Counter(v).most_common(1)[0][1] / len(v) for v in big]
    weights = [len(v) for v in big]
    same = tot = 0
    for i, p in enumerate(parents_idx):
        if p >= 0 and topic[i] >= 0 and topic[p] >= 0:
            tot += 1
            same += topic[i] == topic[p]
    return {
        "threads_per_topic_median": int(np.median(distinct)),
        "threads_per_topic_min": int(min(distinct)),
        "effective_threads_per_topic_median": round(float(np.median(eff)), 1),
        "topics_where_one_thread_is_over_half_pct": round(100 * float(np.mean([s > 0.5 for s in top_share])), 1),
        "topics_made_of_under_5_threads": int(sum(d < 5 for d in distinct)),
        "thread_purity_mean_posts_weighted": round(float(np.average(purity, weights=weights)), 3),
        "threads_3plus": len(big),
        "threads_split_across_topics_pct": round(100 * float(np.mean([p < 0.8 for p in purity])), 1),
        "reply_shares_topic_with_parent_pct": round(100 * same / max(tot, 1), 1),
    }


def accounts(topic, dids):
    by_topic = defaultdict(list)
    for i, t in enumerate(topic):
        if t >= 0:
            by_topic[int(t)].append(dids[i])
    share, eff = [], []
    for rs in by_topic.values():
        c = Counter(rs)
        share.append(c.most_common(1)[0][1] / len(rs))
        eff.append(eff_n(c.values()))
    return {
        "effective_accounts_per_topic_median": round(float(np.median(eff)), 1),
        "top_account_share_median_pct": round(100 * float(np.median(share)), 1),
        "topics_one_account_over_40pct": int(sum(s > 0.4 for s in share)),
        "topics_under_6_effective_accounts": int(sum(e < 6 for e in eff)),
    }


def short_posts(topic, texts):
    n = np.array([len((t or "").strip()) for t in texts])
    short = n < 40
    out = {"short_under_40_chars_pct_all": round(100 * float(short.mean()), 1)}
    if (topic < 0).any():
        out["short_pct_of_unclustered"] = round(100 * float(short[topic < 0].mean()), 1)
        out["short_pct_of_clustered"] = round(100 * float(short[topic >= 0].mean()), 1)
    return out


def keyword_npmi(topic_words, doc_tokens):
    """Mean NPMI over pairs of each topic's top words; documents are the snapshot's posts."""
    vocab = {w for ws in topic_words.values() for w in ws}
    df = Counter()
    co = Counter()
    for toks in doc_tokens:
        present = sorted(toks & vocab)
        df.update(present)
        for a in range(len(present)):
            for b in range(a + 1, len(present)):
                co[(present[a], present[b])] += 1
    n = len(doc_tokens)
    scores = {}
    for t, ws in topic_words.items():
        vals = []
        for a in range(len(ws)):
            for b in range(a + 1, len(ws)):
                x, y = sorted((ws[a], ws[b]))
                pxy = co.get((x, y), 0) / n
                px, py = df[x] / n, df[y] / n
                if pxy <= 0 or px <= 0 or py <= 0:
                    vals.append(-1.0)
                    continue
                vals.append(math.log(pxy / (px * py)) / -math.log(pxy))
        if vals:
            scores[t] = float(np.mean(vals))
    return scores


def space(topic, X):
    ids = sorted(set(topic.tolist()) - {-1})
    cent = np.stack([X[topic == t].mean(0) for t in ids])
    cent /= np.maximum(np.linalg.norm(cent, axis=1, keepdims=True), 1e-9)
    mask = topic >= 0
    S = X[mask] @ cent.T
    own = np.array([ids.index(t) for t in topic[mask]])
    own_s = S[np.arange(len(own)), own]
    S2 = S.copy()
    S2[np.arange(len(own)), own] = -1
    other = S2.max(1)
    rng = np.random.default_rng(0)
    a = rng.integers(0, len(X), 20000)
    b = rng.integers(0, len(X), 20000)
    return cent, ids, {
        "mean_cos_to_own_centroid": round(float(own_s.mean()), 3),
        "mean_cos_to_nearest_other_centroid": round(float(other.mean()), 3),
        "margin": round(float((own_s - other).mean()), 3),
        "nearest_centroid_is_own_pct": round(100 * float((own_s > other).mean()), 1),
        "random_pair_cos": round(float((X[a] * X[b]).sum(1).mean()), 3),
    }


def redundancy(cent, ids, titles, kw, k=8):
    S = cent @ cent.T
    np.fill_diagonal(S, -1)
    pairs = []
    for i in range(len(ids)):
        for j in range(i + 1, len(ids)):
            pairs.append((float(S[i, j]), ids[i], ids[j]))
    pairs.sort(reverse=True)
    return pairs, [
        {"cos": round(c, 3), "a": f"{a}: {titles.get(a, '')}", "b": f"{b}: {titles.get(b, '')}"}
        for c, a, b in pairs[:k]
    ]


def compare(topic, uris, other_dir: Path):
    from sklearn.metrics import adjusted_rand_score, normalized_mutual_info_score

    oc = json.load(open(other_dir / "cols.json"))
    other = dict(zip(oc["uri"], oc["topic"]))
    idx = [i for i, u in enumerate(uris) if u in other and topic[i] >= 0 and other[u] >= 0]
    a = [int(topic[i]) for i in idx]
    b = [other[uris[i]] for i in idx]
    ma, mb = defaultdict(set), defaultdict(set)
    for i in idx:
        ma[int(topic[i])].add(uris[i])
        mb[other[uris[i]]].add(uris[i])
    matched = 0
    for s in ma.values():
        best = max((len(s & t) / len(s | t) for t in mb.values()), default=0)
        matched += best >= 0.5
    return {
        "shared_clustered_posts": len(idx),
        "ARI": round(adjusted_rand_score(a, b), 3),
        "NMI": round(normalized_mutual_info_score(a, b), 3),
        "topics_with_counterpart_pct": round(100 * matched / max(len(ma), 1), 1),
    }


# --------------------------------------------------------------------------- LLM judge

JUDGE_PROMPT = """Below are {n} posts that an algorithm grouped together on a map of a social network
where most accounts are AI agents. Replies are shown with a snippet of the thread they belong to.

{posts}

Judge the grouping, not the writing. Return JSON only:
{{"coherence": 1-5, "outliers": number of posts that clearly do not belong with the rest, "kind": "subject" | "persona" | "chatter" | "mixed", "reason": "at most 15 words"}}
coherence: 5 = all about one specific subject, 3 = a loose theme, 1 = unrelated mix.
kind: subject = grouped by what is discussed; persona = grouped because one account's voice or format repeats; chatter = greetings, thanks, short social replies; mixed = none of these dominates."""

PAIR_PROMPT = """Two clusters from a map of posts on a social network where most accounts are AI agents.

Cluster A "{ta}"
{pa}

Cluster B "{tb}"
{pb}

Are these two clusters about the same subject, so a reader would want them merged?
Return JSON only: {{"verdict": "same" | "related" | "different", "reason": "at most 15 words"}}"""


def show(info, uri, own_text, n=230):
    from build_atlas import clip

    r = info.get(uri) or {}
    s = clip(own_text or r.get("text") or r.get("embed_text") or "", n)
    if r.get("root_text"):
        return f"- (thread: {clip(r['root_text'], 110)}) {s}"
    return f"- {s}"


def judge(topic, uris, texts, info, sample_topics, pairs, rng):
    from build_atlas import chat_json

    members = defaultdict(list)
    for i, t in enumerate(topic):
        if t >= 0:
            members[int(t)].append(i)

    def sample(t, k):
        m = members[t]
        return rng.sample(m, min(k, len(m)))

    def one(t):
        idx = sample(t, 10)
        r = chat_json(JUDGE_PROMPT.format(n=len(idx), posts="\n".join(show(info, uris[i], texts[i]) for i in idx)))
        return t, r

    out_topics = {}
    with ThreadPoolExecutor(8) as ex:
        for t, r in ex.map(one, sample_topics):
            if r and "coherence" in r:
                out_topics[t] = r

    def pair(p):
        _, a, b = p
        ia, ib = sample(a, 6), sample(b, 6)
        r = chat_json(
            PAIR_PROMPT.format(
                ta=titles_global.get(a, a),
                tb=titles_global.get(b, b),
                pa="\n".join(show(info, uris[i], texts[i], 160) for i in ia),
                pb="\n".join(show(info, uris[i], texts[i], 160) for i in ib),
            )
        )
        return p, r

    out_pairs = []
    with ThreadPoolExecutor(8) as ex:
        for p, r in ex.map(pair, pairs):
            if r and "verdict" in r:
                out_pairs.append((p, r))
    return members, out_topics, out_pairs


titles_global: dict[int, str] = {}


# --------------------------------------------------------------------------- main


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("snapshot")
    ap.add_argument("--db", default="../data/delve.db")
    ap.add_argument("--model", default="", help="embedding model key (default: the snapshot's)")
    ap.add_argument("--judge", type=int, default=0, help="topics to sample for the LLM judge (0 = skip)")
    ap.add_argument("--judge-pairs", type=int, default=0, help="nearest topic pairs to judge")
    ap.add_argument("--compare", default="")
    ap.add_argument("--json", default="")
    ap.add_argument("--seed", type=int, default=1)
    args = ap.parse_args()

    snap = Path(args.snapshot)
    atlas, cols = load_snapshot(snap)
    uris = cols["uri"]
    topic = np.array(cols["topic"])
    model = args.model or atlas.get("model") or None
    info, X = load_posts(args.db, uris, model)
    log(f"{len(uris)} posts, vectors: {'yes' if X is not None else 'no'} (model {model})")

    texts = [info.get(u, {}).get("text") or info.get(u, {}).get("embed_text") or "" for u in uris]
    dids = [info.get(u, {}).get("did", "") for u in uris]
    roots = [info.get(u, {}).get("reply_root") or u for u in uris]
    idx_of = {u: i for i, u in enumerate(uris)}
    parents_idx = [idx_of.get(info.get(u, {}).get("reply_parent") or "", -1) for u in uris]

    titles = {t["id"]: t["title"] for t in atlas["topics"]}
    titles_global.update(titles)
    topic_words = {}
    for t in atlas["topics"]:
        ws = []
        for k in t["keywords"]:
            for w in TOKEN.findall(k.lower()):
                if w not in ws:
                    ws.append(w)
        topic_words[t["id"]] = ws[:10]
    doc_tokens = [set(TOKEN.findall(t.lower())) for t in texts]

    report: dict = {"snapshot": snap.name, "model": model}
    report["shape"] = shape(topic)
    report["conversations"] = conversations(topic, roots, parents_idx)
    report["accounts"] = accounts(topic, dids)
    report["short_posts"] = short_posts(topic, texts)
    npmi = keyword_npmi(topic_words, doc_tokens)
    sizes = {t["id"]: t["n"] for t in atlas["topics"]}
    report["keywords"] = {
        "npmi_mean": round(float(np.mean(list(npmi.values()))), 3),
        "npmi_size_weighted": round(float(np.average([npmi[t] for t in npmi], weights=[sizes[t] for t in npmi])), 3),
        "topics_with_npmi_under_0.05": int(sum(v < 0.05 for v in npmi.values())),
    }
    pairs_all = []
    if X is not None and (X != 0).any():
        cent, ids, sp = space(topic, X)
        report["space"] = sp
        pairs_all, red = redundancy(cent, ids, titles, topic_words)
        report["most_similar_topic_pairs"] = red
    if args.compare:
        report["stability_vs_" + Path(args.compare).name] = compare(topic, uris, Path(args.compare))

    if args.judge or args.judge_pairs:
        rng = random.Random(args.seed)
        all_t = sorted(set(topic.tolist()) - {-1})
        sample_t = rng.sample(all_t, min(args.judge, len(all_t))) if args.judge else []
        members, jt, jp = judge(topic, uris, texts, info, sample_t, pairs_all[: args.judge_pairs], rng)
        if jt:
            w = [len(members[t]) for t in jt]
            coh = [float(jt[t]["coherence"]) for t in jt]
            out = [min(float(jt[t].get("outliers", 0)), 10) / 10 for t in jt]
            kinds = Counter(jt[t].get("kind", "?") for t in jt)
            report["judge"] = {
                "topics_judged": len(jt),
                "coherence_mean_1to5": round(float(np.mean(coh)), 2),
                "coherence_post_weighted": round(float(np.average(coh, weights=w)), 2),
                "topics_rated_4plus_pct": round(100 * float(np.mean([c >= 4 for c in coh])), 1),
                "outlier_share_of_sample_pct": round(100 * float(np.mean(out)), 1),
                "kinds": dict(kinds),
                "kind_post_weighted_pct": {
                    k: round(100 * sum(w[i] for i, t in enumerate(jt) if jt[t].get("kind") == k) / sum(w), 1)
                    for k in kinds
                },
                "worst": [
                    {"id": t, "title": titles.get(t, ""), "n": len(members[t]), "kind": jt[t].get("kind"),
                     "coherence": jt[t]["coherence"], "reason": jt[t].get("reason", "")}
                    for t in sorted(jt, key=lambda t: float(jt[t]["coherence"]))[:6]
                ],
            }
        if jp:
            verdicts = Counter(r["verdict"] for _, r in jp)
            report["judge_pairs"] = {
                "pairs_judged": len(jp),
                "same_subject": verdicts.get("same", 0),
                "related": verdicts.get("related", 0),
                "different": verdicts.get("different", 0),
                "same_examples": [
                    f"{titles.get(p[1], p[1])}  <->  {titles.get(p[2], p[2])}  (cos {p[0]:.2f})"
                    for p, r in jp
                    if r["verdict"] == "same"
                ][:6],
            }

    for k, v in report.items():
        if isinstance(v, dict):
            print(f"\n[{k}]")
            for a, b in v.items():
                print(f"  {a}: {b}")
        elif isinstance(v, list):
            print(f"\n[{k}]")
            for x in v:
                print("  ", x)
        else:
            print(f"{k}: {v}")
    if args.json:
        json.dump(report, open(args.json, "w"), indent=2)


if __name__ == "__main__":
    main()
