@echo off
setlocal
chcp 65001 >nul
rem ===========================================================
rem  MiniMax2API - Windows 启动脚本（局域网模式）
rem
rem  监听所有网卡，同网段的手机 / 平板 / 其他电脑都能连。
rem
rem  用法：
rem     start-minimax2api-lan.bat                  :: 默认端口 8080
rem     start-minimax2api-lan.bat 9000             :: 指定端口
rem     start-minimax2api-lan.bat 9000 你的密码    :: 指定端口 + 初始管理员密码
rem     start-minimax2api-lan.bat --rebuild        :: 先重新构建前后端再启动
rem
rem  警告：局域网模式下管理台会对整个网段开放：任何能访问到的人都只会
rem        看到登录页，但请务必给管理员账号设一个像样的密码，并且只在
rem        可信网络（家里的 Wi-Fi、公司内网）里这样跑。
rem ===========================================================

set "REBUILD=0"
set "PORT="
set "PASSWORD="

:parse_args
if "%~1"=="" goto args_done
if /i "%~1"=="--rebuild" ( set "REBUILD=1" & shift & goto parse_args )
if /i "%~1"=="-b"        ( set "REBUILD=1" & shift & goto parse_args )
if /i "%~1"=="--help"    ( call :usage & exit /b 0 )
if /i "%~1"=="-h"        ( call :usage & exit /b 0 )
if "%~1"=="--"           ( shift & goto parse_args )
if not defined PORT (
  set "PORT=%~1"
) else if not defined PASSWORD (
  set "PASSWORD=%~1"
)
shift
goto parse_args

:args_done
if not defined PORT set "PORT=8080"

rem --- 端口校验：必须全是数字，范围 1-65535 ---------------------------
set "PORT_STR=%PORT%"
set "PORT_BAD="
for /f "delims=0123456789" %%a in ("%PORT_STR%") do set "PORT_BAD=1"
if defined PORT_BAD goto bad_port

rem 去掉前导零，避免 set /a 把 0808 当八进制
set "P=%PORT_STR%"
:strip_zero
if not defined P goto bad_port
if not "%P:~0,1%"=="0" goto port_num
if "%P%"=="0" goto port_num
set "P=%P:~1%"
goto strip_zero
:port_num
set /a PORTNUM=%P% >nul 2>nul
if not defined PORTNUM goto bad_port
if %PORTNUM% LSS 1 goto bad_port
if %PORTNUM% GTR 65535 goto bad_port
set "PORT=%PORTNUM%"
goto after_port_check

:bad_port
echo [错误] 端口不合法：%PORT%（应为 1-65535 的数字）
echo        用法: start-minimax2api-lan.bat [端口] [初始管理员密码]
exit /b 1

:after_port_check

rem --- 解析脚本自身所在目录（双击 / 命令行 / 快捷方式都能work）---------
set "HERE=%~dp0"
set "BIN=%HERE%minimax2api.exe"
set "STATIC=%HERE%frontend\dist"
set "DATA=%HERE%data"
if defined MINIMAX2API_DATA set "DATA=%MINIMAX2API_DATA%"
set "BRIDGE_PORT=8766"

set "ADMIN_USER=%MINIMAX2API_ADMIN_USER%"
if not defined ADMIN_USER set "ADMIN_USER=admin"

if "%REBUILD%"=="1" goto do_rebuild
if not exist "%BIN%" (
  echo [提示] 未找到可执行文件，先编译一次。
  call :build_backend
  if errorlevel 1 exit /b 1
)
if not exist "%STATIC%\index.html" (
  echo [提示] 未找到前端产物，先构建一次。
  call :build_frontend
)
goto built

:do_rebuild
call :build_frontend
call :build_backend
if errorlevel 1 exit /b 1

:built
if not exist "%BIN%" (
  echo [错误] 可执行文件仍不存在：%BIN%
  exit /b 1
)

rem 数据目录与静态资源都用相对路径解析，所以必须先切到仓库根目录。
cd /d "%HERE%"

echo ===========================================================
echo   MiniMax2API - 局域网模式
echo.
echo   监听      : 0.0.0.0:%PORT%（所有网卡）
echo   本机管理台: http://127.0.0.1:%PORT%/
echo.
echo   [安全提醒] 管理台已对整个网段开放，请只在可信网络里这样跑，
echo              并给管理员账号设一个像样的密码。
echo.
echo   局域网访问地址（手机 / 其他电脑用这些）:
set "LAN_FOUND="
for /f "tokens=2 delims=:" %%a in ('ipconfig 2^>nul ^| findstr /r /c:"IPv4"') do (
  for /f "tokens=* delims= " %%b in ("%%a") do (
    echo     http://%%b:%PORT%/
    set "LAN_FOUND=1"
  )
)
if not defined LAN_FOUND echo     （没能自动识别局域网 IP，用 ipconfig 自己看一下）
echo.
echo   API 基地址就是把上面的端口后面换成 /v1
echo.
echo   数据目录  : %DATA%
if exist "%DATA%\app.json" (
  echo   管理员密码: 沿用 data\app.json 中已有的设置
) else if defined PASSWORD (
  echo   管理员密码: 使用你传入的密码（用户名 %ADMIN_USER%）
) else (
  echo   管理员密码: 首次启动随机生成，见下方日志
)
if not exist "%STATIC%\index.html" (
  echo.
  echo   [警告] 没有前端产物，本次只提供 API（无法打开管理台）
)
echo.
echo   其他设备连不上时依次检查：
echo     - 系统防火墙是否放行 %PORT% 端口
echo     - 两台设备是否真在同一个网段（访客 Wi-Fi 常常隔离）
echo     - 无线客户端的 AP 隔离（client isolation）是否打开
echo.
echo   关掉这个窗口服务就会停止，按 Ctrl+C 退出。
echo ===========================================================
echo.

rem --- 浏览器登录桥（可选，缺 node 时优雅降级）-------------------------
rem 桥驱动一个真实 Chrome 抓 MiniMax 登录后的 _token，支撑管理台的
rem 「浏览器登录」。桥起不来不影响主服务，只是那个菜单项会报「服务不可用」。
rem 注意：批处理没有好用的退出钩子，桥进程退出时不会被自动清理，
rem       需要时可在任务管理器里手动结束 node.exe。
where node >nul 2>nul
if errorlevel 1 (
  echo [提示] 没找到 node，跳过浏览器登录桥（管理台的「浏览器登录」会报服务不可用）。
  goto bridge_done
)
call :detect_chrome
start "minimax2api-browser-login-bridge" /b node "%HERE%tools\browser-login-bridge.mjs" --port %BRIDGE_PORT%
if defined CHROME_BIN (
  echo [提示] 浏览器登录桥已后台启动（127.0.0.1:%BRIDGE_PORT%，Chrome: %CHROME_BIN%）
) else (
  echo [提示] 浏览器登录桥已后台启动（127.0.0.1:%BRIDGE_PORT%，未找到 Chrome，用前请装 Chrome 或设 CHROME_BIN）
)
:bridge_done

set "MINIMAX2API_BROWSER_LOGIN_BRIDGE=http://127.0.0.1:%BRIDGE_PORT%"
if defined PASSWORD set "MINIMAX2API_ADMIN_PASSWORD=%PASSWORD%"

"%BIN%" -addr "0.0.0.0:%PORT%"
set "STATUS=%errorlevel%"

echo.
echo [服务已退出]
endlocal & exit /b %STATUS%

rem ===========================================================
rem  子过程
rem ===========================================================

:usage
echo 用法: start-minimax2api-lan.bat [选项] [端口] [初始管理员密码]
echo.
echo 选项:
echo   -b, --rebuild   启动前重新构建前端与后端
echo   -h, --help      显示本帮助
echo.
echo 说明:
echo   端口默认 8080，监听 0.0.0.0（所有网卡）。
echo   初始管理员密码只在首次启动（data\app.json 不存在）时生效；
echo   之后密码以 data\app.json 里保存的为准。
echo   不传密码时首次启动会随机生成一个，并打印在下方日志里。
goto :eof

:detect_chrome
if defined CHROME_BIN goto :eof
if exist "%ProgramFiles%\Google\Chrome\Application\chrome.exe" set "CHROME_BIN=%ProgramFiles%\Google\Chrome\Application\chrome.exe"
if exist "%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe" set "CHROME_BIN=%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe"
if exist "%LocalAppData%\Google\Chrome\Application\chrome.exe" set "CHROME_BIN=%LocalAppData%\Google\Chrome\Application\chrome.exe"
goto :eof

:build_backend
where go >nul 2>nul
if errorlevel 1 (
  echo [错误] 没找到 Go 工具链，无法编译后端。
  echo        需要 Go 1.24+。装好后重跑本脚本，或手动编译：
  echo          cd backend
  echo          go build -o ..\minimax2api.exe .\cmd\minimax2api
  exit /b 1
)
echo [构建] 后端 -^> %BIN%
pushd "%HERE%backend"
go build -o "..\minimax2api.exe" ".\cmd\minimax2api"
set "BUILD_RC=%errorlevel%"
popd
if not "%BUILD_RC%"=="0" (
  echo [错误] 后端编译失败。
  exit /b 1
)
exit /b 0

:build_frontend
where npm >nul 2>nul
if errorlevel 1 (
  echo [警告] 没找到 npm，跳过前端构建，服务将只提供 API。
  exit /b 1
)
echo [构建] 前端 -^> %STATIC%
pushd "%HERE%frontend"
if not exist "node_modules" (
  echo [构建] 首次运行，安装前端依赖...
  call npm install --no-audit --no-fund
  if errorlevel 1 (
    popd
    echo [警告] 前端依赖安装失败，界面可能不是最新的。
    exit /b 1
  )
)
call npm run build
if errorlevel 1 (
  popd
  echo [警告] 前端构建失败，界面可能不是最新的。
  exit /b 1
)
popd
exit /b 0
