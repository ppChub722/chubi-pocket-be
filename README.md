# FinaBBear Backend

Personal finance application backend built with **Go**, **Gin**, and **PostgreSQL**. Features modular monolith architecture with 14 database tables across 6 feature groups.

---

## Quick Start

```bash
# 1. Start PostgreSQL
docker compose up -d

# 2. Install migrate CLI (if not installed)
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# 3. Run migrations
make migrate-up

# 4. Start the server
make run
```

Server runs at `http://localhost:8080`

---

## Tech Stack

| Component | Technology |
|:---|:---|
| Language | Go 1.25+ |
| Framework | Gin |
| Database | PostgreSQL 15+ |
| Driver | pgx/v5 (connection pooling) |
| Auth | JWT (golang-jwt/v5) |
| Password | bcrypt |
| Migrations | golang-migrate |
| Logging | slog (structured) |
| Hot Reload | Air |

---

## API Endpoints

### Auth & Profile

| Method | Endpoint | Description |
|:---|:---|:---|
| `POST` | `/api/v1/auth/register` | Register new user |
| `POST` | `/api/v1/auth/login` | Login, receive JWT token |
| `POST` | `/api/v1/auth/logout` | Logout (invalidate token) |
| `GET` | `/api/v1/users/me` | Get profile |
| `PUT` | `/api/v1/users/me` | Update profile |
| `PUT` | `/api/v1/users/me/password` | Change password |

### Accounts

| Method | Endpoint | Description |
|:---|:---|:---|
| `POST` | `/api/v1/accounts` | Create account |
| `GET` | `/api/v1/accounts` | List accounts |
| `GET` | `/api/v1/accounts/:id` | Get account |
| `PUT` | `/api/v1/accounts/:id` | Update account |
| `DELETE` | `/api/v1/accounts/:id` | Deactivate account |
| `GET` | `/api/v1/accounts/:id/summary` | Account summary |

### Categories

| Method | Endpoint | Description |
|:---|:---|:---|
| `POST` | `/api/v1/categories` | Create category |
| `GET` | `/api/v1/categories` | List categories (hierarchical tree) |
| `PUT` | `/api/v1/categories/:id` | Update category |
| `DELETE` | `/api/v1/categories/:id` | Deactivate category |

### Tags

| Method | Endpoint | Description |
|:---|:---|:---|
| `POST` | `/api/v1/tags` | Create tag |
| `GET` | `/api/v1/tags` | List tags |
| `PUT` | `/api/v1/tags/:id` | Update tag |
| `DELETE` | `/api/v1/tags/:id` | Delete tag |

### Transactions

| Method | Endpoint | Description |
|:---|:---|:---|
| `POST` | `/api/v1/transactions` | Create transaction |
| `GET` | `/api/v1/transactions` | List with filters & pagination |
| `GET` | `/api/v1/transactions/summary` | Aggregated totals |
| `GET` | `/api/v1/transactions/:id` | Get transaction with details |
| `PUT` | `/api/v1/transactions/:id` | Update transaction |
| `DELETE` | `/api/v1/transactions/:id` | Delete transaction |
| `POST` | `/api/v1/transactions/:id/tags` | Add tags |
| `DELETE` | `/api/v1/transactions/:id/tags/:tag_id` | Remove tag |

### Shared Expenses

| Method | Endpoint | Description |
|:---|:---|:---|
| `POST` | `/api/v1/shared-expenses` | Create shared expense with splits |
| `GET` | `/api/v1/shared-expenses` | List shared expenses |
| `GET` | `/api/v1/shared-expenses/summary` | Dashboard summary |
| `GET` | `/api/v1/shared-expenses/:id` | Get with splits |
| `PUT` | `/api/v1/shared-expenses/:id` | Update description |
| `DELETE` | `/api/v1/shared-expenses/:id` | Delete |
| `GET` | `/api/v1/shared-expenses/:id/splits` | List splits |
| `POST` | `/api/v1/shared-expenses/:id/splits` | Add split |
| `PUT` | `/api/v1/splits/:id` | Update split |
| `DELETE` | `/api/v1/splits/:id` | Delete split |
| `GET` | `/api/v1/splits/:id/settlements` | List settlements |
| `POST` | `/api/v1/splits/:id/settlements` | Record settlement |
| `DELETE` | `/api/v1/settlements/:id` | Delete settlement |

### Budgets

| Method | Endpoint | Description |
|:---|:---|:---|
| `POST` | `/api/v1/budgets` | Create budget |
| `GET` | `/api/v1/budgets` | List budgets with spending |
| `GET` | `/api/v1/budgets/overview` | All budgets summary |
| `GET` | `/api/v1/budgets/:id` | Get with child breakdown |
| `PUT` | `/api/v1/budgets/:id` | Update budget |
| `DELETE` | `/api/v1/budgets/:id` | Delete budget |

### Saving Goals

| Method | Endpoint | Description |
|:---|:---|:---|
| `POST` | `/api/v1/saving-goals` | Create goal |
| `GET` | `/api/v1/saving-goals` | List goals |
| `GET` | `/api/v1/saving-goals/:id` | Get with progress |
| `PUT` | `/api/v1/saving-goals/:id` | Update goal |
| `DELETE` | `/api/v1/saving-goals/:id` | Delete goal |

### Projects

| Method | Endpoint | Description |
|:---|:---|:---|
| `POST` | `/api/v1/projects` | Create project |
| `GET` | `/api/v1/projects` | List projects |
| `GET` | `/api/v1/projects/:id` | Get with financial summary |
| `PUT` | `/api/v1/projects/:id` | Update project |
| `DELETE` | `/api/v1/projects/:id` | Delete project |
| `GET` | `/api/v1/projects/:id/summary` | P&L summary |
| `GET` | `/api/v1/projects/:id/members` | List members |
| `POST` | `/api/v1/projects/:id/members` | Add member |
| `PUT` | `/api/v1/projects/:id/members/:user_id` | Update role |
| `DELETE` | `/api/v1/projects/:id/members/:user_id` | Remove member |
| `POST` | `/api/v1/projects/:id/leave` | Leave project |

### Recurring & Installments

| Method | Endpoint | Description |
|:---|:---|:---|
| `POST` | `/api/v1/recurrings` | Create recurring/installment |
| `GET` | `/api/v1/recurrings` | List entries |
| `GET` | `/api/v1/recurrings/upcoming` | Due in next N days |
| `GET` | `/api/v1/recurrings/:id` | Get entry |
| `PUT` | `/api/v1/recurrings/:id` | Update entry |
| `DELETE` | `/api/v1/recurrings/:id` | Delete entry |
| `POST` | `/api/v1/recurrings/:id/pause` | Pause |
| `POST` | `/api/v1/recurrings/:id/resume` | Resume |
| `POST` | `/api/v1/recurrings/:id/cancel` | Cancel |

---

## Project Structure

```
.
├── cmd/api/                  # Entry point (main.go)
├── internal/
│   ├── modules/
│   │   ├── auth/             # Authentication & user profile
│   │   ├── accounting/       # Accounts, categories, tags, transactions
│   │   ├── shared/           # Shared expenses, splits, settlements
│   │   ├── budget/           # Budgets & saving goals
│   │   ├── project/          # Projects & members
│   │   └── recurring/        # Recurring payments & installments
│   └── platform/
│       ├── config/           # Environment config loader
│       ├── database/         # PostgreSQL connection pool
│       ├── logger/           # Structured logging (slog)
│       └── response/         # JSON response helpers
├── migrations/               # SQL migration files (16 migrations)
├── postman/                  # Postman collection files
├── docker-compose.yml        # PostgreSQL container
├── Makefile                  # Build & migration commands
└── .air.toml                 # Hot-reload config
```

### Module Architecture

Each module follows a 3-layer pattern:

```
handler.go  →  HTTP layer (request/response)
service.go  →  Business logic
store.go    →  Database queries
model.go    →  Structs & request types
```

---

## Database Schema

14 tables across 6 feature groups:

| Group | Tables |
|:---|:---|
| Users & Accounts | `users`, `accounts` |
| Transactions & Organization | `transactions`, `categories`, `tags`, `transaction_tags` |
| Shared Expenses | `shared_expenses`, `shared_expense_splits`, `split_settlements` |
| Budgets & Savings | `budgets`, `saving_goals` |
| Projects | `projects`, `project_members` |
| Recurring & Installments | `recurrings` |

---

## Make Commands

```bash
make run             # Run the application
make migrate-up      # Apply all migrations
make migrate-down    # Rollback last migration
make db-status       # Check migration version
make migrate-force V=N  # Force migration version
```
