# Project Structure Guide

This project follows the **Standard Go Project Layout** adapted for Modular Monolith architecture. It separates "Infrastructure" (Platform) from "Features" (Modules).

## 📂 Root Directory
* **`cmd/api/main.go`**: The entry point. This is where the application starts, loads config, connects to the database, and wires everything together.
* **`migrations/`**: Contains raw SQL files to create/change database tables. Managed by `golang-migrate`.
* **`docs/`**: Documentation for developers and setup guides.
* **`go.mod`**: Defines dependencies.
* **`.env`**: (Ignored by Git) Local environment variables like Database passwords.

---

## 📂 Internal Directory (`internal/`)
This folder contains the private application code.

### 1. `internal/platform/` (The Foundation)
These are the core technical tools that every part of the app needs. They don't know about "Users" or "Money"; they just handle low-level tasks.
* **`config/`**: Loads environment variables safely into a struct.
* **`database/`**: Manages the PostgreSQL connection pool (`pgx`).
* **`logger/`**: Sets up structured logging (`slog`).
* **`response/`**: Helper functions for consistent JSON API responses (`Success`, `BadRequest`, `InternalError`).

### 2. `internal/modules/` (The Features)
This is where the actual business features live. Each folder is a self-contained feature (like "Auth", "Accounting", "Wallet").

#### Anatomy of a Module (e.g., `auth/`)
We follow a 3-Layer Architecture inside each module:

1.  **`handler.go` (HTTP Layer / Controller)**
    * **Role:** Receives JSON requests from Gin.
    * **Job:** Validates input, calls the Service, and sends JSON responses.
    * **Dependency:** Depends on `Service`.

2.  **`service.go` (Logic Layer)**
    * **Role:** The brain of the module.
    * **Job:** Hashes passwords, generates JWTs, enforces business rules.
    * **Dependency:** Depends on `Store`.

3.  **`store.go` (Data Layer / Repository)**
    * **Role:** The only file that talks to SQL.
    * **Job:** Runs `INSERT`, `SELECT` queries.
    * **Dependency:** Depends on `pgxpool` (Database Connection).

4.  **`model.go`**
    * **Role:** Defines the Go structs that match the Database Tables and API Requests.

5.  **`utils/` (Optional)**
    * Helper functions specific to this module (e.g., Password Hashing, Token Generation).