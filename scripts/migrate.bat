@echo off
REM FinaBBear Database Migration Script for Windows

set DB_URL=postgres://chubadmin:admin1234@localhost:5432/finna_bbear_db?sslmode=disable

if "%1"=="" goto help
if "%1"=="up" goto up
if "%1"=="down" goto down
if "%1"=="status" goto status
if "%1"=="force" goto force
goto help

:help
echo FinaBBear Backend - Database Migration Commands:
echo   migrate.bat up      - Run database migrations
echo   migrate.bat down    - Rollback last migration
echo   migrate.bat status  - Check migration status
echo   migrate.bat force N - Force migration to version N
goto end

:up
echo Running migrations...
migrate -path migrations -database "%DB_URL%" up
echo Migrations completed!
goto end

:down
echo Rolling back migration...
migrate -path migrations -database "%DB_URL%" down 1
goto end

:status
echo Current migration status:
migrate -path migrations -database "%DB_URL%" version
goto end

:force
if "%2"=="" (
    echo Error: Please specify version number
    echo Usage: migrate.bat force 1
    goto end
)
echo Forcing migration to version %2...
migrate -path migrations -database "%DB_URL%" force %2
echo Migration version set to %2
goto end

:end

