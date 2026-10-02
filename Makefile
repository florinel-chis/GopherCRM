# Go packages, excluding sources shipped inside gocrm-ui/node_modules. Recursive
# (=) so go list only runs for targets that use them. The build list keeps
# packages with non-test sources: go build rejects a test-only package when it
# is named explicitly.
GO_PKGS = $(shell go list ./... | grep -v /gocrm-ui/)
GO_BUILD_PKGS = $(shell go list -f '{{if .GoFiles}}{{.ImportPath}}{{end}}' ./... | grep -v /gocrm-ui/)

# The full gates (verify, verify-backend, verify-frontend, e2e) run one at a
# time per machine, across terminals and sessions, through scripts/gate-lock.sh
# (see there for GATE_LOCK_DIR, GATE_LOCK_TIMEOUT and the rest). CI sets
# CI=true, which the script treats as GATE_LOCK=0: every job has a machine of
# its own. A dry run (make -n) never takes the lock.
GATE = scripts/gate-lock.sh

# VERIFY_JOBS=N bounds the parallelism of the gates: Go gets -p=N through
# GOFLAGS (appended to whatever GOFLAGS already says) and Vitest --maxWorkers=N.
# Unset, the command lines below are byte for byte what CI runs.
GO = go
VITEST_RUN_FLAGS = --run
ifneq ($(VERIFY_JOBS),)
ifneq ($(shell printf '%s' '$(VERIFY_JOBS)' | grep -cE '^[1-9][0-9]*$$'),1)
$(error VERIFY_JOBS must be a positive integer, got '$(VERIFY_JOBS)')
endif
GO = GOFLAGS="$(strip $(shell go env GOFLAGS) -p=$(VERIFY_JOBS))" go
VITEST_RUN_FLAGS = --run --maxWorkers=$(VERIFY_JOBS)
endif

.PHONY: help
help:
	@echo "Available commands:"
	@echo "  make create-db    - Create the MySQL database"
	@echo "  make run          - Run the application"
	@echo "  make build        - Build the application"
	@echo "  make test         - Run Go tests"
	@echo "  make verify       - Everything CI runs except e2e: size check, Go build/vet/test -race, frontend build/lint/test"
	@echo "                      (make verify-backend / verify-frontend for one half; verify-hygiene for the checks alone)"
	@echo "  make e2e          - Playwright e2e against a real backend and a freshly reset gocrm_e2e database"
	@echo "                      (make e2e SPECS=\"e2e/tests/admin-leads.spec.ts\" for selected specs)"
	@echo "                      (make e2e E2E_DB_DRIVER=sqlite runs it on a new SQLite file instead)"
	@echo "  make check-touched- Build, vet and test -race only the Go packages changed since origin/main, and the"
	@echo "                      Vitest files related to changed frontend sources; the quick check for a branch in progress"
	@echo "  Gates serialise:    verify, verify-backend, verify-frontend and e2e take a machine-wide lock"
	@echo "                      (scripts/gate-lock.sh; waits up to GATE_LOCK_TIMEOUT=1800 s; GATE_LOCK=0 skips it, CI does)"
	@echo "  VERIFY_JOBS=N       caps parallelism in the gates and check-touched: go -p=N, Vitest --maxWorkers=N"
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
#
# verify, verify-backend and verify-frontend take the gate lock and then run
# their steps in a sub-make, so that one lock covers every line of the target.
# Under `make verify` the lock is already held (GATE_LOCK_HELD), and the two
# verify-* targets run straight through. The *-steps targets are the commands
# themselves; call them directly only when nothing else may be running.
.PHONY: verify verify-hygiene verify-backend verify-frontend verify-backend-steps verify-frontend-steps
verify:
	@$(GATE) verify $(MAKE) verify-hygiene verify-backend verify-frontend

verify-hygiene:
	scripts/ci/check-file-sizes.sh
	scripts/ci/makefile_test.sh
	scripts/gate-lock_test.sh
	scripts/check-touched_test.sh
	scripts/e2e/selftest.sh
	scripts/deploy/receive-release_test.sh

verify-backend:
	@$(GATE) verify-backend $(MAKE) verify-backend-steps

verify-backend-steps:
	$(GO) build $(GO_BUILD_PKGS)
	$(GO) vet $(GO_PKGS)
	$(GO) test -race $(GO_PKGS)

verify-frontend:
	@$(GATE) verify-frontend $(MAKE) verify-frontend-steps

verify-frontend-steps:
	cd gocrm-ui && npm ci && npm run build && npm run lint && npm test -- $(VITEST_RUN_FLAGS)

# check-touched is the quick check for a branch in progress: it builds, vets
# and tests (-race) only the Go packages changed since the merge base with
# origin/main, committed or not, and runs the Vitest files related to changed
# sources under gocrm-ui/src. It takes no lock; the full gates cover the rest.
.PHONY: check-touched
check-touched:
	scripts/check-touched.sh

# e2e resets the gocrm_e2e database, starts the backend on a free port and runs
# Playwright against it; CI runs the same script. SPECS narrows the run (paths
# relative to gocrm-ui/). E2E_DB_DRIVER=sqlite uses a new SQLite file instead of
# MySQL and leaves gocrm_e2e alone. Needs Go, Node, `npx playwright install
# chromium` and, for the default mysql driver, MySQL.
.PHONY: e2e
e2e:
	@$(GATE) e2e env E2E_DB_DRIVER="$(E2E_DB_DRIVER)" scripts/e2e/run.sh $(SPECS)

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
# -B gobuildid stamps an LC_UUID load command: swag's module targets an older
# Go, whose linker default omits it, and current macOS refuses to run such a
# binary ("missing LC_UUID load command"). The flag is a no-op elsewhere.
swagger:
	go run -ldflags "-B gobuildid" github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/main.go --output api --outputTypes json,yaml --parseDependency