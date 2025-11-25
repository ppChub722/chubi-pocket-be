.PHONY: help migrate-up migrate-down migrate-force db-status run

DB_URL := postgres://chubadmin:admin1234@localhost:5432/finna_bbear_db?sslmode=disable

help:
	@echo "FinaBBear Backend - Available Commands:"
	@echo "  make run           - Run the application"
	@echo "  make migrate-up    - Run database migrations"
	@echo "  make migrate-down  - Rollback last migration"
	@echo "  make db-status     - Check migration status"
	@echo "  make migrate-force - Force migration version (use: make migrate-force V=1)"

run:
	@echo "🐻 Starting FinaBBear..."
	go run cmd/api/main.go

migrate-up:
	@echo "⬆️  Running migrations..."
	migrate -path migrations -database "$(DB_URL)" up
	@echo "✅ Migrations completed!"

migrate-down:
	@echo "⬇️  Rolling back migration..."
	migrate -path migrations -database "$(DB_URL)" down 1

db-status:
	@echo "📊 Current migration status:"
	migrate -path migrations -database "$(DB_URL)" version

migrate-force:
ifndef V
	@echo "❌ Please specify version: make migrate-force V=1"
else
	@echo "⚠️  Forcing migration to version $(V)..."
	migrate -path migrations -database "$(DB_URL)" force $(V)
	@echo "✅ Migration version set to $(V)"
endif

