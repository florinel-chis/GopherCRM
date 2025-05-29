# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

GopherCRM is a Customer Relationship Management system built in Go. The project is currently in initial development phase with a comprehensive task list defined in `tasks.md`.

## Architecture

The project follows clean architecture principles with the following layers:
- **Models**: Domain entities (User, Lead, Customer, Ticket, Task, APIKey)
- **Repository**: Data access layer with interfaces
- **Service**: Business logic layer
- **Handler**: HTTP handlers using Gin framework
- **Middleware**: Authentication (JWT & API Key), logging, error handling

## Key Technologies

- **Web Framework**: Gin
- **Database**: MySQL
- **ORM**: GORM
- **Authentication**: JWT tokens and API Keys
- **Logging**: Structured JSON logging (Logrus)
- **Testing**: testify

## Development Commands

Since the project is not yet initialized, the first steps are:

```bash
# Create MySQL database
make create-db
# or
mysql -u root < scripts/create_database.sql

# Initialize Go module
go mod init github.com/florinel-chis/gophercrm

# Install dependencies
go get -u github.com/gin-gonic/gin
go get -u gorm.io/gorm
go get -u gorm.io/driver/mysql
go get -u github.com/golang-jwt/jwt/v5
go get -u github.com/sirupsen/logrus
go get -u github.com/stretchr/testify
go get -u github.com/joho/godotenv
go get -u github.com/google/uuid
go get -u golang.org/x/crypto/bcrypt

# Run tests (once implemented)
go test ./...

# Run specific test
go test -run TestName ./path/to/package

# Build the application
go build -o gophercrm cmd/main.go

# Run the application
./gophercrm
```

## Project Structure (Planned)

```
├── cmd/
│   └── main.go              # Application entry point
├── internal/
│   ├── config/              # Configuration management
│   ├── models/              # Domain models
│   ├── repository/          # Data access interfaces and implementations
│   ├── service/             # Business logic
│   ├── handler/             # HTTP handlers
│   ├── middleware/          # Auth, logging, error handling
│   └── utils/               # Utility functions
├── migrations/              # Database migrations
├── tests/                   # Integration tests
└── .env.example            # Environment variables template
```

## Implementation Priorities

Refer to `tasks.md` for the complete task list. High priority items that should be completed first:
1. General Setup - Project structure and dependencies
2. Authentication & Authorization - JWT and API Key implementation
3. Logging & Observability - Structured logging setup
4. Error Handling & Validation - Unified response format
5. User Entity - Core user management functionality

## Database Schema Considerations

Key relationships to implement:
- Users can have multiple Leads, Customers, Tasks
- Leads can be converted to Customers
- Customers can have multiple Tickets
- Tickets have status tracking and assignment
- API Keys belong to Users with revocation capability

## API Design Patterns

- RESTful endpoints with consistent naming
- Unified response envelope for all endpoints
- Role-based access control (admin, sales, support, customer)
- Owner-based filtering for user-specific resources
- Consistent error codes and messages