# 🐻 FinaBBear Developer Guide

Complete guide for developers working on the FinaBBear Personal Finance Management API.

---

## 📚 Table of Contents

1. [Project Overview](#project-overview)
2. [Project Structure](#project-structure)
3. [Getting Started](#getting-started)
4. [Running Locally](#running-locally)
5. [Database Connection](#database-connection)
6. [Creating New Modules](#creating-new-modules)
7. [Folder Structure Explained](#folder-structure-explained)
8. [Development Workflow](#development-workflow)
9. [Testing](#testing)
10. [Common Tasks](#common-tasks)
11. [Best Practices](#best-practices)
12. [Troubleshooting](#troubleshooting)

---

## 🎯 Project Overview

**FinaBBear** is a RESTful API backend for personal finance management built with:
- **Language:** Go 1.25.3+
- **Framework:** Gin (HTTP web framework)
- **Database:** PostgreSQL 18
- **ORM/Driver:** pgx/v5 (PostgreSQL driver)
- **Authentication:** JWT (JSON Web Tokens)

### Key Features
- User authentication and authorization
- Account management (bank, cash, credit cards, etc.)
- Transaction tracking (income, expense, transfer)
- Budget management
- Bill splitting with friends
- Loan tracking
- Recurring transactions

---

## 📁 Project Structure

```
finna-bbear-be/
│
├── cmd/
│   └── api/
│       └── main.go              # Application entry point
│
├── internal/                    # Private application code
│   ├── auth/                    # Authentication module
│   │   ├── auth.go             # Auth service
│   │   ├── password.go         # Password hashing
│   │   ├── token.go            # JWT token handling
│   │   └── auth_test.go        # Auth tests
│   │
│   ├── config/                  # Configuration management
│   │   └── config.go           # Environment config loader
│   │
│   ├── logger/                  # Logging utilities
│   │   └── logger.go           # Structured logging setup
│   │
│   ├── models/                  # Data models
│   │   └── user.go             # User model
│   │
│   ├── response/                # HTTP response helpers
│   │   └── response.go         # Standard response formats
│   │
│   ├── server/                  # HTTP server setup
│   │   └── (handlers, routes, middleware)
│   │
│   └── store/                   # Database layer
│       └── store.go            # Database operations
│
├── migrations/                  # Database migrations
│   ├── 000001_init_schema.up.sql    # Migration up
│   └── 000001_init_schema.down.sql  # Migration down
│
├── .env                         # Local environment variables (NOT in git)
├── env.example                  # Environment template
├── .gitignore                   # Git ignore rules
├── go.mod                       # Go dependencies
├── go.sum                       # Dependency checksums
├── Makefile                     # Build commands (Linux/Mac)
├── migrate.bat                  # Migration script (Windows)
├── run.bat                      # Run script (Windows)
└── README.md                    # Project documentation
```

---

## 🚀 Getting Started

### Prerequisites

1. **Go 1.25.3+**
   ```bash
   go version
   ```

2. **PostgreSQL 18**
   ```bash
   psql --version
   ```

3. **golang-migrate CLI**
   ```bash
   go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
   ```

4. **Git**
   ```bash
   git --version
   ```

### Initial Setup

#### 1. Clone the Repository
```bash
git clone https://github.com/ppChub722/finna-bbear-be.git
cd finna-bbear-be
```

#### 2. Install Dependencies
```bash
go mod download
```

#### 3. Set Up Environment Variables

**Windows:**
```cmd
setup-env.bat
```

**Linux/Mac:**
```bash
./setup-env.sh
```

Then edit `.env` file:
```env
# Database
DB_HOST=localhost
DB_PORT=5432
DB_USER=chubadmin
DB_PASSWORD=admin1234
DB_NAME=finna_bbear_db

# JWT
JWT_SECRET=change-this-to-a-secure-random-key

# App
APP_PORT=8080
APP_ENV=development
```

#### 4. Set Up PostgreSQL Database

**Create Database:**
```sql
CREATE DATABASE finna_bbear_db;
CREATE USER chubadmin WITH PASSWORD 'admin1234';
GRANT ALL PRIVILEGES ON DATABASE finna_bbear_db TO chubadmin;

-- Connect to database
\c finna_bbear_db

-- Grant schema permissions
GRANT ALL ON SCHEMA public TO chubadmin;
```

#### 5. Run Database Migrations

**Windows:**
```cmd
migrate.bat up
```

**Linux/Mac:**
```bash
make migrate-up
```

#### 6. Verify Setup
```bash
go run cmd/api/main.go
```

Visit: `http://localhost:8080/ping`

---

## 🏃 Running Locally

### Method 1: Using Scripts (Recommended)

**Windows:**
```cmd
run.bat
```

**Linux/Mac:**
```bash
make run
```

### Method 2: Direct Go Command
```bash
go run cmd/api/main.go
```

### Method 3: Build and Run
```bash
# Build
go build -o bin/finna-bbear cmd/api/main.go

# Run
./bin/finna-bbear
```

### Expected Output
```
🐻 FinaBBear is waking up...
✅ Configuration loaded (Environment: development)
✅ Database connected successfully! (localhost:5432/finna_bbear_db)
🚀 Server is running on http://localhost:8080
📚 API Documentation: http://localhost:8080/
```

---

## 🗄️ Database Connection

### Connection Configuration

Database connection is managed through environment variables in `.env`:

```env
DB_HOST=localhost
DB_PORT=5432
DB_USER=chubadmin
DB_PASSWORD=admin1234
DB_NAME=finna_bbear_db
DB_SSLMODE=disable
```

### Connection String Format
```
postgres://{user}:{password}@{host}:{port}/{database}?sslmode={mode}
```

### Check Database Connection

#### Method 1: Using psql
```bash
psql -U chubadmin -d finna_bbear_db -h localhost
```

#### Method 2: Check from Application
The application automatically tests the connection on startup. Look for:
```
✅ Database connected successfully!
```

#### Method 3: Test Connection Script

Create a test file `test-db.go`:
```go
package main

import (
    "context"
    "fmt"
    "log"
    
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/ppChub722/finna-bbear-be/internal/config"
)

func main() {
    cfg, _ := config.Load()
    
    dbpool, err := pgxpool.New(context.Background(), cfg.GetDatabaseURL())
    if err != nil {
        log.Fatal("Connection failed:", err)
    }
    defer dbpool.Close()
    
    if err := dbpool.Ping(context.Background()); err != nil {
        log.Fatal("Ping failed:", err)
    }
    
    fmt.Println("✅ Database connection successful!")
    
    // Count tables
    var count int
    query := `SELECT COUNT(*) FROM information_schema.tables 
              WHERE table_schema = 'public' AND table_name != 'schema_migrations'`
    dbpool.QueryRow(context.Background(), query).Scan(&count)
    fmt.Printf("📊 Tables found: %d\n", count)
}
```

Run: `go run test-db.go`

### Common Database Operations

#### View All Tables
```sql
\dt
```

#### View Table Structure
```sql
\d users
```

#### Check Migration Status
```cmd
migrate.bat status
```

---

## 🆕 Creating New Modules

Follow this step-by-step guide to create a new module (e.g., "Accounts" module).

### Step 1: Create the Model

**File:** `internal/models/account.go`
```go
package models

import "time"

type Account struct {
    ID             int64     `json:"id"`
    UserID         int64     `json:"user_id" binding:"required"`
    Name           string    `json:"name" binding:"required"`
    Type           string    `json:"type" binding:"required"`
    InitialBalance float64   `json:"initial_balance"`
    CreatedAt      time.Time `json:"created_at"`
}
```

### Step 2: Create Store Methods

**File:** `internal/store/account_store.go`
```go
package store

import (
    "context"
    "github.com/ppChub722/finna-bbear-be/internal/models"
)

// CreateAccount creates a new account in the database
func (s *Store) CreateAccount(ctx context.Context, account *models.Account) error {
    query := `
        INSERT INTO accounts (user_id, name, type, initial_balance) 
        VALUES ($1, $2, $3, $4) 
        RETURNING id, created_at`
    
    return s.DB.QueryRow(ctx, query, 
        account.UserID, 
        account.Name, 
        account.Type, 
        account.InitialBalance,
    ).Scan(&account.ID, &account.CreatedAt)
}

// GetAccountByID retrieves an account by ID
func (s *Store) GetAccountByID(ctx context.Context, id int64) (*models.Account, error) {
    query := `
        SELECT id, user_id, name, type, initial_balance, created_at 
        FROM accounts 
        WHERE id = $1`
    
    account := &models.Account{}
    err := s.DB.QueryRow(ctx, query, id).Scan(
        &account.ID,
        &account.UserID,
        &account.Name,
        &account.Type,
        &account.InitialBalance,
        &account.CreatedAt,
    )
    
    if err != nil {
        return nil, err
    }
    
    return account, nil
}

// GetAccountsByUserID retrieves all accounts for a user
func (s *Store) GetAccountsByUserID(ctx context.Context, userID int64) ([]*models.Account, error) {
    query := `
        SELECT id, user_id, name, type, initial_balance, created_at 
        FROM accounts 
        WHERE user_id = $1 
        ORDER BY created_at DESC`
    
    rows, err := s.DB.Query(ctx, query, userID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    
    accounts := []*models.Account{}
    for rows.Next() {
        account := &models.Account{}
        err := rows.Scan(
            &account.ID,
            &account.UserID,
            &account.Name,
            &account.Type,
            &account.InitialBalance,
            &account.CreatedAt,
        )
        if err != nil {
            return nil, err
        }
        accounts = append(accounts, account)
    }
    
    return accounts, nil
}

// UpdateAccount updates an account
func (s *Store) UpdateAccount(ctx context.Context, account *models.Account) error {
    query := `
        UPDATE accounts 
        SET name = $1, type = $2 
        WHERE id = $3 AND user_id = $4`
    
    _, err := s.DB.Exec(ctx, query, account.Name, account.Type, account.ID, account.UserID)
    return err
}

// DeleteAccount deletes an account
func (s *Store) DeleteAccount(ctx context.Context, id, userID int64) error {
    query := `DELETE FROM accounts WHERE id = $1 AND user_id = $2`
    _, err := s.DB.Exec(ctx, query, id, userID)
    return err
}
```

### Step 3: Create Handler

**File:** `internal/server/handlers/account_handler.go`
```go
package handlers

import (
    "net/http"
    "strconv"
    
    "github.com/gin-gonic/gin"
    "github.com/ppChub722/finna-bbear-be/internal/models"
    "github.com/ppChub722/finna-bbear-be/internal/response"
    "github.com/ppChub722/finna-bbear-be/internal/store"
)

type AccountHandler struct {
    store *store.Store
}

func NewAccountHandler(store *store.Store) *AccountHandler {
    return &AccountHandler{store: store}
}

// CreateAccount godoc
// @Summary Create a new account
// @Tags accounts
// @Accept json
// @Produce json
// @Param account body models.Account true "Account data"
// @Success 201 {object} response.GeneralResponse
// @Router /accounts [post]
func (h *AccountHandler) CreateAccount(c *gin.Context) {
    var account models.Account
    
    if err := c.ShouldBindJSON(&account); err != nil {
        response.BadRequest(c, "INVALID_INPUT", "Invalid request data", err.Error())
        return
    }
    
    // Get user ID from JWT context (set by auth middleware)
    userID, _ := c.Get("user_id")
    account.UserID = userID.(int64)
    
    if err := h.store.CreateAccount(c.Request.Context(), &account); err != nil {
        response.InternalError(c, "Failed to create account", err.Error())
        return
    }
    
    response.Created(c, "Account created successfully", account)
}

// GetAccounts godoc
// @Summary Get all accounts for current user
// @Tags accounts
// @Produce json
// @Success 200 {object} response.GeneralResponse
// @Router /accounts [get]
func (h *AccountHandler) GetAccounts(c *gin.Context) {
    userID, _ := c.Get("user_id")
    
    accounts, err := h.store.GetAccountsByUserID(c.Request.Context(), userID.(int64))
    if err != nil {
        response.InternalError(c, "Failed to fetch accounts", err.Error())
        return
    }
    
    response.OK(c, "Accounts retrieved successfully", accounts)
}

// GetAccount godoc
// @Summary Get account by ID
// @Tags accounts
// @Produce json
// @Param id path int true "Account ID"
// @Success 200 {object} response.GeneralResponse
// @Router /accounts/{id} [get]
func (h *AccountHandler) GetAccount(c *gin.Context) {
    id, err := strconv.ParseInt(c.Param("id"), 10, 64)
    if err != nil {
        response.BadRequest(c, "INVALID_ID", "Invalid account ID", nil)
        return
    }
    
    account, err := h.store.GetAccountByID(c.Request.Context(), id)
    if err != nil {
        response.NotFound(c, "ACCOUNT_NOT_FOUND", "Account not found")
        return
    }
    
    // Verify ownership
    userID, _ := c.Get("user_id")
    if account.UserID != userID.(int64) {
        response.Fail(c, http.StatusForbidden, "FORBIDDEN", "Access denied", nil)
        return
    }
    
    response.OK(c, "Account retrieved successfully", account)
}

// UpdateAccount godoc
// @Summary Update account
// @Tags accounts
// @Accept json
// @Produce json
// @Param id path int true "Account ID"
// @Param account body models.Account true "Account data"
// @Success 200 {object} response.GeneralResponse
// @Router /accounts/{id} [put]
func (h *AccountHandler) UpdateAccount(c *gin.Context) {
    id, err := strconv.ParseInt(c.Param("id"), 10, 64)
    if err != nil {
        response.BadRequest(c, "INVALID_ID", "Invalid account ID", nil)
        return
    }
    
    var account models.Account
    if err := c.ShouldBindJSON(&account); err != nil {
        response.BadRequest(c, "INVALID_INPUT", "Invalid request data", err.Error())
        return
    }
    
    userID, _ := c.Get("user_id")
    account.ID = id
    account.UserID = userID.(int64)
    
    if err := h.store.UpdateAccount(c.Request.Context(), &account); err != nil {
        response.InternalError(c, "Failed to update account", err.Error())
        return
    }
    
    response.OK(c, "Account updated successfully", account)
}

// DeleteAccount godoc
// @Summary Delete account
// @Tags accounts
// @Produce json
// @Param id path int true "Account ID"
// @Success 200 {object} response.GeneralResponse
// @Router /accounts/{id} [delete]
func (h *AccountHandler) DeleteAccount(c *gin.Context) {
    id, err := strconv.ParseInt(c.Param("id"), 10, 64)
    if err != nil {
        response.BadRequest(c, "INVALID_ID", "Invalid account ID", nil)
        return
    }
    
    userID, _ := c.Get("user_id")
    
    if err := h.store.DeleteAccount(c.Request.Context(), id, userID.(int64)); err != nil {
        response.InternalError(c, "Failed to delete account", err.Error())
        return
    }
    
    response.OK(c, "Account deleted successfully", nil)
}
```

### Step 4: Register Routes

**File:** `internal/server/routes.go`
```go
package server

import (
    "github.com/gin-gonic/gin"
    "github.com/ppChub722/finna-bbear-be/internal/server/handlers"
    "github.com/ppChub722/finna-bbear-be/internal/server/middleware"
    "github.com/ppChub722/finna-bbear-be/internal/store"
)

func SetupRoutes(router *gin.Engine, store *store.Store, jwtSecret string) {
    // Initialize handlers
    accountHandler := handlers.NewAccountHandler(store)
    
    // Public routes
    api := router.Group("/api/v1")
    
    // Protected routes (require authentication)
    protected := api.Group("")
    protected.Use(middleware.AuthMiddleware(jwtSecret))
    {
        // Account routes
        accounts := protected.Group("/accounts")
        {
            accounts.POST("", accountHandler.CreateAccount)
            accounts.GET("", accountHandler.GetAccounts)
            accounts.GET("/:id", accountHandler.GetAccount)
            accounts.PUT("/:id", accountHandler.UpdateAccount)
            accounts.DELETE("/:id", accountHandler.DeleteAccount)
        }
    }
}
```

### Step 5: Test Your Module

Create test file: `internal/store/account_store_test.go`
```go
package store

import (
    "context"
    "testing"
    "github.com/ppChub722/finna-bbear-be/internal/models"
)

func TestCreateAccount(t *testing.T) {
    // Setup test database connection
    store := setupTestStore(t)
    defer teardownTestStore(t, store)
    
    account := &models.Account{
        UserID:         1,
        Name:           "Test Account",
        Type:           "bank",
        InitialBalance: 1000.00,
    }
    
    err := store.CreateAccount(context.Background(), account)
    if err != nil {
        t.Fatalf("Failed to create account: %v", err)
    }
    
    if account.ID == 0 {
        t.Error("Expected account ID to be set")
    }
}
```

---

## 📂 Folder Structure Explained

### `/cmd/api/`
**Purpose:** Application entry points

- `main.go` - Initializes config, database, server, and starts the application
- Keep this minimal - delegates to internal packages

**When to add here:**
- New executable commands
- Different server types (worker, cron, etc.)

---

### `/internal/`
**Purpose:** Private application code (not importable by external projects)

#### `/internal/auth/`
**Purpose:** Authentication and authorization logic

**Files:**
- `auth.go` - Main auth service
- `password.go` - Password hashing with bcrypt
- `token.go` - JWT token generation and validation
- `auth_test.go` - Authentication tests

**When to modify:**
- Add new authentication methods (OAuth, etc.)
- Change JWT claims structure
- Update password policies

---

#### `/internal/config/`
**Purpose:** Configuration management

**Files:**
- `config.go` - Loads and validates environment variables

**Responsibilities:**
- Load `.env` file
- Provide default values
- Validate required configurations
- Build connection strings

**When to modify:**
- Add new configuration variables
- Change validation rules
- Add new environment types

---

#### `/internal/logger/`
**Purpose:** Structured logging

**Files:**
- `logger.go` - Creates slog logger instances

**Features:**
- JSON logging for production
- Text logging for development
- Source file tracking

**When to modify:**
- Change log levels
- Add custom log fields
- Integrate with external logging services

---

#### `/internal/models/`
**Purpose:** Data models and structs

**Files:**
- `user.go` - User model
- `account.go` - Account model (example)
- `transaction.go` - Transaction model (example)

**Structure:**
```go
type Model struct {
    ID        int64     `json:"id"`
    Field     string    `json:"field" binding:"required"`
    CreatedAt time.Time `json:"created_at"`
}
```

**When to add:**
- New database table representation
- Request/Response DTOs
- Business domain objects

---

#### `/internal/response/`
**Purpose:** Standardized HTTP responses

**Files:**
- `response.go` - Response helper functions

**Standard Response Format:**
```json
{
  "success": true,
  "message": "Operation successful",
  "data": {...}
}
```

**Error Response Format:**
```json
{
  "success": false,
  "error": {
    "code": "ERROR_CODE",
    "message": "Error description",
    "details": {...}
  }
}
```

**Available Functions:**
- `OK()` - 200 success
- `Created()` - 201 created
- `BadRequest()` - 400 error
- `NotFound()` - 404 error
- `InternalError()` - 500 error

---

#### `/internal/server/`
**Purpose:** HTTP server setup, handlers, routes, middleware

**Suggested Structure:**
```
server/
├── handlers/
│   ├── user_handler.go
│   ├── account_handler.go
│   └── transaction_handler.go
├── middleware/
│   ├── auth.go
│   ├── logger.go
│   └── cors.go
├── routes.go
└── server.go
```

**When to add:**
- New API endpoints
- New middleware
- Route groups

---

#### `/internal/store/`
**Purpose:** Database access layer (Repository pattern)

**Files:**
- `store.go` - Main store struct and interface
- `user_store.go` - User-related DB operations (example)
- `account_store.go` - Account-related DB operations (example)

**Responsibilities:**
- Execute SQL queries
- Map database rows to models
- Handle database errors
- Transaction management

**Pattern:**
```go
func (s *Store) OperationName(ctx context.Context, params...) (result, error) {
    query := `SQL QUERY HERE`
    // Execute and return
}
```

---

### `/migrations/`
**Purpose:** Database schema version control

**Naming Convention:**
```
{version}_{description}.up.sql    # Apply changes
{version}_{description}.down.sql  # Revert changes
```

**Example:**
```
000001_init_schema.up.sql
000001_init_schema.down.sql
000002_add_user_email.up.sql
000002_add_user_email.down.sql
```

**Best Practices:**
- Always create both up and down migrations
- Test migrations in development first
- Never edit applied migrations
- Keep migrations small and focused

---

## 🔄 Development Workflow

### Daily Development Cycle

1. **Pull Latest Changes**
   ```bash
   git pull origin main
   ```

2. **Create Feature Branch**
   ```bash
   git checkout -b feature/account-module
   ```

3. **Run Migrations (if any new)**
   ```bash
   migrate.bat up
   ```

4. **Start Development Server**
   ```bash
   run.bat
   ```

5. **Make Changes**
   - Edit code
   - Server auto-restarts (if using air)
   - Test endpoints

6. **Test Changes**
   ```bash
   go test ./...
   ```

7. **Commit Changes**
   ```bash
   git add .
   git commit -m "feat: add account CRUD operations"
   ```

8. **Push to Remote**
   ```bash
   git push origin feature/account-module
   ```

---

## 🧪 Testing

### Running Tests

**All tests:**
```bash
go test ./...
```

**Specific package:**
```bash
go test ./internal/auth
```

**With coverage:**
```bash
go test -cover ./...
```

**Verbose output:**
```bash
go test -v ./...
```

### Writing Tests

**Example Test:**
```go
package store

import (
    "context"
    "testing"
)

func TestCreateUser(t *testing.T) {
    // Arrange
    store := setupTestStore(t)
    defer teardownTestStore(t, store)
    
    user := &models.User{
        Username: "testuser",
        Email:    "test@example.com",
        Password: "hashedpassword",
    }
    
    // Act
    err := store.CreateUser(context.Background(), user)
    
    // Assert
    if err != nil {
        t.Fatalf("Expected no error, got %v", err)
    }
    
    if user.ID == 0 {
        t.Error("Expected user ID to be set")
    }
}
```

---

## 🛠️ Common Tasks

### Add New Database Migration

```bash
# Create migration files
migrate create -ext sql -dir migrations -seq add_user_email

# Edit the files:
# migrations/000002_add_user_email.up.sql
# migrations/000002_add_user_email.down.sql

# Apply migration
migrate.bat up
```

### Add New Environment Variable

1. Add to `env.example`:
   ```env
   NEW_VARIABLE=default_value
   ```

2. Add to `internal/config/config.go`:
   ```go
   type Config struct {
       // ... existing fields
       NewVariable string
   }
   
   // In Load() function
   NewVariable: getEnv("NEW_VARIABLE", "default"),
   ```

3. Update your `.env` file

### Add New API Endpoint

1. Create handler function
2. Register route in `routes.go`
3. Test endpoint
4. Document in README

### Debug Database Queries

Add logging to store methods:
```go
func (s *Store) CreateUser(ctx context.Context, user *models.User) error {
    query := `INSERT INTO users...`
    log.Printf("Executing query: %s with params: %v", query, user)
    // ... execute query
}
```

---

## ✅ Best Practices

### Code Style
- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use `gofmt` to format code
- Run `go vet` to catch issues
- Use meaningful variable names

### Error Handling
```go
// Good
if err != nil {
    return fmt.Errorf("failed to create user: %w", err)
}

// Bad
if err != nil {
    return err
}
```

### Context Usage
Always pass context for database operations:
```go
func (s *Store) GetUser(ctx context.Context, id int64) (*models.User, error) {
    // Use ctx in query
}
```

### SQL Queries
- Use parameterized queries (prevent SQL injection)
- Use `$1, $2` placeholders
- Keep queries readable with proper formatting

### Security
- Never log sensitive data (passwords, tokens)
- Always validate user input
- Use prepared statements
- Hash passwords with bcrypt
- Validate JWT tokens

---

## 🐛 Troubleshooting

### Database Connection Issues

**Problem:** `Unable to connect to database`

**Solutions:**
1. Check PostgreSQL is running:
   ```bash
   pg_ctl status
   ```

2. Verify credentials in `.env`

3. Test connection:
   ```bash
   psql -U chubadmin -d finna_bbear_db
   ```

---

### Migration Errors

**Problem:** `Migration failed`

**Solutions:**
1. Check migration syntax
2. Rollback and retry:
   ```bash
   migrate.bat down
   migrate.bat up
   ```

3. Force version if stuck:
   ```bash
   migrate.bat force 1
   ```

---

### Port Already in Use

**Problem:** `Port 8080 already in use`

**Solutions:**
1. Change port in `.env`:
   ```env
   APP_PORT=8081
   ```

2. Or kill process:
   ```cmd
   netstat -ano | findstr :8080
   taskkill /PID <PID> /F
   ```

---

### Import Errors

**Problem:** `package not found`

**Solutions:**
```bash
go mod tidy
go mod download
```

---

## 📚 Additional Resources

- [Go Documentation](https://go.dev/doc/)
- [Gin Framework](https://gin-gonic.com/)
- [pgx Documentation](https://pkg.go.dev/github.com/jackc/pgx/v5)
- [PostgreSQL Docs](https://www.postgresql.org/docs/)
- [JWT.io](https://jwt.io/)

---

## 🤝 Getting Help

- Check existing issues in the repository
- Review this guide thoroughly
- Ask team members
- Consult official documentation

---

**Happy Coding! 🐻✨**

