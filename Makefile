VERSION := $(shell cat VERSION)

.PHONY: all build build-all run test clean setup db-reset sqlc prod dev tools hooks image image-push help

# Default target
all: build

# Build the application
build:
	go build -o portal ./cmd/server

# Build all binaries
build-all: build
	go build -o portal-cron ./cmd/cron
	go build -o import ./cmd/import

# Run the application
run:
	go run ./cmd/server

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -f portal portal-cron import
	rm -f *.exe
	rm -rf tmp/

# Initial setup
setup:
	cp -n .env.example .env || true
	mkdir -p data
	go mod tidy

# Reset database (WARNING: deletes all data). There is no db-init: the server
# runs the embedded migrations at startup and creates the file if missing.
db-reset:
	rm -f data/portal.db data/portal.db-journal

# Generate sqlc code
sqlc:
	sqlc generate

# Build the production image locally, tagged the way the host pulls it.
# CI does this on a version tag; this is the manual fallback.
PORTAL_IMAGE ?= ghcr.io/thebys/member-portal
image:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(shell git rev-parse HEAD) \
		--build-arg SOURCE=$(shell git remote get-url origin | sed 's/\.git$$//') \
		-t $(PORTAL_IMAGE):$(VERSION) .

# Requires `docker login ghcr.io` with a PAT that has write:packages.
image-push: image
	docker push $(PORTAL_IMAGE):$(VERSION)

# Enable the repo's git hooks (blocks committing secrets into this public repo)
hooks:
	git config core.hooksPath .githooks
	@echo "hooks enabled: .githooks/pre-commit"

# Install development tools
tools:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/air-verse/air@latest

# Run with hot reload (requires air)
dev:
	air

# Build for production. Matches what the Docker image does: static, pure-Go
# sqlite driver, version stamped from the VERSION file.
prod:
	CGO_ENABLED=0 go build -trimpath -buildvcs=false \
		-ldflags="-s -w -X 'main.BuildDate=$(VERSION) ($(shell date -u '+%Y-%m-%d %H:%M UTC'))'" \
		-o portal ./cmd/server

# Help
help:
	@echo "Available targets:"
	@echo "  make build      - Build the main application"
	@echo "  make build-all  - Build all binaries (server + cron jobs)"
	@echo "  make run        - Run the application"
	@echo "  make test       - Run tests"
	@echo "  make clean      - Clean build artifacts"
	@echo "  make setup      - Initial project setup"
	@echo "  make db-reset   - Delete the database (server re-creates + migrates)"
	@echo "  make sqlc       - Generate SQL code"
	@echo "  make image      - Build the production image"
	@echo "  make image-push - Build and push it to ghcr"
	@echo "  make hooks      - Enable git hooks that block secret commits"
	@echo "  make tools      - Install dev tools"
	@echo "  make dev        - Run with hot reload"
	@echo "  make prod       - Build for production"
