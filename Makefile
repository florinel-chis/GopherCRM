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
	@echo "  make verify       - Core CI checks: hygiene, Go build/vet/test -race, frontend build/lint/test, API docs"
	@echo "  make verify-runner - Runner tests, workflow export parity, and database probe tests"
	@echo "  make e2e          - Playwright e2e against a real backend and a freshly reset gocrm_e2e database"
	@echo "                      (make e2e SPECS=\"e2e/tests/admin-leads.spec.ts\" for selected specs)"
	@echo "                      (make e2e E2E_DB_DRIVER=sqlite runs it on a new SQLite file instead)"
	@echo "  make migrate      - Run database migrations"
	@echo "  make clean        - Clean build artifacts"
	@echo "  make create-admin - Create an admin user"
	@echo "  make swagger      - Regenerate api/swagger.json, api/swagger.yaml and the HTML reference api/index.html"
	@echo "  make verify-api-docs - Check that api/ is current and every route is documented (part of make verify)"

.PHONY: create-db
create-db:
	mysql -u root < scripts/create_database.sql

.PHONY: run
run:
	go run ./cmd

.PHONY: build
build:
	go build -o bin/gophercrm ./cmd

.PHONY: test
test:
	go test $(GO_PKGS)

# Core CI jobs call these targets. Browser and runner checks have separate
# targets below. Run make verify before pushing a branch.
.PHONY: verify verify-hygiene verify-backend verify-frontend
verify: verify-hygiene verify-backend verify-frontend verify-api-docs

verify-hygiene:
	scripts/ci/check-file-sizes.sh
	scripts/e2e/selftest.sh
	scripts/deploy/receive-release_test.sh
	python3 -m unittest discover -s scripts/ci/tests -v
	python3 -m unittest discover -s scripts/release/tests -v

verify-backend:
	go build $(GO_BUILD_PKGS)
	go vet $(GO_PKGS)
	go test -race $(GO_PKGS)
	go test -race -tags=gophercrm_e2e ./internal/middleware ./internal/aeo

verify-frontend:
	cd gocrm-ui && npm ci && npm run build && npm run lint && npm test -- --run

# e2e resets the gocrm_e2e database, starts the backend on a free port and runs
# Playwright against it; CI runs the same script. SPECS narrows the run (paths
# relative to gocrm-ui/). E2E_DB_DRIVER=sqlite uses a new SQLite file instead of
# MySQL and leaves gocrm_e2e alone. Needs Go, Node, `npx playwright install
# chromium` and, for the default mysql driver, MySQL.
.PHONY: e2e
e2e:
	E2E_DB_DRIVER="$(E2E_DB_DRIVER)" scripts/e2e/run.sh $(SPECS)

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

.PHONY: swagger api-docs verify-api-docs
# -B gobuildid stamps an LC_UUID load command: swag's module targets an older
# Go, whose linker default omits it, and current macOS refuses to run such a
# binary ("missing LC_UUID load command"). The flag is a no-op elsewhere.
SWAG = go run -ldflags "-B gobuildid" github.com/swaggo/swag/cmd/swag@v1.16.6
API_DOCS_DIR ?= api
# The OpenAPI spec (JSON and YAML) from the swag annotations, then the static
# HTML reference rendered from it. scripts/docs/swag-overrides holds swag's
# global type replacements. verify-api-docs regenerates through the
# same api-docs target into a temporary directory and compares the bytes.
swagger:
	$(MAKE) --no-print-directory api-docs API_DOCS_DIR=api

api-docs:
	$(SWAG) init -g cmd/main.go --output $(API_DOCS_DIR) --outputTypes json,yaml --parseDependency --quiet --overridesFile scripts/docs/swag-overrides
	python3 scripts/docs/render_api_reference.py $(API_DOCS_DIR)/swagger.json $(API_DOCS_DIR)/index.html

# Fails when api/ is stale or the spec is incomplete: routes without an
# operation (and the reverse), missing descriptions, tags or enums. The route
# extractor's own tests run first.
verify-api-docs:
	go test ./scripts/ci/apiroutes
	python3 scripts/ci/check-api-docs.py

# Install automation/langflow[flows] in an isolated Python 3.12 environment first.
RUNNER_PYTHON ?= python3
.PHONY: verify-runner
verify-runner:
	PYTHONPATH=automation/langflow $(RUNNER_PYTHON) -m unittest discover -s automation/langflow/tests -v
	PYTHONPATH=automation/langflow $(RUNNER_PYTHON) -m gophercrm_runner.verify_export
	go test ./automation/langflow/probes/mysql
