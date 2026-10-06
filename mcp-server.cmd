@echo off
rem Claude Mnemonic MCP server on Windows: run the installed binary. The binaries are fetched first when they are
rem missing (this waits: the server cannot start without them). A batch file hands its standard input and output to
rem the server unchanged, which MCP needs; PowerShell is used for the download only, and never touches stdout.
set "BIN=%USERPROFILE%\.claude-mnemonic\bin\mcp-server.exe"
if not exist "%BIN%" (
    if exist "%~dp0lib\ensure-binaries.ps1" (
        powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "%~dp0lib\ensure-binaries.ps1" 1>&2
    )
)
if exist "%BIN%" (
    "%BIN%" %*
    exit /b %ERRORLEVEL%
)
echo claude-mnemonic: mcp-server.exe not found at %BIN% and the download did not succeed (see above). 1>&2
echo claude-mnemonic: it needs Windows on x86-64 and network access to github.com. See %USERPROFILE%\.claude-mnemonic\plugin-install.log 1>&2
exit /b 1
