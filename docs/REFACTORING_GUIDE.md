# Phase 2 Architecture Refactoring Guide

## Overview
This document guides the refactoring to remove global state and add context.Context throughout the codebase.

## Completed
✅ Created `internal/database/database.go` - Database service wrapper
✅ Created `internal/logging/logger.go` - Logger service wrapper
✅ **Reference Implementation:** User/Auth domain fully refactored

## Pattern Summary

### 1. Repository Layer Pattern
**Before:**
```go
type UserRepository interface {
    GetByID(id uint) (*models.User, error)
}

func (r *userRepository) GetByID(id uint) (*models.User, error) {
    var user models.User
    err := r.db.First(&user, id).Error
    return &user, err
}
```

**After:**
```go
type UserRepository interface {
    GetByID(ctx context.Context, id uint) (*models.User, error)
}

func (r *userRepository) GetByID(ctx context.Context, id uint) (*models.User, error) {
    var user models.User
    err := r.db.WithContext(ctx).First(&user, id).Error
    return &user, err
}
```

### 2. Service Layer Pattern
**Before:**
```go
type UserService interface {
    GetByID(id uint) (*models.User, error)
}

func (s *userService) GetByID(id uint) (*models.User, error) {
    return s.userRepo.GetByID(id)
}
```

**After:**
```go
type UserService interface {
    GetByID(ctx context.Context, id uint) (*models.User, error)
}

func (s *userService) GetByID(ctx context.Context, id uint) (*models.User, error) {
    return s.userRepo.GetByID(ctx, id)
}
```

### 3. Handler Layer Pattern
**Before:**
```go
func (h *UserHandler) Get(c *gin.Context) {
    id := parseID(c)
    user, err := h.userService.GetByID(id)
    // ...
}
```

**After:**
```go
func (h *UserHandler) Get(c *gin.Context) {
    ctx := c.Request.Context()
    id := parseID(c)
    user, err := h.userService.GetByID(ctx, id)
    // ...
}
```

### 4. Logger Usage Pattern
**Before:**
```go
utils.Logger.Info("message")
logger := utils.LogServiceCall(utils.Logger, "Service", "Method")
```

**After:**
```go
// Inject logger into services/repositories
type userService struct {
    logger   *logging.Logger
    userRepo repository.UserRepository
}

func (s *userService) GetByID(ctx context.Context, id uint) (*models.User, error) {
    s.logger.WithContext(ctx).WithField("user_id", id).Debug("Getting user")
    return s.userRepo.GetByID(ctx, id)
}
```

## Domains to Refactor

Each domain follows the same pattern. Refactor in this order:

### ✅ 1. User/Auth (COMPLETED - Reference Implementation)
- [x] internal/repository/user_repository.go
- [x] internal/service/user_service.go
- [x] internal/service/auth_service.go
- [x] internal/handler/user_handler.go
- [x] internal/handler/auth_handler.go

### 2. Lead (TODO)
- [ ] internal/repository/lead_repository.go
- [ ] internal/service/lead_service.go
- [ ] internal/handler/lead_handler.go

### 3. Customer (TODO)
- [ ] internal/repository/customer_repository.go
- [ ] internal/service/customer_service.go
- [ ] internal/handler/customer_handler.go

### 4. Ticket (TODO)
- [ ] internal/repository/ticket_repository.go
- [ ] internal/service/ticket_service.go
- [ ] internal/handler/ticket_handler.go

### 5. Task (TODO)
- [ ] internal/repository/task_repository.go
- [ ] internal/service/task_service.go
- [ ] internal/handler/task_handler.go

### 6. APIKey (TODO)
- [ ] internal/repository/apikey_repository.go
- [ ] internal/service/apikey_service.go
- [ ] internal/handler/apikey_handler.go

### 7. Configuration (TODO)
- [ ] internal/repository/configuration_repository.go
- [ ] internal/service/configuration_service.go
- [ ] internal/handler/configuration_handler.go

### 8. Dashboard (TODO)
- [ ] internal/service/dashboard_service.go (no repository)
- [ ] internal/handler/dashboard_handler.go

## Transaction Support Pattern

### Add to Repository
```go
// In repository implementation
func (r *userRepository) CreateTx(ctx context.Context, tx *gorm.DB, user *models.User) error {
    return tx.WithContext(ctx).Create(user).Error
}
```

### Add to Service
```go
func (s *leadService) ConvertToCustomer(ctx context.Context, leadID uint, customerData *models.Customer) error {
    return s.db.Transaction(ctx, func(tx *gorm.DB) error {
        lead, err := s.leadRepo.GetByIDTx(ctx, tx, leadID)
        if err != nil {
            return err
        }

        if err := s.customerRepo.CreateTx(ctx, tx, customerData); err != nil {
            return err
        }

        if err := s.leadRepo.ConvertToCustomerTx(ctx, tx, leadID, customerData.ID); err != nil {
            return err
        }

        return nil
    })
}
```

## GORM Preloading Pattern

```go
// Add preload parameter to list operations
func (r *userRepository) List(ctx context.Context, offset, limit int, preload []string) ([]models.User, error) {
    query := r.db.WithContext(ctx)

    for _, rel := range preload {
        query = query.Preload(rel)
    }

    var users []models.User
    err := query.Offset(offset).Limit(limit).Find(&users).Error
    return users, err
}
```

## Testing Pattern

**Before:**
```go
mockRepo := new(MockUserRepository)
service := NewUserService(mockRepo)
user, err := service.GetByID(1)
```

**After:**
```go
mockRepo := new(MockUserRepository)
mockLogger := &logging.Logger{}
service := NewUserService(mockRepo, mockLogger)

ctx := context.Background()
user, err := service.GetByID(ctx, 1)
```

## Migration Checklist Per Domain

For each domain, follow these steps:

1. **Update Repository Interface** (internal/repository/interfaces.go)
   - Add `ctx context.Context` as first parameter to all methods
   - Add transaction variants: `CreateTx`, `UpdateTx`, etc.
   - Add preload options to list methods

2. **Update Repository Implementation**
   - Add context parameter
   - Use `r.db.WithContext(ctx)` for all queries
   - Implement transaction variants
   - Add GORM preloading

3. **Update Service Interface** (internal/service/interfaces.go)
   - Add `ctx context.Context` as first parameter

4. **Update Service Implementation**
   - Add `logger *logging.Logger` to struct
   - Add context parameter
   - Pass context to repository calls
   - Use logger.WithContext(ctx) for logging
   - Use database.Transaction() for multi-step operations

5. **Update Handler**
   - Extract `ctx := c.Request.Context()`
   - Pass context to service calls

6. **Update Tests**
   - Add `ctx := context.Background()`
   - Pass context to all calls
   - Update mocks to expect context

## Benefits After Refactoring

1. **Request Cancellation**: Long-running operations can be cancelled
2. **Timeout Enforcement**: Per-request timeouts can be set
3. **Distributed Tracing**: Request IDs and traces propagate through the stack
4. **No Global State**: Better testability and concurrency safety
5. **Transaction Safety**: Multi-step operations are atomic
6. **Query Optimization**: N+1 queries prevented with preloading

## Estimated Effort

- Per domain (Repository + Service + Handler): ~30-45 minutes
- Tests per domain: ~15-20 minutes
- Total for all 8 domains: ~6-8 hours

## Next Steps

1. Use the User/Auth reference implementation as a template
2. Refactor one domain at a time
3. Run tests after each domain
4. Commit incrementally

## Reference Files

See the completed User/Auth implementation:
- `internal/repository/user_repository_v2.go`
- `internal/service/user_service_v2.go`
- `internal/service/auth_service_v2.go`
- `internal/handler/user_handler_v2.go`
- `internal/handler/auth_handler_v2.go`
