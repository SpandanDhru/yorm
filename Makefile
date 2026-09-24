export DATABASE_URL ?= postgres://yorm:yorm@localhost:5432/yorm?sslmode=disable
export YORM_TOKEN_SECRET ?= dev-only-secret-do-not-use-in-prod-0123456789
export YORM_ALLOWED_ORIGINS ?= localhost:5173

.PHONY: db-up db-down run web-install web-dev web-build web-test token test race integration fuzz lint

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

lint:
	golangci-lint run
