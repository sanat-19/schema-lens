.PHONY: run backend frontend build test test-integration lint

# Starts the backend and the frontend together; Ctrl+C stops both. The
# browser opens on the Connect page.
run:
	$(MAKE) -j2 backend frontend

# The API on 127.0.0.1:8080.
backend:
	go run ./backend

# The page on http://localhost:5173, forwarding /api to the backend.
frontend: frontend/node_modules
	cd frontend && npm run dev

frontend/node_modules: frontend/package.json frontend/package-lock.json
	cd frontend && npm ci
	@touch $@

# bin/schemalens (the API) and frontend/dist (the page).
build: frontend/node_modules
	go build -o bin/schemalens ./backend
	cd frontend && npm run build

test:
	go test ./...

# Runs the tests that need a real Postgres. Point SCHEMALENS_TEST_DSN at an
# empty database loaded with testdata/sample_schema.sql.
test-integration:
	@test -n "$$SCHEMALENS_TEST_DSN" || { echo "Set SCHEMALENS_TEST_DSN to a Postgres loaded with testdata/sample_schema.sql"; exit 1; }
	go test ./... -count=1

lint:
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@2025.1.1 ./...
