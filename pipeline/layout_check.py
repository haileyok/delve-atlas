"""How well does a conversation-level snapshot hold its conversations together on the map?

    uv run python layout_check.py ../data/runs/full-cp/latest
"""

import json
import sys
from pathlib import Path

import numpy as np
from scipy.spatial import cKDTree

d = Path(sys.argv[1]).resolve()
cols = json.load(open(d / "cols.json"))
xy = np.fromfile(d / "xy.f32", dtype="<f4").reshape(-1, 2)
conv = np.array(cols["conv"])
topic = np.array(cols["topic"])
print(f"points {len(xy)}, conversations {conv.max() + 1}, xy range {xy.min(0).round(2)} .. {xy.max(0).round(2)}")

ts = np.array(cols["ts"])
sizes = np.bincount(conv)
big = sizes[conv] >= 5  # singletons and pairs have no inner structure to keep together

tree = cKDTree(xy)
dist, idx = tree.query(xy, k=6)
same = (conv[idx[:, 1:]] == conv[:, None]).mean(1)
print(f"posts in conversations of 5+: {big.mean():.2f} of all; share of their 5 nearest neighbours in the same conversation: {same[big].mean():.2f}")
print(f"share of a post's 5 nearest neighbours in the same topic: {(topic[idx[:, 1:]] == topic[:, None]).mean():.2f}")
print(f"median nearest-neighbour distance {np.median(dist[:, 1]):.4f} (the map is about 2 wide)")

# A conversation is a spiral around its oldest post.
centre = np.zeros((len(sizes), 2))
for u in range(len(sizes)):
    m = np.where(conv == u)[0]
    centre[u] = xy[m[np.argmin(ts[m])]]
rad = np.array([np.linalg.norm(xy[conv == u] - centre[u], axis=1).max() for u in range(len(sizes))])
pairs = cKDTree(centre).query_pairs(0.12)
over = [(a, b) for a, b in pairs if np.linalg.norm(centre[a] - centre[b]) < rad[a] + rad[b]]
bigover = [(a, b) for a, b in over if sizes[a] >= 5 and sizes[b] >= 5]
print(f"conversation discs that overlap another: {len(over)} pairs (of which both 5+: {len(bigover)}); {len(sizes)} conversations")

# Do a topic's posts form one blob? Share of each topic's posts within 2.5 median radii of its centre.
tids = sorted(set(topic.tolist()) - {-1})
tight = []
for t in tids:
    p = xy[topic == t]
    c = np.median(p, axis=0)
    r = np.linalg.norm(p - c, axis=1)
    tight.append(float((r <= 2.5 * np.median(r)).mean()))
print(f"topics: median share of posts within 2.5x median radius of the topic's centre {np.median(tight):.2f}")
