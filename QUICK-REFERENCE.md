# 🐻 FinaBBear Quick Reference Guide

Quick reference for common tasks and commands.

---

## 🚀 Running the Application

### Start Server
```bash
# Windows
run.bat

# Linux/Mac
make run

# Direct
go run cmd/api/main.go
```

### Stop Server
Press `Ctrl + C` in the terminal

---

## 🗄️ Database Operations

### Migrations
```bash
# Windows
migrate.bat up          # Apply migrations
migrate.bat down        # Rollback last migration
migrate.bat status      # Check current version
migrate.bat force 1     # Force version to 1

# Linux/Mac
make migrate-up         # Apply migrations
make migrate-down       # Rollback last migration
make db-status          # Check current version
make migrate-force V=1  # Force version to 1
```

### Create New Migration
```bash
migrate create -ext sql -dir migrations -seq migration_name
```

### Check Database Connection
```bash
# psql
psql -U chubadmin -d finna_bbear_db

# From code
go run cmd/api/main.go
# Look for: ✅ Database connected successfully!
```

---

## 📁 Project Structure Quick Map

```
finna-bbear-be/
├── cmd/api/main.go           → Application entry point
├── internal/
│   ├── auth/                 → Authentication logic
│   ├── config/               → Configuration management
│   ├── logger/               → Logging utilities
│   ├── models/               → Data models
│   ├── response/             → HTTP response helpers
│   ├── server/               → HTTP handlers & routes
│   └── store/                → Database operations
├── migrations/               → Database migrations
├── .env                      → Local config (DO NOT COMMIT)
└── env.example               → Config template
```

---

## 🔧 Development Commands

### Dependencies
```bash
go mod download      # Download dependencies
go mod tidy          # Clean up dependencies
go mod vendor        # Vendor dependencies
```

### Testing
```bash
go test ./...              # Run all tests
go test ./internal/auth    # Test specific package
go test -v ./...           # Verbose output
go test -cover ./...       # With coverage
```

### Code Quality
```bash
go fmt ./...        # Format code
go vet ./...        # Lint code
```

### Build
```bash
# Development build
go build -o bin/finna-bbear cmd/api/main.go

# Production build
CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd/api
```

---

## 🆕 Creating New Module - Checklist

1. ✅ Create model in `internal/models/`
2. ✅ Create store methods in `internal/store/`
3. ✅ Create handler in `internal/server/handlers/`
4. ✅ Register routes in `internal/server/routes.go`
5. ✅ Write tests
6. ✅ Update documentation

**Example:** See [Developer Guide - Creating New Modules](./DEVELOPER-GUIDE.md#creating-new-modules)

---

## 📝 Common Code Patterns

### Model
```go
type Account struct {
    ID        int64     `json:"id"`
    UserID    int64     `json:"user_id" binding:"required"`
    Name      string    `json:"name" binding:"required"`
    CreatedAt time.Time `json:"created_at"`
}
```

### Store Method
```go
func (s *Store) CreateAccount(ctx context.Context, account *models.Account) error {
    query := `INSERT INTO accounts (...) VALUES (...) RETURNING id, created_at`
    return s.DB.QueryRow(ctx, query, params...).Scan(&account.ID, &account.CreatedAt)
}
```

### Handler
```go
func (h *Handler) CreateAccount(c *gin.Context) {
    var account models.Account
    if err := c.ShouldBindJSON(&account); err != nil {
        response.BadRequest(c, "INVALID_INPUT", "Invalid data", err.Error())
        return
    }
    // ... business logic
    response.Created(c, "Account created", account)
}
```

### Response Helpers
```go
response.OK(c, "Success", data)                    // 200
response.Created(c, "Created", data)               // 201
response.BadRequest(c, "CODE", "Message", details) // 400
response.NotFound(c, "CODE", "Message")            // 404
response.InternalError(c, "Message", details)      // 500
```

---

## 🌐 API Endpoints

### Health & Info
```
GET  /              → API information
GET  /ping          → Health check
```

### User Authentication (Example)
```
POST /api/v1/auth/register   → Register new user
POST /api/v1/auth/login      → Login user
POST /api/v1/auth/logout     → Logout user
```

### Protected Endpoints (Require JWT)
```
GET    /api/v1/accounts          → List accounts
POST   /api/v1/accounts          → Create account
GET    /api/v1/accounts/:id      → Get account
PUT    /api/v1/accounts/:id      → Update account
DELETE /api/v1/accounts/:id      → Delete account
```

---

## 🔒 Environment Variables

### Required
```env
DB_HOST=localhost
DB_PORT=5432
DB_USER=chubadmin
DB_PASSWORD=admin1234
DB_NAME=finna_bbear_db
JWT_SECRET=your-secret-key
```

### Optional
```env
APP_PORT=8080
APP_ENV=development
LOG_LEVEL=debug
SERVER_READ_TIMEOUT=10
SERVER_WRITE_TIMEOUT=10
```

---

## 🐳 Docker Commands

### Basic
```bash
docker-compose up -d          # Start services
docker-compose down           # Stop services
docker-compose ps             # List services
docker-compose logs -f api    # View API logs
```

### Management
```bash
docker-compose build          # Rebuild images
docker-compose restart api    # Restart API
docker-compose exec api sh    # Shell into API container
docker-compose down -v        # Stop and remove volumes
```

---

## 🔍 Troubleshooting Quick Fixes

### Port Already in Use
```bash
# Windows
netstat -ano | findstr :8080
taskkill /PID <PID> /F

# Linux/Mac
lsof -ti:8080 | xargs kill -9
```

### Database Connection Failed
```bash
# Check PostgreSQL status
# Windows: Services → PostgreSQL
# Linux: sudo systemctl status postgresql

# Test connection
psql -U chubadmin -d finna_bbear_db -h localhost
```

### Migration Stuck
```bash
migrate.bat force 1
migrate.bat up
```

### Import Errors
```bash
go mod tidy
go mod download
```

---

## 📊 Logging Examples

### In Code
```go
logger.Info("User created", 
    slog.Int64("user_id", userID),
    slog.String("username", username))

logger.Error("Database error",
    slog.String("error", err.Error()),
    slog.String("operation", "CreateUser"))
```

### View Logs
```bash
# Running server: View in terminal
# Systemd service: sudo journalctl -u finabbear -f
# Docker: docker-compose logs -f api
```

---

## 🧪 Testing Examples

### Unit Test
```go
func TestCreateAccount(t *testing.T) {
    store := setupTestStore(t)
    defer teardownTestStore(t, store)
    
    account := &models.Account{Name: "Test"}
    err := store.CreateAccount(context.Background(), account)
    
    if err != nil {
        t.Fatalf("Expected no error, got %v", err)
    }
}
```

### Run Tests
```bash
go test ./internal/store -v
go test ./... -cover
```

---

## 🔐 Security Checklist

### Before Production
- [ ] Change JWT_SECRET to strong random value
- [ ] Use strong database password (32+ chars)
- [ ] Enable HTTPS/SSL
- [ ] Set APP_ENV=production
- [ ] Enable rate limiting
- [ ] Configure CORS properly
- [ ] Review all environment variables
- [ ] Remove debug logging
- [ ] Set up monitoring

---

## 📦 Deployment Quick Steps

### Docker Deployment
```bash
docker-compose build
docker-compose up -d
docker-compose exec api migrate -path migrations -database "$DB_URL" up
```

### Server Deployment
```bash
git pull
go build -o finabbear cmd/api/main.go
sudo systemctl restart finabbear
sudo systemctl status finabbear
```

---

## 💡 Useful Git Commands

```bash
git status                          # Check status
git add .                           # Stage all changes
git commit -m "feat: description"   # Commit changes
git push origin branch-name         # Push to remote
git pull origin main                # Pull latest
git checkout -b feature/name        # Create branch
```

### Commit Message Convention
```
feat: Add new feature
fix: Bug fix
docs: Documentation update
refactor: Code refactoring
test: Add tests
chore: Maintenance tasks
```

---

## 🔗 Important Links

- **Developer Guide:** [DEVELOPER-GUIDE.md](./DEVELOPER-GUIDE.md)
- **Deployment Guide:** [DEPLOYMENT-GUIDE.md](./DEPLOYMENT-GUIDE.md)
- **README:** [README.md](./README.md)
- **Go Documentation:** https://go.dev/doc/
- **Gin Framework:** https://gin-gonic.com/
- **pgx Driver:** https://pkg.go.dev/github.com/jackc/pgx/v5
- **PostgreSQL Docs:** https://www.postgresql.org/docs/

---

## 🆘 Get Help

1. Check logs for error messages
2. Review relevant guide section
3. Search existing GitHub issues
4. Ask team members
5. Create new GitHub issue with:
   - Error message
   - Steps to reproduce
   - Environment details
   - What you've tried

---

## 📋 Daily Workflow

```bash
# Morning
git pull origin main
run.bat                    # Start server

# During Development
# ... make changes ...
go test ./...              # Test changes
git add .
git commit -m "message"

# End of Day
git push origin branch
# Stop server (Ctrl+C)
```

---

**🐻 Happy Coding! Keep this guide handy for quick reference.**

**Pro Tip:** Bookmark this file in your browser for instant access!

