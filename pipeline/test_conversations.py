"""Tests for the conversation units.   cd pipeline && uv run python -m unittest -v"""

import unittest

import numpy as np

import conversations as conv
from build_atlas import attach_by_similarity


def post(i, parent=None, root=None, text=None, root_text=""):
    uri = f"at://p/{i}"
    return {
        "uri": uri,
        "reply_parent": f"at://p/{parent}" if parent is not None else "",
        "reply_root": f"at://p/{root}" if root is not None else "",
        "text": text if text is not None else f"post {i}",
        "embed_text": "",
        "root_text": root_text,
    }


class SegmentTests(unittest.TestCase):
    def test_a_short_thread_is_one_unit(self):
        rows = [post(0), post(1, 0, 0), post(2, 1, 0), post(3, 0, 0)]
        unit_of, members = conv.segment(rows, max_posts=20)
        self.assertEqual(len(members), 1)
        self.assertEqual(members[0], [0, 1, 2, 3])
        self.assertTrue((unit_of == 0).all())

    def test_a_long_chain_is_cut_into_consecutive_stretches(self):
        rows = [post(0)] + [post(i, i - 1, 0) for i in range(1, 45)]
        _, members = conv.segment(rows, max_posts=20)
        self.assertEqual([len(m) for m in members], [20, 20, 5])
        # each stretch is consecutive in time
        for m in members:
            self.assertEqual(m, list(range(m[0], m[0] + len(m))))

    def test_replies_follow_their_parents_unit_and_unrelated_posts_start_their_own(self):
        rows = [post(0), post(1), post(2, 0, 0), post(3, 1, 1)]
        unit_of, _ = conv.segment(rows)
        self.assertEqual(unit_of.tolist(), [0, 1, 0, 1])

    def test_a_reply_whose_parent_is_outside_the_window_starts_a_unit(self):
        rows = [post(5, parent=99, root=99), post(6, 5, 99)]
        unit_of, members = conv.segment(rows)
        self.assertEqual(unit_of.tolist(), [0, 0])
        self.assertEqual(len(members), 1)

    def test_a_parent_that_sorts_after_its_child_does_not_break_anything(self):
        # clock skew: the child is listed first; it must not be attached to a unit that doesn't exist yet
        rows = [post(1, 0, 0), post(0)]
        unit_of, members = conv.segment(rows)
        self.assertEqual(sorted(sum(members, [])), [0, 1])
        self.assertTrue((unit_of >= 0).all())

    def test_every_post_lands_in_exactly_one_unit(self):
        rng = np.random.default_rng(0)
        rows = [post(0)]
        for i in range(1, 200):
            parent = int(rng.integers(0, i))
            rows.append(post(i, parent, 0))
        unit_of, members = conv.segment(rows, max_posts=12)
        self.assertEqual(sorted(sum(members, [])), list(range(200)))
        self.assertTrue(all(len(m) <= 12 for m in members))
        for u, m in enumerate(members):
            self.assertTrue(all(unit_of[i] == u for i in m))


class TranscriptTests(unittest.TestCase):
    def test_a_unit_that_does_not_start_the_thread_names_the_thread(self):
        rows = [post(1, 0, 0, text="a reply", root_text="Should agents have wallets?")]
        docs = conv.transcripts(rows, [[0]])
        self.assertIn("[thread: Should agents have wallets?]", docs[0])
        self.assertIn("- a reply", docs[0])

    def test_the_first_post_of_a_thread_needs_no_header(self):
        docs = conv.transcripts([post(0, text="opening post")], [[0]])
        self.assertNotIn("[thread", docs[0])

    def test_account_names_are_not_in_the_text(self):
        row = post(1, 0, 0, text="hello")
        row["did"] = "did:plc:secretname"
        self.assertNotIn("secretname", conv.transcripts([row], [[0]])[0])

    def test_long_conversations_are_cut_to_the_budget(self):
        rows = [post(i, i - 1 if i else None, 0 if i else None, text="x" * 280) for i in range(20)]
        doc = conv.transcripts(rows, [list(range(20))], max_chars=1000)[0]
        self.assertLessEqual(len(doc), 1100)
        self.assertGreater(doc.count("\n"), 1)


class PooledTests(unittest.TestCase):
    def test_the_unit_vector_is_the_normalised_mean(self):
        X = np.array([[1, 0], [0, 1], [1, 1]], dtype=np.float32)
        out = conv.pooled_vectors(X, [[0, 1], [2]])
        np.testing.assert_allclose(out[0], [2**-0.5, 2**-0.5], rtol=1e-5)
        np.testing.assert_allclose(np.linalg.norm(out, axis=1), 1, rtol=1e-5)


class LayoutTests(unittest.TestCase):
    def setUp(self):
        rng = np.random.default_rng(1)
        self.sizes = [1] * 30 + [3] * 15 + [12] * 6 + [40] * 2
        self.members, k = [], 0
        for s in self.sizes:
            self.members.append(list(range(k, k + s)))
            k += s
        self.n = k
        # start every unit nearly on top of the others, the worst case for overlap
        self.start = rng.normal(0, 0.05, (len(self.sizes), 2))

    def test_every_post_gets_a_position_near_its_unit_and_nothing_overlaps(self):
        xy, centres = conv.spiral_layout(self.start, self.members, self.n, fill=0.2)
        self.assertEqual(xy.shape, (self.n, 2))
        self.assertTrue(np.isfinite(xy).all())
        s = (0.2 / self.n) ** 0.5
        for u, m in enumerate(self.members):
            far = np.linalg.norm(xy[m] - centres[u], axis=1).max()
            self.assertLessEqual(far, s * (len(m) + 0.5) ** 0.5 * 1.01, "posts stay inside their conversation's disc")
        # discs (as planned) are pushed apart
        radii = np.array([s * (len(m) + 0.5) ** 0.5 for m in self.members])
        worst = 0.0
        for a in range(len(radii)):
            for b in range(a + 1, len(radii)):
                worst = max(worst, radii[a] + radii[b] - np.linalg.norm(centres[a] - centres[b]))
        self.assertLess(worst, 0.5 * s, "no two conversations may overlap by more than half a point spacing")

    def test_oldest_posts_sit_at_the_centre_of_the_spiral(self):
        xy, centres = conv.spiral_layout(self.start, self.members, self.n)
        s = (0.2 / self.n) ** 0.5
        for u, m in enumerate(self.members):
            self.assertLess(np.linalg.norm(xy[m[0]] - centres[u]), s * 0.75)

    def test_a_single_unit_is_fine(self):
        xy, _ = conv.spiral_layout(np.zeros((1, 2)), [[0, 1, 2]], 3)
        self.assertEqual(xy.shape, (3, 2))


class AttachTests(unittest.TestCase):
    def test_a_stray_row_joins_the_topic_it_resembles_but_not_a_distant_one(self):
        rng = np.random.default_rng(2)
        a = np.array([1.0, 0, 0, 0]) + rng.normal(0, 0.05, (30, 4))
        b = np.array([0, 1.0, 0, 0]) + rng.normal(0, 0.05, (30, 4))
        near_a = np.array([[1.0, 0.1, 0, 0]])
        far = np.array([[0, 0, 0, 1.0]])
        V = np.vstack([a, b, near_a, far]).astype(np.float32)
        V /= np.linalg.norm(V, axis=1, keepdims=True)
        labels = np.array([0] * 30 + [1] * 30 + [-1, -1])
        out = attach_by_similarity(V, labels, pct=5)
        self.assertEqual(out[60], 0)
        self.assertEqual(out[61], -1)

    def test_slack_makes_it_more_generous(self):
        V = np.array([[1, 0], [1, 0.05], [0.95, 0.05], [0.6, 0.8]], dtype=np.float32)
        V /= np.linalg.norm(V, axis=1, keepdims=True)
        labels = np.array([0, 0, 0, -1])
        self.assertEqual(attach_by_similarity(V, labels, pct=5, slack=0.0)[3], -1)
        self.assertEqual(attach_by_similarity(V, labels, pct=5, slack=1.0)[3], 0)

    def test_no_topics_changes_nothing(self):
        V = np.eye(3, dtype=np.float32)
        labels = np.array([-1, -1, -1])
        self.assertEqual(attach_by_similarity(V, labels).tolist(), [-1, -1, -1])


if __name__ == "__main__":
    unittest.main()
