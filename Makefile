# Go packages, excluding sources shipped inside gocrm-ui/node_modules. Recursive
# (=) so go list only runs for targets that use them. The build list keeps
# packages with non-test sources: go build rejects a test-only package when it
# is named explicitly.
GO_PKGS = $(shell go list ./... | grep -v /gocrm-ui/)
GO_BUILD_PKGS = $(shell go list -f '{{if .GoFiles}}{{.ImportPath}}{{end}}' ./... | grep -v /gocrm-ui/)

.PHONY: help
help:
	@echo "Available commands:"
	@echo "  make create-db    - Create the MySQL database"
	@echo "  make run          - Run the application"
	@echo "  make build        - Build the application"
	@echo "  make test         - Run Go tests"
	@echo "  make verify       - Everything CI runs except e2e: size check, Go build/vet/test -race, frontend build/lint/test"
	@echo "  make e2e          - Playwright e2e against a real backend and a freshly reset gocrm_e2e database"
	@echo "                      (make e2e SPECS=\"e2e/tests/admin-leads.spec.ts\" for selected specs)"
	@echo "  make migrate      - Run database migrations"
	@echo "  make clean        - Clean build artifacts"
	@echo "  make create-admin - Create an admin user"
	@echo "  make swagger      - Regenerate api/swagger.json and api/swagger.yaml"

.PHONY: create-db
create-db:
	mysql -u root < scripts/create_database.sql

.PHONY: run
run:
	go run cmd/main.go

.PHONY: build
build:
	go build -o bin/gophercrm cmd/main.go

.PHONY: test
test:
	go test $(GO_PKGS)

# verify runs exactly what CI runs; CI calls these targets, so the two cannot
# drift apart. Run it before pushing a branch.
.PHONY: verify verify-hygiene verify-backend verify-frontend
verify: verify-hygiene verify-backend verify-frontend

verify-hygiene:
	scripts/ci/check-file-sizes.sh
	scripts/e2e/selftest.sh
	scripts/deploy/receive-release_test.sh

verify-backend:
	go build $(GO_BUILD_PKGS)
	go vet $(GO_PKGS)
	go test -race $(GO_PKGS)

verify-frontend:
	cd gocrm-ui && npm ci && npm run build && npm run lint && npm test -- --run

# e2e resets the gocrm_e2e database, starts the backend on a free port and runs
# Playwright against it; CI runs the same script. SPECS narrows the run (paths
# relative to gocrm-ui/). Needs MySQL, Go, Node and `npx playwright install chromium`.
.PHONY: e2e
e2e:
	scripts/e2e/run.sh $(SPECS)

.PHONY: migrate
migrate: run

.PHONY: clean
clean:
	rm -rf bin/

.PHONY: deps
deps:
	go mod download
	go mod tidy

.PHONY: create-admin
create-admin:
	@bin/create-admin

.PHONY: build-tools
build-tools:
	go build -o bin/create-admin cmd/create-admin/main.go

.PHONY: swagger
swagger:
	go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/main.go --output api --outputTypes json,yaml --parseDependency