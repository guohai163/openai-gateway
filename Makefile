.PHONY: build test integration dev
build:
	cd web && npm ci && npm run build
	go build -o bin/gyai-gateway ./cmd/gateway

test:
	go test -race ./...
	go vet ./...
	cd web && npm test && npm run build

integration:
	@test -n "$$TEST_DATABASE_URL" || (echo 'Set TEST_DATABASE_URL to an isolated PostgreSQL database'; exit 1)
	go test -race -count=1 ./internal/gateway/...

dev:
	go run ./cmd/gateway
