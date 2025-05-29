.PHONY: help
help:
	@echo "Available commands:"
	@echo "  make create-db    - Create the MySQL database"
	@echo "  make run          - Run the application"
	@echo "  make build        - Build the application"
	@echo "  make test         - Run tests"
	@echo "  make migrate      - Run database migrations"
	@echo "  make clean        - Clean build artifacts"
	@echo "  make create-admin - Create an admin user"

.PHONY: create-db
create-db:
	mysql -u root < scripts/create_database.sql

.PHONY: run
run:
	go run cmd/main.go

.PHONY: build
build:
	go build -o bin/gocrm cmd/main.go

.PHONY: test
test:
	go test ./...

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
	go build -o bin/.create-admin cmd/create-admin/main.go