/* ========= CORE TYPES ========= */
CREATE TYPE "account_type" AS ENUM ('bank','cash','credit_card','e_wallet','investment','loan');
CREATE TYPE "transaction_type" AS ENUM ('income','expense','transfer');

/* ========= MODULE 1: CORE ACCOUNTING ========= */
CREATE TABLE "users" ( "id" bigserial PRIMARY KEY, "username" text NOT NULL UNIQUE, "password_hash" text NOT NULL, "created_at" timestamptz NOT NULL DEFAULT (now()) );
CREATE TABLE "accounts" ( "id" bigserial PRIMARY KEY, "user_id" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "name" text NOT NULL, "type" account_type NOT NULL, "initial_balance" decimal(19, 4) NOT NULL DEFAULT 0, "created_at" timestamptz NOT NULL DEFAULT (now()) );
CREATE TABLE "categories" ( "id" bigserial PRIMARY KEY, "user_id" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "parent_id" bigint REFERENCES "categories" ("id") ON DELETE SET NULL, "name" text NOT NULL, "type" transaction_type NOT NULL, "created_at" timestamptz NOT NULL DEFAULT (now()) );
CREATE TABLE "transactions" ( "id" bigserial PRIMARY KEY, "user_id" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "account_id" bigint NOT NULL REFERENCES "accounts" ("id") ON DELETE RESTRICT, "category_id" bigint REFERENCES "categories" ("id") ON DELETE SET NULL, "amount" decimal(19, 4) NOT NULL, "type" transaction_type NOT NULL, "description" text, "transaction_date" timestamptz NOT NULL DEFAULT (now()), "linked_transaction_id" bigint REFERENCES "transactions" ("id") ON DELETE SET NULL );

/* ========= MODULE 2: FAST INPUT ========= */
CREATE TABLE "pending_transactions" ( "id" bigserial PRIMARY KEY, "user_id" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "source" text NOT NULL, "raw_text" text, "image_path" text, "suggested_amount" decimal(19, 4), "suggested_date" timestamptz, "status" text NOT NULL DEFAULT 'Pending', "split_id" bigint );

/* ========= MODULE 3: SOCIAL SPLITTING ========= */
CREATE TABLE "connections" ( "user_id_1" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "user_id_2" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "status" text NOT NULL DEFAULT 'Pending', PRIMARY KEY ("user_id_1", "user_id_2") );
CREATE TABLE "projects" ( "id" bigserial PRIMARY KEY, "owner_user_id" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "name" text NOT NULL, "status" text NOT NULL DEFAULT 'Open' );
CREATE TABLE "shared_expenses" ( "id" bigserial PRIMARY KEY, "project_id" bigint REFERENCES "projects" ("id") ON DELETE SET NULL, "payer_user_id" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "total_amount" decimal(19, 4) NOT NULL, "description" text, "date" timestamptz NOT NULL DEFAULT (now()) );
CREATE TABLE "expense_splits" ( "id" bigserial PRIMARY KEY, "shared_expense_id" bigint NOT NULL REFERENCES "shared_expenses" ("id") ON DELETE CASCADE, "ower_user_id" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "amount_owed" decimal(19, 4) NOT NULL, "status" text NOT NULL DEFAULT 'Pending' );
ALTER TABLE "pending_transactions" ADD FOREIGN KEY ("split_id") REFERENCES "expense_splits" ("id") ON DELETE CASCADE;

/* ========= MODULE 4: FORECASTING & PLANNING ========= */
CREATE TABLE "budgets" ( "id" bigserial PRIMARY KEY, "user_id" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "category_id" bigint NOT NULL REFERENCES "categories" ("id") ON DELETE CASCADE, "amount" decimal(19, 4) NOT NULL, "period" date NOT NULL, UNIQUE("user_id", "category_id", "period") );
CREATE TABLE "recurring_transactions" ( "id" bigserial PRIMARY KEY, "user_id" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "account_id" bigint NOT NULL REFERENCES "accounts" ("id") ON DELETE CASCADE, "category_id" bigint REFERENCES "categories" ("id") ON DELETE SET NULL, "amount" decimal(19, 4) NOT NULL, "description" text, "frequency" text NOT NULL, "start_date" date NOT NULL, "next_due_date" date NOT NULL );

/* ========= MODULE 5: LOAN MANAGEMENT ========= */
CREATE TABLE "loans" ( "id" bigserial PRIMARY KEY, "user_id" bigint NOT NULL REFERENCES "users" ("id") ON DELETE CASCADE, "name" text NOT NULL, "original_principal" decimal(19, 4) NOT NULL, "current_principal_balance" decimal(19, 4) NOT NULL, "interest_rate" decimal(5, 4) NOT NULL, "monthly_payment" decimal(19, 4) NOT NULL, "type" text NOT NULL, "total_months" integer NOT NULL, "months_paid" integer NOT NULL DEFAULT 0, "payment_due_day" integer NOT NULL );
CREATE TABLE "loan_payments" ( "id" bigserial PRIMARY KEY, "loan_id" bigint NOT NULL REFERENCES "loans" ("id") ON DELETE CASCADE, "transaction_id" bigint REFERENCES "transactions" ("id") ON DELETE SET NULL, "payment_date" timestamptz NOT NULL, "principal_paid" decimal(19, 4) NOT NULL, "interest_paid" decimal(19, 4) NOT NULL );

/* ========= INDEXES ========= */
CREATE INDEX ON "accounts" ("user_id");
CREATE INDEX ON "categories" ("user_id");
CREATE INDEX ON "transactions" ("user_id");
CREATE INDEX ON "transactions" ("account_id");
CREATE INDEX ON "transactions" ("category_id");
CREATE INDEX ON "pending_transactions" ("user_id");
CREATE INDEX ON "shared_expenses" ("payer_user_id");
CREATE INDEX ON "expense_splits" ("ower_user_id");
CREATE INDEX ON "budgets" ("user_id");
CREATE INDEX ON "recurring_transactions" ("user_id");
CREATE INDEX ON "loans" ("user_id");