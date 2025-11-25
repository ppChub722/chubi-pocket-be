@echo off
REM Test FinaBBear API endpoints

echo Testing FinaBBear API...
echo.

echo [1] Testing API Info endpoint (GET /)
curl -s http://localhost:8080/
echo.
echo.

echo [2] Testing Health Check endpoint (GET /ping)
curl -s http://localhost:8080/ping
echo.
echo.

echo Done!
pause

