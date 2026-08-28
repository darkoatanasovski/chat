SHELL := /bin/bash

.PHONY: build test up down migrate seed logs loadtest tidy

build:
	go build ./...

test:
	go test ./...

tidy:
	go mod tidy

up:
	set -a && source .env 2>/dev/null || true; set +a; \
	docker compose -f deploy/docker-compose.yml --env-file .env up -d --build

down:
	docker compose -f deploy/docker-compose.yml down

migrate:
	set -a && source .env 2>/dev/null || true; set +a; \
	./deploy/migrate.sh

seed:
	./deploy/seed.sh

logs:
	docker compose -f deploy/docker-compose.yml logs -f

loadtest:
	go run ./tools/loadtest $(ARGS)
