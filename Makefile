export DATABASE_URL ?= postgres://yorm:yorm@localhost:5432/yorm?sslmode=disable
export YORM_TOKEN_SECRET ?= dev-only-secret-do-not-use-in-prod-0123456789
export YORM_ALLOWED_ORIGINS ?= localhost:5173

.PHONY: db-up db-down run web-install web-dev web-build web-test fixtures token test race integration fuzz lint

db-up:
	docker compose up -d --wait postgres

db-down:
	docker compose down

run:
	go run ./cmd/yormd

web-install:
	cd web && npm ci

# Vite on :5173, forwarding API and WebSocket calls to `make run` on :8080.
web-dev:
	cd web && npm run dev

web-build:
	cd web && npm run build

web-test:
	cd web && npm test

# Rewrite web/src/testdata from the Go test cases the frontend shares.
fixtures:
	UPDATE_FIXTURES=1 go test -run Fixture ./internal/game

# Usage: make token SESSION=demo USER_ID=dm1 ROLE=dm
token:
	@go run ./cmd/yormd mint-token -session $(or $(SESSION),demo) -user $(or $(USER_ID),dm1) -role $(or $(ROLE),dm)

test:
	go test ./...

race:
	go test -race -count=1 ./...

integration:
	go test -race -count=1 -tags=integration ./...

fuzz:
	go test -run=^$$ -fuzz=FuzzVerify -fuzztime=30s ./internal/auth
	go test -run=^$$ -fuzz=FuzzParseLine -fuzztime=30s ./internal/dice

lint:
	golangci-lint run --build-tags=integration
