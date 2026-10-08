"""Side-by-side of diagnose.py results.  python3 compare.py run-dir [run-dir ...]"""
import json
import sys
from pathlib import Path

ROWS = [
    ("topics", "shape", "topics"),
    ("unclustered %", "shape", "unclustered_pct"),
    ("median posts/topic", "shape", "size_median"),
    ("threads/topic (med)", "conversations", "threads_per_topic_median"),
    ("eff. threads/topic", "conversations", "effective_threads_per_topic_median"),
    ("topics >50% one thread %", "conversations", "topics_where_one_thread_is_over_half_pct"),
    ("topics from <5 threads", "conversations", "topics_made_of_under_5_threads"),
    ("thread purity (wtd)", "conversations", "thread_purity_mean_posts_weighted"),
    ("threads split %", "conversations", "threads_split_across_topics_pct"),
    ("reply w/ parent topic %", "conversations", "reply_shares_topic_with_parent_pct"),
    ("eff. accounts/topic", "accounts", "effective_accounts_per_topic_median"),
    ("topics >40% one account", "accounts", "topics_one_account_over_40pct"),
    ("keyword NPMI (wtd)", "keywords", "npmi_size_weighted"),
    ("judge coherence", "judge", "coherence_mean_1to5"),
    ("judge coh. post-wtd", "judge", "coherence_post_weighted"),
    ("judge topics 4+ %", "judge", "topics_rated_4plus_pct"),
    ("judge outliers %", "judge", "outlier_share_of_sample_pct"),
    ("pairs same-subject", "judge_pairs", "same_subject"),
    ("pairs judged", "judge_pairs", "pairs_judged"),
]


def load(d):
    p = Path(d)
    for name in ("diag-full.json", "diag-judged.json", "diag.json"):
        if (p / name).exists():
            return json.load(open(p / name))
    return None


runs = [(Path(a).name.replace("screen-", "").replace("conv-", "c:"), load(a)) for a in sys.argv[1:]]
print(f"{'':26s}" + "".join(f"{n[:14]:>15s}" for n, _ in runs))
for label, sec, key in ROWS:
    vals = []
    for _, d in runs:
        v = (d or {}).get(sec, {}).get(key)
        vals.append("-" if v is None else str(v))
    if all(v == "-" for v in vals):
        continue
    print(f"{label:26s}" + "".join(f"{v:>15s}" for v in vals))
for n, d in runs:
    if d and "judge" in d:
        k = d["judge"].get("kind_post_weighted_pct", {})
        print(f"  kinds (post-wtd) {n}: {k}")
