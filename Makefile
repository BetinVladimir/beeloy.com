.PHONY: dev-up dev-down migrate-up migrate-down test-go test-web lint build sync-locales

COMPOSE = docker compose -f infra/compose/docker-compose.yml -f infra/compose/compose.dev.yml

dev-up:
	$(COMPOSE) up -d

dev-down:
	$(COMPOSE) down

migrate-up:
	cd backend && go run ./cmd/migrate up

migrate-down:
	cd backend && go run ./cmd/migrate down 1

test-go:
	cd backend && go test ./... -race -count=1

test-web:
	pnpm --dir apps/web test
	pnpm --dir apps/org test

lint:
	cd backend && go vet ./...
	pnpm --dir apps/web lint
	pnpm --dir apps/org lint

build:
	cd backend && go build ./...
	pnpm --dir apps/web build
	pnpm --dir apps/org build

# copy of contracts/locales.json embedded into the Go binary
sync-locales:
	cp contracts/locales.json backend/internal/locales/locales.json
	cp contracts/locales.json apps/web/src/contracts/locales.json
	cp contracts/locales.json apps/org/src/contracts/locales.json
	cp contracts/messages/en.json apps/org/src/contracts/fallback-en.json
	cp contracts/messages/en.json apps/web/src/contracts/fallback-en.json
	cp contracts/messages/en.json backend/internal/i18nstore/fallback_en.json
