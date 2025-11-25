.PHONY: all build run test clean setup db-init db-reset sqlc

# Default target
all: build

# Build the application
build:
	go build -o portal cmd/server/main.go

# Build all binaries
build-all: build
	go build -o sync_fio_payments cmd/cron/sync_fio_payments.go
	go build -o update_debt_status cmd/cron/update_debt_status.go
	go build -o import cmd/import/main.go

# Run the application
run:
	go run cmd/server/main.go

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -f portal sync_fio_payments update_debt_status import
	rm -f *.exe
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
	@echo "  make build      - Build the main application"
	@echo "  make build-all  - Build all binaries (server + cron jobs)"
	@echo "  make run        - Run the application"
	@echo "  make test       - Run tests"
	@echo "  make clean      - Clean build artifacts"
	@echo "  make setup      - Initial project setup"
	@echo "  make db-init    - Initialize database"
	@echo "  make db-reset   - Reset database (WARNING: deletes data)"
	@echo "  make sqlc       - Generate SQL code"
	@echo "  make tools      - Install dev tools"
	@echo "  make dev        - Run with hot reload"
	@echo "  make prod       - Build for production"
