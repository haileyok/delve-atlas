#!/usr/bin/env bash
# Build conversation-level variants on the sample and diagnose them (no judge).
# Usage: variants.sh name "build_atlas args..." [name "args"...]
set -uo pipefail
cd "$(dirname "$0")/.."
while [ $# -ge 2 ]; do
  name=$1; args=$2; shift 2
  out=../data/runs/$name
  rm -rf "$out"
  echo "=== $name: $args"
  uv run python build_atlas.py --db ../data/sample.db --out "$out" --model nomic-embed-text+thread1 \
    --no-labels --days 7 --workers 8 $args 2>&1 | grep -E 'unit embeddings|topics,|Traceback|Error'
  uv run python diagnose.py "$out/latest" --db ../data/sample.db --json "$out/diag.json" > "$out/diag.txt" 2>&1
done
