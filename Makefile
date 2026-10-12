.PHONY: build run dev test clean docker-up docker-down tidy

# Build the binary
build:
	go build -ldflags="-s -w" -o dynago ./cmd/dynago

# Run locally (requires DATABASE_URL set or .env loaded)
run: build
	./dynago

# Development mode with auto-restart (requires air: go install github.com/air-verse/air@latest)
dev:
	air -c .air.toml 2>/dev/null || go run ./cmd/dynago

# Run tests
test:
	go test ./... -v -count=1

# Clean build artifacts
clean:
	rm -f dynago dynago.exe

# Docker Compose
docker-up:
	docker compose up --build -d

docker-down:
	docker compose down -v

# Tidy dependencies
tidy:
	go mod tidy
