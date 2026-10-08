ENV_FILE ?= $(HOME)/.config/delve-atlas/env
RUN = set -a; [ -f $(ENV_FILE) ] && . $(ENV_FILE); set +a;

.PHONY: build ingest embed resolve atlas serve test check-web shot

build:
	go build -o bin/ingest ./cmd/ingest
	go build -o bin/embed ./cmd/embed
	go build -o bin/resolve ./cmd/resolve
	go build -o bin/server ./cmd/server

# Follow Jetstream: replays the last 7 days on the first run, then tails live.
ingest: build
	$(RUN) ./bin/ingest -days 7

embed: build
	$(RUN) ./bin/embed -watch

resolve: build
	$(RUN) ./bin/resolve -watch

# One rebuild pass of the atlas (embed + resolve + layout + labels).
atlas: build
	$(RUN) scripts/rebuild-loop.sh once

serve: build
	$(RUN) ./bin/server -addr :8080

test:
	go vet ./...
	go test ./...

# Syntax-check the frontend modules.
check-web:
	@for f in web/static/js/*.js; do node --input-type=module --check < $$f || exit 1; done; echo ok
