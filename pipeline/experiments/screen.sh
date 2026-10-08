#!/usr/bin/env bash
# Screen embedding models on the sample database: embed, build the map without LLM labels,
# measure. Usage: screen.sh model...   (nomic-embed-text uses the vectors already there)
set -uo pipefail
cd "$(dirname "$0")/../.."
DB=data/sample.db
for m in "$@"; do
  name=$(echo "$m" | tr ':/' '__')
  key="$m+thread1"
  out=data/runs/screen-$name
  echo "=== $m"
  if [ "$m" != "nomic-embed-text" ]; then
    /usr/bin/time -f "embed wall %es" ./bin/embed -db $DB -model "$m" -recipe thread1 -days 7 -workers 2 2>&1 | tail -2
  fi
  rm -rf "$out"
  (cd pipeline && uv run python build_atlas.py --db ../$DB --out ../$out --model "$key" --no-labels --days 7 --workers 8 2>&1 | grep -E 'topics|wrote|Error|Traceback' )
  (cd pipeline && uv run python diagnose.py ../$out/latest --db ../$DB --json ../$out/diag.json 2>&1 | grep -v -i warn > ../$out/diag.txt)
  echo "done $m"
done
