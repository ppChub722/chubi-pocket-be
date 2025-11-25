# Local Database Setup Guide

> **⚠️ SECURITY NOTE:** > The passwords used in this script (`admin1234`) are for **local study and development only**. 
> If you deploy this to a public server, NEVER use these hardcoded passwords. Instead, use secure Environment Variables and strong, random passwords.

## Prerequisites

1.  **Docker Desktop** must be installed and running.
2.  **Golang Migrate** tool must be installed.
    * To install: `go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest`

---

## Reset & Setup Script (PowerShell)

You can copy and paste this entire block into your PowerShell terminal. Make sure you are in the **root folder** of your project (where the `migrations` folder is).

```powershell
# ---------------------------------------------------------
# STEP 1: CLEANUP
# Stop and remove the old container if it exists
# ---------------------------------------------------------
docker rm -f finna-bbear-db-container

# ---------------------------------------------------------
# STEP 2: START POSTGRES
# Starts a new container named 'finna-bbear-db-container'
# Root Password: admin1234
# ---------------------------------------------------------
docker run --name finna-bbear-db-container -e POSTGRES_PASSWORD=admin1234 -p 5432:5432 -d postgres:15

Write-Host "⏳ Waiting 5 seconds for Database to wake up..."
Start-Sleep -Seconds 5

# ---------------------------------------------------------
# STEP 3: CONFIGURE DATABASE
# Create the specific user and database for the App
# App User: chubadmin / admin1234
# Database: finna_bbear_db
# ---------------------------------------------------------
docker exec -i finna-bbear-db-container psql -U postgres -c "CREATE USER chubadmin WITH PASSWORD 'admin1234' CREATEDB;"
docker exec -i finna-bbear-db-container psql -U postgres -c "CREATE DATABASE finna_bbear_db OWNER chubadmin;"

# ---------------------------------------------------------
# STEP 4: RUN MIGRATIONS
# Connects as 'chubadmin' and creates the tables from your /migrations folder
# ---------------------------------------------------------
migrate -path migrations -database "postgres://chubadmin:admin1234@localhost:5432/finna_bbear_db?sslmode=disable" up

Write-Host "✅ Database Setup Complete! Tables are ready."

Table "public.users"
    Column     |           Type           | Collation | Nullable |                Default
---------------+--------------------------+-----------+----------+---------------------------------------
 id            | bigint                   |           | not null | nextval('users_id_seq'::regclass)
 username      | character varying(50)    |           | not null |
 email         | character varying(255)   |           | not null |
 password_hash | character varying(255)   |           | not null |
 created_at    | timestamp with time zone |           |          | now()
 updated_at    | timestamp with time zone |           |          | now()
Indexes:
    "users_pkey" PRIMARY KEY, btree (id)
    "users_email_key" UNIQUE CONSTRAINT, btree (email)
    "users_username_key" UNIQUE CONSTRAINT, btree (username)
    "idx_users_email" btree (email)
    "idx_users_username" btree (username)
Triggers:
    set_timestamp BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION trigger_set_timestamp()