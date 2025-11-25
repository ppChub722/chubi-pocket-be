@echo off
REM FinaBBear Environment Setup Script for Windows

echo.
echo ========================================
echo   FinaBBear Environment Setup
echo ========================================
echo.

if exist .env (
    echo [!] .env file already exists!
    set /p "OVERWRITE=Do you want to overwrite it? (y/N): "
    if /i not "%OVERWRITE%"=="y" (
        echo Setup cancelled.
        goto :end
    )
)

echo Creating .env file from template...
copy env.example .env >nul

if %ERRORLEVEL% EQU 0 (
    echo [+] .env file created successfully!
    echo.
    echo ========================================
    echo   IMPORTANT - Update Your Settings
    echo ========================================
    echo.
    echo Please edit .env file and update:
    echo   - DB_PASSWORD (if different)
    echo   - JWT_SECRET (REQUIRED for production)
    echo.
    echo Default configuration:
    echo   - Database: localhost:5432/finna_bbear_db
    echo   - User: chubadmin
    echo   - Port: 8080
    echo.
    echo Run 'run.bat' to start the application
) else (
    echo [-] Failed to create .env file
    echo Please manually copy env.example to .env
)

:end
echo.
pause

