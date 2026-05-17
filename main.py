"""MiniMax2API — OpenAI-compatible proxy for MiniMax AI.

Routes:
  /v1/chat/completions   OpenAI-compatible chat completions
  /v1/models             Available models
  /admin/…               WebUI management console
  /admin/api/…           Management API (config, usage, accounts)
  /health                Health check
"""

import logging
import os
from contextlib import asynccontextmanager
from pathlib import Path

import uvicorn
from fastapi import FastAPI, Header, Request
from fastapi.responses import HTMLResponse, JSONResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from fastapi.middleware.cors import CORSMiddleware

from auth import extract_api_key
from config import config_manager, usage_tracker
from models import ChatCompletionRequest
from proxy import proxy_chat, proxy_chat_stream, test_connection, fetch_models, test_account_by_index, get_accounts_status

# ── logging ─────────────────────────────────────────────────────
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
logger = logging.getLogger("minimax2api")

# ── load .env ───────────────────────────────────────────────────
_env = Path(__file__).parent / ".env"
if _env.exists():
    for line in _env.read_text("utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, _, v = line.partition("=")
        k, v = k.strip(), v.strip().strip("\"'")
        if k and not os.environ.get(k):
            os.environ[k] = v

STATIC = Path(__file__).parent / "static"
ADMIN_STATIC = Path(__file__).parent / "admin_static"


# ── lifespan ────────────────────────────────────────────────────
@asynccontextmanager
async def lifespan(_app: FastAPI):
    port = int(os.environ.get("PORT", "8000"))
    logger.info("=" * 58)
    logger.info("  MiniMax2API — OpenAI-compatible proxy")
    logger.info("  API   : http://localhost:%d/v1/chat/completions", port)
    logger.info("  Admin : http://localhost:%d/admin/", port)
    logger.info("  Docs  : http://localhost:%d/docs", port)
    logger.info("=" * 58)
    yield


app = FastAPI(title="MiniMax2API", version="1.1.0", lifespan=lifespan)
app.add_middleware(CORSMiddleware, allow_origins=["*"], allow_credentials=True,
                   allow_methods=["*"], allow_headers=["*"])

# ── static files ────────────────────────────────────────────────
if STATIC.exists():
    app.mount("/static", StaticFiles(directory=str(STATIC)), name="static")
if ADMIN_STATIC.exists():
    app.mount("/admin/static", StaticFiles(directory=str(ADMIN_STATIC)), name="admin_static")


@app.get("/")
async def index():
    idx = STATIC / "index.html"
    if idx.exists():
        return HTMLResponse(idx.read_text("utf-8"))
    return JSONResponse({"error": "WebUI not found"})


@app.get("/admin")
@app.get("/admin/")
async def admin_index():
    idx = ADMIN_STATIC / "index.html"
    if idx.exists():
        return HTMLResponse(idx.read_text("utf-8"))
    return HTMLResponse("<h1>Admin UI not found</h1>", status_code=404)


# ── OpenAI-compatible API ───────────────────────────────────────

@app.post("/v1/chat/completions")
async def chat_completions(
    request: ChatCompletionRequest,
    authorization: str = Header(None),
    x_api_key: str = Header(None, alias="x-api-key"),
):
    proxy_key = extract_api_key(authorization, x_api_key)
    params = request.model_dump()
    messages = [m.model_dump(exclude_none=True) for m in request.messages]

    if request.stream:
        return StreamingResponse(
            proxy_chat_stream(request.model, messages, params, proxy_key),
            media_type="text/event-stream",
            headers={
                "Cache-Control": "no-cache",
                "X-Accel-Buffering": "no",
                "Connection": "keep-alive",
            },
        )

    result = await proxy_chat(request.model, messages, params, proxy_key)
    return JSONResponse(result)


@app.get("/v1/models")
async def list_models(
    authorization: str = Header(None),
    x_api_key: str = Header(None, alias="x-api-key"),
):
    extract_api_key(authorization, x_api_key)
    return JSONResponse(await fetch_models())


# ── WebUI Auth ──────────────────────────────────────────────────

from pydantic import BaseModel

class LoginRequest(BaseModel):
    password: str

@app.post("/api/auth/login")
async def webui_login(req: LoginRequest):
    if req.password == config_manager.config.webui_password:
        return {"success": True}
    return JSONResponse(
        {"success": False, "error": "密码错误"},
        status_code=401,
    )


# ── Management API ──────────────────────────────────────────────

@app.get("/api/config")
async def get_config():
    return JSONResponse(config_manager.get_config())


@app.post("/api/config")
async def update_config(req: Request):
    try:
        data = await req.json()
        config_manager.update_config(data)
        return JSONResponse({"status": "ok"})
    except Exception as e:
        return JSONResponse({"status": "error", "error": str(e)}, status_code=400)


@app.post("/api/test")
async def test_api():
    return JSONResponse(await test_connection())


@app.post("/api/test-account/{idx:int}")
async def test_account_route(idx: int):
    return JSONResponse(await test_account_by_index(idx))


@app.get("/api/accounts/status")
async def accounts_status():
    return JSONResponse(get_accounts_status())


@app.get("/api/models")
async def api_models():
    return JSONResponse(await fetch_models())


@app.get("/api/usage")
async def get_usage():
    return JSONResponse(usage_tracker.get_stats())


# ── Health ──────────────────────────────────────────────────────

@app.get("/health")
async def health():
    return {"status": "ok", "version": "1.1.0", "service": "minimax2api"}


# ── SPA fallback: let React Router handle unknown paths ─────────

@app.api_route("/{path:path}", methods=["GET"])
async def spa_fallback(path: str):
    # Don't catch API, static, or v1 routes
    if path.startswith(("api/", "v1/", "static/", "admin/", "health")):
        return JSONResponse({"error": "Not found"}, status_code=404)
    idx = STATIC / "index.html"
    if idx.exists():
        return HTMLResponse(idx.read_text("utf-8"))
    return JSONResponse({"error": "WebUI not found"}, status_code=404)


# ── entry point ─────────────────────────────────────────────────
def main():
    port = int(os.environ.get("PORT", "8000"))
    uvicorn.run(app, host="0.0.0.0", port=port, log_level="info")


if __name__ == "__main__":
    main()
