# 🐻 FinaBBear Backend

FinaBBear is a Finance/Accounting application backend built with **Go (Golang)** and **Gin**. It features a **Modular Monolith** architecture, separating infrastructure ("Platform") from features ("Modules") like Authentication.

---

## 🚀 Quick Start Guide

Follow these steps to get the server running locally.

### 1. Database Setup
You need a PostgreSQL database running. We use Docker for this.
* 👉 **[Read the Database Setup Guide](docs/SETUP_LOCAL_DB.md)**
* *Includes scripts to start Postgres, create the user/db, and run migrations.*

### 2. Configuration
Create a `.env` file in the root directory with your passwords and settings.
* 👉 **[Read the Configuration Guide](docs/SETUP_CONFIGURATION.md)**
* 📄 **[View Example Environment File](docs/env.example)**

### 3. Run the Server
Once the Database is up and `.env` is created:

```bash
# 1. Download dependencies
go mod tidy

# 2. Run the API
go run cmd/api/main.go
```

The server will start at: `http://localhost:8080` (or the port defined in your .env).

-----

## 📚 Developer Documentation

We have detailed guides to help you understand the codebase:

| Topic | Description |
| :--- | :--- |
| **[Project Structure](https://www.google.com/search?q=docs/PROJECT_STRUCTURE.md)** | Explains the folder layout (`internal/`, `cmd/`, `platform/`) and architecture. |
| **[Auth Module Guide](https://www.google.com/search?q=docs/GUIDE_AUTH_MODULE.md)** | Step-by-step tutorial on how we built the Login/Register system. |
| **[Setup Local DB](https://www.google.com/search?q=docs/SETUP_LOCAL_DB.md)** | Instructions for Docker, Postgres, and Migrations. |
| **[Setup Config](https://www.google.com/search?q=docs/SETUP_CONFIGURATION.md)** | How environment variables and the config loader work. |

-----

## 🛠️ Tech Stack

  * **Language:** Go (1.23+)
  * **Framework:** Gin (HTTP Web Framework)
  * **Database:** PostgreSQL 15+
  * **Driver:** pgx/v5 (Connection Pool)
  * **Authentication:** JWT (JSON Web Tokens)
  * **Security:** bcrypt (Password Hashing)
  * **Migrations:** golang-migrate
  * **Logging:** slog (Structured Logging)

-----

## 🔌 API Endpoints

### 🔐 Auth Module

| Method | Endpoint | Description |
| :--- | :--- | :--- |
| `POST` | `/api/v1/auth/register` | Create a new user account. |
| `POST` | `/api/v1/auth/login` | Login and receive a Bearer Token. |

*(More feature modules like Accounting and Wallets will be added here...)*

-----

## 📂 Folder Overview

```text
.
├── cmd/api/            # Application entry point (main.go)
├── docs/               # Documentation & Setup Guides
├── internal/
│   ├── modules/        # Feature Modules (Auth, Accounting, etc.)
│   └── platform/       # Core Infrastructure (Database, Logger, Config)
├── migrations/         # SQL Migration files
├── .env                # Local secrets (Not in Git)
├── go.mod              # Dependencies
└── README.md           # This file
```