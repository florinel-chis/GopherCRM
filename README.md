# GoCRM

A Customer Relationship Management (CRM) system built with Go, Gin, and GORM.

## Features

- User authentication with JWT and API Keys
- Lead management with conversion to customers
- Customer management
- Ticket system for support
- Task management
- Role-based access control (Admin, Sales, Support, Customer)
- RESTful API with clean architecture

## Prerequisites

- Go 1.23 or higher
- MySQL 8.0 or higher
- Make (optional, for using Makefile commands)

## Setup

1. Clone the repository:
```bash
git clone https://github.com/florinel-chis/gocrm.git
cd gocrm
```

2. Create the database:
```bash
make create-db
# or manually:
mysql -u root < scripts/create_database.sql
```

3. Configure the environment:
```bash
cp .env.example .env
# Edit .env if you need to change database credentials
```

4. Install dependencies:
```bash
go mod download
```

5. Run the application:
```bash
make run
# or
go run cmd/main.go
```

The server will start on http://localhost:8080

## API Endpoints

### Authentication
- `POST /api/v1/auth/register` - Register a new user
- `POST /api/v1/auth/login` - Login

### Users
- `GET /api/v1/users` - List users (Admin only)
- `GET /api/v1/users/:id` - Get user by ID
- `PUT /api/v1/users/:id` - Update user
- `DELETE /api/v1/users/:id` - Delete user (Admin only)
- `GET /api/v1/users/me` - Get current user
- `PUT /api/v1/users/me` - Update current user

### Other endpoints for Leads, Customers, Tickets, Tasks, and API Keys follow similar patterns.

## Development

### Building
```bash
make build
```

### Running tests
```bash
make test
```

### Project Structure
```
├── cmd/
│   └── main.go              # Application entry point
├── internal/
│   ├── config/              # Configuration management
│   ├── models/              # Domain models
│   ├── repository/          # Data access layer
│   ├── service/             # Business logic
│   ├── handler/             # HTTP handlers
│   ├── middleware/          # HTTP middleware
│   └── utils/               # Utilities
├── migrations/              # Database migrations
├── tests/                   # Integration tests
└── scripts/                 # Utility scripts
```

## License

MIT License