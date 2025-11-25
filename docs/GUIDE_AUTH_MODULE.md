# Module Creation Guide: Authentication

This document describes the step-by-step process used to build the `auth` module.
**Pattern:** Database First -> Data Layer -> Logic Layer -> HTTP Layer -> Wiring.

---

## Step 1: Database Design & Migration
Before writing code, we defined the data structure.

1.  **Design:**
    * Table: `users`
    * Columns: `id` (BigSerial), `username`, `email`, `password_hash`, `timestamps`.
2.  **Migration:**
    * Created file: `migrations/000001_create_users_table.up.sql`
    * Ran command: `migrate up` to create the table in Postgres.

---

## Step 2: Data Layer (The Foundation)
We created the Go structures to match the database.

* **`internal/modules/auth/model.go`**
    * Defined the `User` struct with `db` tags.
    * Defined `RegisterRequest` and `LoginRequest` with `json` and validation tags.

* **`internal/modules/auth/store.go`**
    * Implemented `NewStore(db)`.
    * Wrote SQL queries for `CreateUser` and `GetUserByIdentifier`.
    * Handled specific DB errors (like Duplicate Email).

---

## Step 3: Logic Layer (The Brains)
We added the business logic and security features.

* **`internal/modules/auth/utils/`**
    * `password.go`: Uses `bcrypt` to hash and check passwords.
    * `token.go`: Uses `jwt` to generate secure tokens.

* **`internal/modules/auth/service.go`**
    * Implemented `NewService(store, config)`.
    * **RegisterUser:** Hash Password -> Call Store -> Generate Token.
    * **LoginUser:** Find User -> Check Password -> Generate Token.

---

## Step 4: HTTP Layer (The Doorway)
We exposed the logic to the outside world via API endpoints.

* **`internal/modules/auth/handler.go`**
    * Implemented `NewHandler(service)`.
    * **Register:** Parse JSON -> Call `service.RegisterUser` -> Return 201.
    * **Login:** Parse JSON -> Call `service.LoginUser` -> Return 200.
    * Used `response` package for consistent error handling.

---

## Step 5: Wiring (The Assembly)
Finally, we connected everything in `cmd/api/main.go`.

```go
// 1. Create Repository (needs DB)
authStore := auth.NewStore(dbPool)

// 2. Create Service (needs Store + Config)
authService := auth.NewService(authStore, cfg)

// 3. Create Handler (needs Service)
authHandler := auth.NewHandler(authService)

// 4. Register Routes
api.POST("/auth/register", authHandler.Register)