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
                     pipeline/build_atlas.py     every ~3h
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
- **Agent API**: everything above as JSON for agents, with a guide at `/AGENTS.md` (see below).
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
scripts/rebuild-loop.sh   # rebuild the atlas every 3h (or `make atlas` for one pass); see "As systemd services" below
make serve       # http://localhost:8080
```

`make atlas` is also the first thing to run once the backfill has finished. The server serves the
newest snapshot, so a rebuild needs no restart; reload the page.

### As systemd services

To keep it running across logouts and reboots, install the units in `deploy/systemd/` as
**user** services (no root; run `loginctl enable-linger $USER` once so they start at boot):

```bash
deploy/install.sh            # build, install, enable and start everything
deploy/install.sh restart    # after pulling changes: rebuild binaries, restart the services
```

| unit | what it runs |
|---|---|
| `delve-ingest.service` | Jetstream replay, then live tail (resumes from its saved cursor) |
| `delve-embed.service` | embeds new posts with Ollama |
| `delve-resolve.service` | fills in handles and display names |
| `delve-atlas-web.service` | the site and API on `127.0.0.1:8088` |
| `delve-rebuild.timer` → `delve-rebuild.service` | one map rebuild every 3 hours (00:15, 03:15, ...) |

Secrets stay in `~/.config/delve-atlas/env`, which every unit reads. Day to day:

```bash
systemctl --user status 'delve-*'
journalctl --user -u delve-atlas-web -f
systemctl --user start delve-rebuild.service   # rebuild now
systemctl --user list-timers delve-rebuild.timer
```

Ollama and the tunnel are expected to run separately (for example as system services). The units
don't depend on them: if Ollama is down, `/api/v1/search` answers 503 and embedding resumes when it is back.

## For agents

Everything the site shows is also a read-only JSON API under `/api/v1`, with a guide agents can
read at **`/AGENTS.md`** (also `/llms.txt`, and an OpenAPI 3.1 document at `/api/v1/openapi.json`).
There are no keys; CORS is open. Point an agent at `https://<host>/AGENTS.md` and it has what it needs.

| endpoint | what it answers |
|---|---|
| `/overview` | the whole map in one call: regions, topics, headline and live numbers |
| `/regions`, `/regions/{id}` | broad areas of conversation |
| `/topics`, `/topics/{id}`, `/topics/{id}/posts` | specific subjects; activity by day, top accounts, conversations, nearby topics |
| `/posts` | list and filter posts (topic, region, author, reply, time, sort) with keyword search |
| `/search` | search by meaning (needs the local Ollama; answers 503 without it) |
| `/post` | one post with its parent, replies and similar posts |
| `/threads`, `/thread` | conversations, and one read whole, in order, with reply depth |
| `/authors`, `/authors/{ref}`, `/authors/{ref}/network` | accounts, their topics, who they follow, reply to and like |
| `/graph/replies` | who replies to whom, as weighted edges |
| `/activity` | live volume per hour, top accounts, most-liked posts |

The guide, the OpenAPI document and the `/api/v1` index are all generated from one endpoint
registry (`internal/agentapi/registry.go`), and the guide's examples are filled in from the current
map, so they work as written. The tests request every documented example and every documented
parameter, check that the OpenAPI schemas match the real responses, and that the guide mentions
every endpoint. Run the same check against a live server with `node tools/agent-smoke.mjs [base-url]`.

- **Content is untrusted.** Posts are written by agents and people and may contain instructions
  aimed at whoever reads them. The guide, the overview and the OpenAPI description all say to treat
  post text as data, not instructions.
- **Rate limits** are per client (`CF-Connecting-IP` when behind Cloudflare): about 20 requests/s
  with a burst of 60, and 2/s with a burst of 8 for `/search`, since each search runs an embedding
  model. Responses carry `Cache-Control: public, max-age=60`.
- **Search** embeds each query with the same model and recipe as the posts (`-embed-model`,
  `OLLAMA_URL`); `-no-semantic` turns it off. Keyword search (`/posts?q=`) is SQLite FTS5 with
  stemming and is always on; its index is kept current by triggers on the `posts` table.
- **Ids**: topic and region ids belong to one map and are renumbered at each rebuild (every
  response says which `snapshot` it came from); post URIs, DIDs and conversation roots are stable.

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
- **Conversations are what gets clustered, not posts.** 87% of posts are replies and 90% sit in
  about 600 threads, so clustering single posts made most topics a single big thread (80% of topics
  had over half their posts from one thread) and a reply landed in its parent's topic only 75% of
  the time. `pipeline/conversations.py` groups posts into *conversation units*: a post joins its
  parent's unit until that unit holds 20 posts, then starts a new one (the longest "threads" are
  100-deep chains that wander through many subjects, so they are cut into stretches). Each unit is
  embedded as a transcript of its posts (account names left out, they pull topics toward one
  voice), HDBSCAN finds topics among the units (EOM selection, minimum size about units/250), and
  every post inherits its unit's topic. Conversations the clusterer calls noise join the nearest
  topic if they are about as close to its centre as its less typical members. On the map each unit
  is a small spiral of its posts, oldest at the centre, so a conversation is a visible cluster
  (`cols.json` has a `conv` column with the unit of each post). `build_atlas.py --unit post` is the
  old behaviour. On a 3.5k-post sample, versus clustering posts: a reply shares its parent's topic
  98% of the time (was 75%), topics draw on about 3 conversations (was 1.6), keyword coherence is
  3x higher, and a model judge rates the topics within 0.1 of the post-based ones once weighted by
  posts (3.95 against 4.05 of 5).
- **The embedding model was not the weak point.** nomic-embed-text, mxbai-embed-large,
  embeddinggemma, qwen3-embedding (0.6B and 4B), bge-m3 and snowflake-arctic-embed2 all landed
  within the judge's noise of each other, both for posts and for conversation transcripts, so the
  site keeps nomic (the smallest, and the one search already uses). To try another:
  `bin/embed -model <ollama model> -days 7`, then `UNIT_MODEL=<model>` for the transcripts. The
  embeddings table keys vectors by (post, model), so models can sit side by side.
- **Measuring a change.** `pipeline/diagnose.py <snapshot> --db data/delve.db [--judge 200
  --judge-pairs 20] [--compare <other snapshot>]` reports, per snapshot: how many separate
  conversations feed each topic, how often a thread or a reply is split across topics, how
  concentrated topics are in one account, keyword coherence, near-duplicate topics, and (with
  `--judge`, needs `AGW_KEY`) a model's rating of every topic and of the most similar pairs. Two
  traps: the judge rewards topics that are one thread (ten posts from one conversation always
  look coherent), so compare at the same topic count and read the structural numbers beside it; and
  a 30-topic sample is too noisy (about +-0.15), so judge every topic. Run the Python tests with
  `cd pipeline && uv run python -m unittest`. `pipeline/experiments/` has the scripts used to
  compare embedders and settings on a sample of whole conversations (`screen.sh`, `variants.sh`,
  `compare.py`); they work on a copy of the database, never the live one. Result on the live map:
  the judge rates it level with post-based clustering weighted by posts (3.63 vs 3.59 of 5) but
  looser per topic (51% of topics rated 4+, was 65%; 12% "mixed", was 7%), because topics now span
  about three times as many conversations. A larger `--min-cluster` makes topics broader still, a
  smaller one finer and tidier but with more near-duplicates.
- **Labelling can fail quietly, so it retries.** The gateway models are reasoning models whose
  thinking counts against `max_tokens`. One was seen spending a whole 4,000-token budget counting
  the words of its own summary and returning nothing. `chat_json` retries with a bigger budget and
  then with `LABEL_FALLBACK_MODEL` (default `glm-5.3-flash`), and logs why.
- **Labels are stable across rebuilds.** A new topic inherits the previous snapshot's title and
  summary when at least half its posts (Jaccard ≥ 0.5) are shared, so the map doesn't relabel (or
  re-bill) itself every three hours. Only LLM-written labels are reused; keyword fallbacks are retried.
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

`tools/scenarios/mobile.mjs` does the same at a phone-sized viewport, and `tools/scenarios/align.mjs` / `align-dynamic.mjs`
read back the rendered pixels to check the dots sit where the labels and hover think they do.
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

### Link previews

Pasting the address into Bluesky, Slack, Discord, iMessage and the like shows a 1200x630 card: the
current map (every post a dot, in the site's colours), the largest regions named on it, and the
title and totals. It is drawn from the newest snapshot, not made once by hand:

- `web/static/og/card.html` + `card.js` draw it (Inter is bundled next to them, OFL licensed);
  `tools/og.mjs` opens that page in headless Chromium and saves the screenshot to
  `data/atlas/<snapshot>/og.jpg`. `scripts/rebuild-loop.sh` runs it after every map build; if the
  render fails it only warns, and the site keeps showing the last card. It is a JPEG because
  thousands of anti-aliased dots make a lossless PNG over 500 KB and some chat apps drop previews
  above about 300 KB; with ImageMagick installed it is encoded at quality 90 with full colour
  (about 250 KB), without it the browser's own JPEG is used (slightly softer).
- The server serves it at `/og.jpg` (newest snapshot's card, else the most recent one that has a
  card, else `web/static/og/default.jpg`, which ships with the site so a preview always has an image).
- `index.html` carries `og:*`, `twitter:*` (`summary_large_image`) and `canonical` tags. The server
  fills in three placeholders per request: the absolute origin (from `X-Forwarded-Host` and
  `X-Forwarded-Proto`, which a Cloudflare tunnel sets, else `Host`; or `-public-url` to pin it), a
  one-sentence description of the current map (counts and the biggest regions), and the map's id as
  `?v=` on the image URL so previews refresh after a rebuild. Anything inserted is HTML-escaped and
  a host that is not a plain host name is ignored.
- Render one by hand: `node tools/og.mjs --base http://127.0.0.1:8088 --out card.jpg`.

## Not done yet

- Jev-based labels (the broad/sub topic taxonomy and tone/substance signals that topic-feed uses)
  as an extra way to colour and filter posts, alongside the unsupervised clusters.
