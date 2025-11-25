# Project Configuration Guide

This project uses **Environment Variables** to manage sensitive data (like passwords) and settings. We use the `internal/platform/config` package to load these safely.

---

## 1. Prerequisites (Software)

Before running the code, ensure you have these installed:

* **Go (Golang):** Version 1.22 or higher.
* **Git:** For version control.
* **PostgreSQL:** (Or use Docker as described in `SETUP_LOCAL_DB.md`).

---

## 2. Environment Variables (`.env`)

The application looks for a file named `.env` in the root directory.
**DO NOT** commit the actual `.env` file to Git (it is ignored in `.gitignore`).

### How to set it up:
1.  Create a file named `.env` in the project root.
2.  Copy the content below into it.

```ini
# --- Application Settings ---
APP_NAME=FinaBBear
APP_ENV=development
APP_PORT=8080

# --- Database Configuration ---
# Must match your Docker/Postgres setup
DB_HOST=localhost
DB_PORT=5432
DB_USER=chubadmin
DB_PASSWORD=admin1234
DB_NAME=finna_bbear_db
DB_SSLMODE=disable

# --- Security (JWT) ---
# CHANGE THIS SECRET IN PRODUCTION!
JWT_SECRET=your-super-secret-jwt-key-change-this
JWT_EXPIRATION_HOURS=24

# --- Server Timeouts (Advanced) ---
SERVER_READ_TIMEOUT=10
SERVER_WRITE_TIMEOUT=10
SERVER_IDLE_TIMEOUT=120