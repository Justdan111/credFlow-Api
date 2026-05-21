# CredFlow API — developer commands.
# Run `make help` to see what's available.

.PHONY: help run build test test-integration test-all lint vet tidy db-up db-down db-reset migrate

# Default target = help. Running `make` with no args shows the menu.
help:
	@echo "CredFlow API — developer commands"
	@echo ""
	@echo "  make run               Start the server (go run ./cmd/server)"
	@echo "  make build             Compile the binary into ./bin/credflow"
	@echo "  make test              Run unit tests only (fast, no DB)"
	@echo "  make test-integration  Run integration tests (needs Postgres)"
	@echo "  make test-all          Run every test"
	@echo "  make lint              go vet ./..."
	@echo "  make vet               Alias for lint"
	@echo "  make tidy              go mod tidy"
	@echo ""
	@echo "  make db-up             Start the Postgres container"
	@echo "  make db-down           Stop the Postgres container"
	@echo "  make db-reset          Drop + recreate the credflow_test database"

run:
	go run ./cmd/server

build:
	mkdir -p bin
	go build -o bin/credflow ./cmd/server

test:
	go test ./...

test-integration:
	go test -tags=integration -count=1 ./test/integration/...

test-all:
	go test -tags=integration -count=1 ./...

lint vet:
	go vet ./...

tidy:
	go mod tidy

db-up:
	docker start credflow-pg || docker run -d \
		--name credflow-pg \
		--restart unless-stopped \
		-e POSTGRES_USER=credflow \
		-e POSTGRES_PASSWORD=credflow_dev \
		-e POSTGRES_DB=credflow \
		-p 127.0.0.1:5432:5432 \
		-v credflow-pg-data:/var/lib/postgresql/data \
		--health-cmd="pg_isready -U credflow -d credflow" \
		--health-interval=2s --health-timeout=2s --health-retries=10 \
		postgres:16

db-down:
	docker stop credflow-pg

# Drop + recreate the test database. Safe to run anytime; the dev database
# is untouched.
db-reset:
	PGPASSWORD=credflow_dev psql -h localhost -U credflow -d credflow \
		-c "DROP DATABASE IF EXISTS credflow_test;" \
		-c "CREATE DATABASE credflow_test OWNER credflow;"
