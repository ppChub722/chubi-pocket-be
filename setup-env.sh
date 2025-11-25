#!/bin/bash
# FinaBBear Environment Setup Script for Linux/Mac

echo ""
echo "========================================"
echo "  FinaBBear Environment Setup"
echo "========================================"
echo ""

if [ -f .env ]; then
    read -p "[!] .env file already exists! Overwrite? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Setup cancelled."
        exit 0
    fi
fi

echo "Creating .env file from template..."
cp env.example .env

if [ $? -eq 0 ]; then
    echo "[+] .env file created successfully!"
    echo ""
    echo "========================================"
    echo "  IMPORTANT - Update Your Settings"
    echo "========================================"
    echo ""
    echo "Please edit .env file and update:"
    echo "  - DB_PASSWORD (if different)"
    echo "  - JWT_SECRET (REQUIRED for production)"
    echo ""
    echo "Default configuration:"
    echo "  - Database: localhost:5432/finna_bbear_db"
    echo "  - User: chubadmin"
    echo "  - Port: 8080"
    echo ""
    echo "Run 'make run' to start the application"
else
    echo "[-] Failed to create .env file"
    echo "Please manually copy env.example to .env"
fi

echo ""

