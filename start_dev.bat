@echo off
echo Building HO...
go generate
go build -o ho.exe . || (
    echo.
    echo Build failed! Press any key to close.
    pause >nul
    exit /b 1
)
echo.
echo Starting HO...
echo Admin panel: http://localhost:5001
echo Public site:  http://localhost:5000
echo.
ho.exe
echo.
echo HO has stopped. Press any key to close.
pause >nul
