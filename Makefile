.PHONY: all build run test clean setup db-init db-reset sqlc

# Default target
all: build

# Build the application
build:
	go build -o portal cmd/server/main.go

# Run the application
run:
	go run cmd/server/main.go

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -f portal
	rm -rf tmp/

# Initial setup
setup:
	cp -n .env.example .env || true
	mkdir -p data
	go mod tidy

# Initialize database
db-init:
	mkdir -p data
	sqlite3 data/portal.db < migrations/001_initial_schema.sql

# Reset database (WARNING: deletes all data)
db-reset:
	rm -f data/portal.db
	$(MAKE) db-init

# Generate sqlc code
sqlc:
	sqlc generate

# Install development tools
tools:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/air-verse/air@latest

# Run with hot reload (requires air)
dev:
	air

# Build for production
prod:
	CGO_ENABLED=1 go build -ldflags="-s -w" -o portal cmd/server/main.go

# Help
help:
	@echo "Available targets:"
	@echo "  make build     - Build the application"
	@echo "  make run       - Run the application"
	@echo "  make test      - Run tests"
	@echo "  make clean     - Clean build artifacts"
	@echo "  make setup     - Initial project setup"
	@echo "  make db-init   - Initialize database"
	@echo "  make db-reset  - Reset database (WARNING: deletes data)"
	@echo "  make sqlc      - Generate SQL code"
	@echo "  make tools     - Install dev tools"
	@echo "  make dev       - Run with hot reload"
	@echo "  make prod      - Build for production"
