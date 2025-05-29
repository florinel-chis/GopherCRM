# GoCRM Task List

## Task Overview

| Entity / Area | Task Category | Key Tasks | Priority | Done |
|---|---|---|---|---|
| **General Setup** | Coding | Initialize Go module; install Gin, GORM/sqlx, JWT, logging, testify; set up project structure (models, repo, service, handler, middleware); config management via env; database connection & migrations; main.go router & middleware; enforce clean architecture (DI, interfaces) | High | ☑ |
| **Authentication & Authorization** | Coding | Implement login endpoint & JWT generation; JWT auth middleware; design & implement hashed API-Key model; API-Key creation endpoint; API-Key auth middleware; route protection & role checks | High | ☑ |
| **Authentication & Authorization** | Unit Tests | Unit tests for JWT creation/parsing; tests for API-Key generation & hashing; tests for auth middleware (valid/invalid tokens & keys) | Medium | ☑ |
| **Logging & Observability** | Coding | Set up structured JSON logging (e.g. Logrus/zap); custom Gin middleware for request logging (method, path, status, latency); request-ID middleware; application-level logs in handlers/services; optional metrics/tracing integration | High | ☑ |
| **Logging & Observability** | Unit Tests | Tests for logger & request-ID middleware; ensure logs include required fields; verify no sensitive data is logged | Medium | ☑ |
| **Error Handling & Validation** | Coding | Define unified API response envelope; implement error-handling middleware; configure Gin recovery to JSON; set up input validation with Gin binding & tags; enforce consistent HTTP status codes & error formats | High | ☑ |
| **Error Handling & Validation** | Integration Tests | End-to-end tests for validation errors (400), authentication/authorization (401/403), not found (404) & internal errors (500); verify consistent error response format | Medium | ☑ |
| **User** | Coding | Define User model & migrations; implement UserRepository & GORM/sqlx impl; implement UserService (Register, Authenticate, CRUD); build Gin handlers & routes (CRUD + login); add request validation; enforce role-based checks; add logging | High | ☑ |
| **User** | Unit Tests | Service tests for RegisterUser & AuthenticateUser (success, duplicate email, wrong password); handler tests for Create/Get/Update/Delete & Login endpoints | Medium | ☑ |
| **User** | Integration Tests | End-to-end tests for registration, login, protected routes, user CRUD & permission enforcement | Medium | ☑ |
| **Lead** | Coding | Define Lead model & migrations; implement LeadRepository & service (CRUD + ConvertLead); build handlers & routes; enforce sales/admin role & owner checks; add logging | Medium | ☑ |
| **Lead** | Unit Tests | Service tests for CreateLead, UpdateLead, ConvertLead & owner-filtering; handler tests for all lead endpoints (success & error cases) | Low | ☑ |
| **Lead** | Integration Tests | End-to-end tests for lead creation, listing, retrieval, update, deletion & conversion workflows, with permission checks | Low | ☑ |
| **Customer** | Coding | Define Customer model & migrations; implement CustomerRepository & service (CRUD); build handlers & routes; enforce role-based access; add logging | Medium | ☑ |
| **Customer** | Unit Tests | Service tests for CreateCustomer (success, duplicate); handler tests for create, list, get, update & delete | Low | ☑ |
| **Customer** | Integration Tests | End-to-end tests for customer CRUD, duplicate-email handling & permission enforcement | Low | ☑ |
| **Ticket** | Coding | Define Ticket model & migrations; implement TicketRepository & service (Open, List, Update, Delete); build handlers & routes; enforce customer vs support/admin roles; add logging | Medium | ☑ |
| **Ticket** | Unit Tests | Service tests for OpenTicket, filtering, update rules & deletion; handler tests for ticket endpoints under various roles | Low | ☑ |
| **Ticket** | Integration Tests | End-to-end tests for ticket lifecycle (create, list, get, update, delete), routing & permission rules | Low | ☑ |
| **Task** | Coding | Define Task model & migrations; implement TaskRepository & service (CRUD); build handlers & routes; enforce owner/admin access; add logging | Medium | ☑ |
| **Task** | Unit Tests | Service tests for CreateTask, ListTasks, UpdateTask & DeleteTask; handler tests for task endpoints & authorization | Low | ☑ |
| **Task** | Integration Tests | End-to-end tests for task workflows: create, list, retrieve, update & delete with correct permissions | Low | ☑ |
| **API Key** | Coding | Define APIKey model & migrations; implement APIKeyRepository & service (GenerateAPIKey, Revoke, List); build handlers & routes for key management; integrate API-Key auth middleware; ensure hashing & no plaintext storage; add logging | Medium | ☑ |
| **API Key** | Unit Tests | Service tests for GenerateAPIKey & Revoke; handler tests for API-Key creation, listing & revocation endpoints | Low | ☑ |
| **API Key** | Integration Tests | End-to-end tests for API-Key creation & metadata listing; using API-Key for auth; revocation & post-revoke access blocking; permission checks for key management | Low | ☑ |

## Progress Tracking

### High Priority Tasks (5 total)
- ☑ General Setup - Coding
- ☑ Authentication & Authorization - Coding  
- ☑ Logging & Observability - Coding
- ☑ Error Handling & Validation - Coding
- ☑ User - Coding

### Medium Priority Tasks (10 total)
- ☑ Authentication & Authorization - Unit Tests
- ☑ Logging & Observability - Unit Tests
- ☑ Error Handling & Validation - Integration Tests
- ☑ User - Unit Tests
- ☑ User - Integration Tests
- ☑ Lead - Coding
- ☑ Customer - Coding
- ☑ Ticket - Coding
- ☑ Task - Coding
- ☑ API Key - Coding

### Low Priority Tasks (10 total)
- ☑ Lead - Unit Tests
- ☑ Lead - Integration Tests
- ☑ Customer - Unit Tests
- ☑ Customer - Integration Tests
- ☑ Ticket - Unit Tests
- ☑ Ticket - Integration Tests
- ☑ Task - Unit Tests
- ☑ Task - Integration Tests
- ☑ API Key - Unit Tests
- ☑ API Key - Integration Tests

## Notes
- Check off tasks using `[x]` or `☑` as they are completed
- Tasks should generally be completed in priority order (High → Medium → Low)
- Within each priority level, complete coding tasks before their corresponding tests