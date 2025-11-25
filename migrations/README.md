# 📊 Database Migrations

This directory contains database migration files organized by module for better maintenance.

## 📁 Migration Structure

Migrations are organized into separate files for each module:

### **000001_create_types** - Core Types
- Creates ENUM types used across the application
- `account_type`: bank, cash, credit_card, e_wallet, investment, loan
- `transaction_type`: income, expense, transfer

### **000002_create_core_accounting** - Core Accounting Module
- **Users table** (with email and updated_at support)
- **Accounts table**
- **Categories table**
- **Transactions table**
- Auto-update trigger for `users.updated_at`
- Indexes for performance

### **000003_create_fast_input** - Fast Input Module
- **Pending transactions table**
- Indexes

### **000004_create_social_splitting** - Social Splitting Module
- **Connections table**
- **Projects table**
- **Shared expenses table**
- **Expense splits table**
- Foreign key from pending_transactions to expense_splits
- Indexes

### **000005_create_forecasting** - Forecasting & Planning Module
- **Budgets table**
- **Recurring transactions table**
- Indexes

### **000006_create_loan_management** - Loan Management Module
- **Loans table**
- **Loan payments table**
- Indexes

## 🚀 Running Migrations

### Apply All Migrations
```bash
# Windows
migrate.bat up

# Linux/Mac
make migrate-up
```

### Rollback Last Migration
```bash
# Windows
migrate.bat down

# Linux/Mac
make migrate-down
```

### Check Migration Status
```bash
# Windows
migrate.bat status

# Linux/Mac
make db-status
```

## 📋 Migration Dependencies

Migrations must run in order due to foreign key dependencies:

1. **Types** (000001) - No dependencies
2. **Core Accounting** (000002) - Depends on Types
3. **Fast Input** (000003) - Depends on Core Accounting (users)
4. **Social Splitting** (000004) - Depends on Core Accounting (users)
5. **Forecasting** (000005) - Depends on Core Accounting (users, accounts, categories)
6. **Loan Management** (000006) - Depends on Core Accounting (users, transactions)

## 🔄 Rollback Order

When rolling back, migrations must be dropped in reverse order:

1. Loan Management (000006)
2. Forecasting (000005)
3. Social Splitting (000004)
4. Fast Input (000003)
5. Core Accounting (000002)
6. Types (000001)

## ✨ Key Features

### Users Table Enhancements
- ✅ Added `email` column (required, unique)
- ✅ Added `updated_at` column (auto-updated via trigger)
- ✅ Auto-update trigger for `updated_at` on row updates

## 📝 Adding New Migrations

When adding new migrations:

1. **Create new migration files:**
   ```bash
   migrate create -ext sql -dir migrations -seq add_new_feature
   ```

2. **Follow naming convention:**
   - `000XXX_descriptive_name.up.sql`
   - `000XXX_descriptive_name.down.sql`

3. **Ensure proper order:**
   - Check dependencies
   - Use next sequential number

4. **Test both directions:**
   - Test `up` migration
   - Test `down` migration
   - Verify data integrity

## ⚠️ Important Notes

- **Never edit applied migrations** - Create new ones instead
- **Always test down migrations** - Ensure clean rollback
- **Keep migrations small** - One logical change per migration
- **Document breaking changes** - Add comments for complex changes
- **Test in development first** - Before applying to production

## 🔍 Migration File Naming

Format: `{version}_{description}.{direction}.sql`

- **Version:** Sequential number (000001, 000002, etc.)
- **Description:** Brief description of what the migration does
- **Direction:** `up` (apply) or `down` (rollback)

Examples:
- `000001_create_types.up.sql`
- `000001_create_types.down.sql`
- `000002_create_core_accounting.up.sql`
- `000002_create_core_accounting.down.sql`

---

**Last Updated:** November 14, 2025

