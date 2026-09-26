# The demo database from docker-compose.yml.
DEMO_DSN ?= postgres://schemalens:schemalens@localhost:5433/shop?sslmode=disable

.PHONY: build test test-integration lint db-up db-down db-reset demo

build:
	go build -o bin/schemalens ./cmd/schemalens

test:
	go test ./...

# Runs the tests that need a real Postgres loaded with testdata/sample_schema.sql.
test-integration:
	SCHEMALENS_TEST_DSN="$(DEMO_DSN)" go test ./... -count=1

lint:
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

db-up:
	docker compose up -d --wait

db-down:
	docker compose down

# Drop the data volume so sample_schema.sql is loaded again from scratch.
db-reset:
	docker compose down -v
	docker compose up -d --wait

demo: db-up
	DATABASE_URL="$(DEMO_DSN)" go run ./cmd/schemalens serve --open
