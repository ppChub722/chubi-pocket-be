# finna-bbear-be

🐻 **FinaBBear Backend** - Personal Finance Management API

## Prerequisites

- Go 1.25.3+
- PostgreSQL 18
- golang-migrate CLI

## Installation

### 1. Install golang-migrate

```bash
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

Make sure `$GOPATH/bin` is in your PATH.

### 2. Set Up Environment Variables

**On Windows:**
```cmd
setup-env.bat
```

**On Linux/Mac:**
```bash
./setup-env.sh
```

This will create a `.env` file from the template. Edit `.env` and update your configuration:

```env
# Database Configuration
DB_HOST=localhost
DB_PORT=5432
DB_USER=chubadmin
DB_PASSWORD=admin1234
DB_NAME=finna_bbear_db

# JWT Secret (CHANGE THIS!)
JWT_SECRET=your-super-secret-jwt-key-change-this-in-production

# App Configuration
APP_PORT=8080
APP_ENV=development
```

**⚠️ IMPORTANT:** Never commit `.env` file to git! It contains sensitive information.

### 3. Database Setup

Create the database and user:

```sql
CREATE DATABASE finna_bbear_db;
CREATE USER chubadmin WITH PASSWORD 'admin1234';
GRANT ALL PRIVILEGES ON DATABASE finna_bbear_db TO chubadmin;
-- Connect to the database first
\c finna_bbear_db
GRANT ALL ON SCHEMA public TO chubadmin;
```

### 4. Run Migrations

**On Windows:**
```cmd
migrate.bat up
```

**On Linux/Mac:**
```bash
make migrate-up
```

**Or manually:**
```bash
migrate -path migrations -database "postgres://chubadmin:admin1234@localhost:5432/finna_bbear_db?sslmode=disable" up
```

## Running the Application

**On Windows:**
```cmd
run.bat
```

**On Linux/Mac:**
```bash
make run
```

**Or directly:**
```bash
go run cmd/api/main.go
```

The server will start on `http://localhost:8080` (or the port specified in `.env`)

## Configuration

All configuration is managed through environment variables. See `env.example` for all available options:

| Variable | Description | Default |
|----------|-------------|---------|
| `APP_NAME` | Application name | FinaBBear |
| `APP_ENV` | Environment (development/production) | development |
| `APP_PORT` | Server port | 8080 |
| `DB_HOST` | Database host | localhost |
| `DB_PORT` | Database port | 5432 |
| `DB_USER` | Database user | chubadmin |
| `DB_PASSWORD` | Database password | admin1234 |
| `DB_NAME` | Database name | finna_bbear_db |
| `DB_SSLMODE` | SSL mode | disable |
| `JWT_SECRET` | JWT signing secret | *must be changed* |
| `JWT_EXPIRATION_HOURS` | JWT token expiration | 24 |
| `SERVER_READ_TIMEOUT` | HTTP read timeout (seconds) | 10 |
| `SERVER_WRITE_TIMEOUT` | HTTP write timeout (seconds) | 10 |
| `SERVER_IDLE_TIMEOUT` | HTTP idle timeout (seconds) | 120 |
| `LOG_LEVEL` | Logging level | debug |

## API Endpoints

- `GET /` - API information and status
- `GET /ping` - Health check endpoint

**Example Response:**
```json
{
  "app": "FinaBBear",
  "version": "1.0.0",
  "status": "running"
}
```

## Database Schema

The application includes 13 tables across 5 modules:

### Module 1: Core Accounting
- `users` - User authentication and profiles
- `accounts` - Financial accounts (bank, cash, credit cards, etc.)
- `categories` - Transaction categories
- `transactions` - All financial transactions

### Module 2: Fast Input
- `pending_transactions` - Transactions awaiting confirmation

### Module 3: Social Splitting
- `connections` - User connections for expense sharing
- `projects` - Shared expense projects
- `shared_expenses` - Expenses split among users
- `expense_splits` - Individual split amounts

### Module 4: Forecasting & Planning
- `budgets` - Budget allocations by category
- `recurring_transactions` - Scheduled recurring transactions

### Module 5: Loan Management
- `loans` - Loan information and tracking
- `loan_payments` - Individual loan payment records

## Available Commands

### Windows (Batch Scripts)
- `run.bat` - Run the application
- `migrate.bat up` - Run database migrations
- `migrate.bat down` - Rollback last migration
- `migrate.bat status` - Check current migration version
- `migrate.bat force 1` - Force specific migration version

### Linux/Mac (Makefile)
- `make run` - Run the application
- `make migrate-up` - Run database migrations
- `make migrate-down` - Rollback last migration
- `make db-status` - Check current migration version
- `make migrate-force V=1` - Force specific migration version

## Troubleshooting

### Tables not created?

If tables aren't being created automatically, run:

**Windows:**
```cmd
migrate.bat up
```

**Linux/Mac:**
```bash
make migrate-up
```

### Migration stuck in dirty state?

**Windows:**
```cmd
migrate.bat force 1
migrate.bat up
```

**Linux/Mac:**
```bash
make migrate-force V=1
make migrate-up
```

### Check current database state

**Windows:**
```cmd
migrate.bat status
```

**Linux/Mac:**
```bash
make db-status
```