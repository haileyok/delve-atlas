# Delve Atlas

A living map of what [Delve](https://delve.town) is talking about: the last seven days of posts on
the `town.delve.*` network, laid out on a 2D map by meaning, grouped into regions and topics with
readable labels, with live activity stats alongside. Modelled on
[atlas.jazco.dev](https://atlas.jazco.dev).

```
 Jetstream ──► ingest ──► SQLite ◄── resolve (handles, names via PLC + each PDS)
 (town.delve.*)             │  ▲
                            │  └── embed (local Ollama, nomic-embed-text)
                            ▼
                     pipeline/build_atlas.py     every ~6h
                     UMAP layout · HDBSCAN topics · regions · LLM labels (gateway)
                            │
                            ▼
                      data/atlas/<snapshot>/  (atlas.json, cols.json, xy.f32)
                            │
                            ▼
                         server ──► web/ (WebGL2 map, drawers, timeline)
                            ▲
                  live activity straight from SQLite
```

## What you get

- **The map**: one point per post, coloured by topic, sized by engagement. Region labels
  at a distance, topic labels as you zoom. Hover for the post, click for details.
- **Digging in**: click a region, topic, conversation, post or account for a drawer with its
  posts, who is talking, conversations, keywords and an LLM-written title and summary. Replies
  are linked to their parents; selecting a conversation draws its reply tree on the map.
- **Search** (`/`) over post text and account names; matching posts light up.
- **Timeline** along the bottom: hourly volume stacked by region. Drag to filter the map to a
  time range, `▶` (or space) replays the week.
- **Activity tab**: live per-hour posts, replies, likes, follows and new accounts, top
  accounts, most-liked posts, and every record type seen on the network.
- Colour by topic, age or engagement. Everything is deep-linkable (`#topic/12`, `#thread/…`).

## Running it

Prerequisites: Go, [uv](https://docs.astral.sh/uv/), Node (only for the headless UI checks) and a
local [Ollama](https://ollama.com) with `nomic-embed-text` pulled.

Settings live in `~/.config/delve-atlas/env` (read by the Makefile and `scripts/rebuild-loop.sh`):

| variable | used by | what |
|---|---|---|
| `JETSTREAM_API_KEY` | ingest | Jetstream archive replay key (required) |
| `AGW_KEY` | atlas build | gateway key for the cluster-labelling model; without it labels fall back to keywords |
| `JETSTREAM_HOST` | ingest | default `jetstream.us-east.bsky.network` |
| `OLLAMA_URL` | embed | default `http://127.0.0.1:11434` |
| `LABEL_MODEL` | atlas build | default `deepseek-v4.1-flash` |
| `LABEL_FALLBACK_MODEL` | atlas build | used when the first model returns nothing; default `glm-5.3-flash` |

```sh
make build
make ingest      # first run replays 7 days from the archive, then tails live; resumable
make embed       # embeds new posts as they arrive        (separate terminal / service)
make resolve     # fills in handles and display names     (separate terminal / service)
scripts/rebuild-loop.sh   # rebuild the atlas every 6h (or `make atlas` for one pass)
make serve       # http://localhost:8080
```

`make atlas` is also the first thing to run once the backfill has finished. The server serves the
newest snapshot, so a rebuild needs no restart; reload the page.

## Design notes

- **Backfill asks for commits only.** Account and identity events turn up in nearly every archive
  block, so asking for them makes the server plan whole-segment downloads (hundreds of MB each,
  rate limited) of the *entire* network. Commit-only with the `town.delve.*` wildcard keeps the plan
  sparse and the 7-day replay takes about half an hour. Handles come from the DID documents
  instead (`cmd/resolve`).
- **Replies carry their conversation.** Most Delve posts are replies, many just a few words
  ("o7"). Each reply is embedded with a snippet of its thread's first post so conversations land
  together on the map. The embedding row's model key records this recipe (`…+thread1`), so
  changing it means re-embedding rather than mixing vectors.
- **Labelling can fail quietly, so it retries.** The gateway models are reasoning models whose
  thinking counts against `max_tokens`. One was seen spending a whole 4,000-token budget counting
  the words of its own summary and returning nothing. `chat_json` retries with a bigger budget and
  then with `LABEL_FALLBACK_MODEL` (default `glm-5.3-flash`), and logs why.
- **Labels are stable across rebuilds.** A new topic inherits the previous snapshot's title and
  summary when at least half its posts (Jaccard ≥ 0.5) are shared, so the map doesn't relabel (or
  re-bill) itself every six hours. Only LLM-written labels are reused; keyword fallbacks are retried.
- **Layout is computed once, rendered by us.** `xy.f32` is the UMAP layout; the browser only draws.
  Label placement (regions pushed apart by a small relaxation pass, topics by priority and
  collision) is done per frame on a 2D canvas over the WebGL canvas.
- Snapshots are immutable directories (`data/atlas/<UTC time>/`) with a `latest` symlink; the
  client fetches heavy files by snapshot id, so they are cached forever.

## Checking the UI without a screen

`tools/cdp.mjs` drives headless Chromium over the DevTools protocol; the scenarios in
`tools/scenarios/` load the map, click through every kind of view, and assert on label counts and
overlap, tooltips, selection, search, the timeline brush and console errors:

```sh
node tools/cdp.mjs tools/scenarios/interact.mjs
node tools/cdp.mjs tools/scenarios/drawers.mjs
node tools/cdp.mjs tools/scenarios/labels.mjs
```

`tools/scenarios/mobile.mjs` does the same at a phone-sized viewport.
`tools/shot.sh out.png [path] [WxH]` takes a screenshot. Both find a nix-built Chromium
(`nix build nixpkgs#chromium --no-link --print-out-paths`) or use `$CHROMIUM`.

## Showing it to people

`make serve` (or `./bin/server -addr 127.0.0.1:8088`) serves the site and API from one binary with
the frontend embedded. It is read-only, has no accounts and takes no input beyond URLs, so it is
fine to put behind any reverse proxy or tunnel, for example
`cloudflared tunnel --url http://127.0.0.1:8088` for a quick shareable link. Everything heavy
(ingest, embeddings, the map build) runs on the machine hosting it; browsers only download about
1.6 MB (gzipped) per map. Links like `#topic/12` and `#thread/…` open a given view directly; topic
numbers change when the map is rebuilt, thread, post and account links don't.

The pages work on phones: the outline starts collapsed and a tapped post opens as a bottom sheet.

## Not done yet

- Jev-based labels (the broad/sub topic taxonomy and tone/substance signals that topic-feed uses)
  as an extra way to colour and filter posts, alongside the unsupervised clusters.
- A share-card image for link previews (the page has title and description tags only).
