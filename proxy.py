"""API proxy layer — forwards OpenAI-format requests to MiniMax.

Handles:
  - Standard OpenAI-compatible API (api.minimax.io / api.minimaxi.com)
  - MiniMax web agent API (agent.minimaxi.com) via MiniMaxAdapter
  - Multi-account rotation with cooldown (MiMo2API / qwen2API)
  - Model name mapping
  - Test connection / model discovery
"""

import json
import logging
import time
import httpx
from typing import AsyncGenerator, Optional

from fastapi import HTTPException

from config import config_manager, usage_tracker, resolve_model, Account
from minimax_adapter import (
    parse_token,
    web_agent_chat,
    web_agent_chat_stream,
    test_adapter,
)

logger = logging.getLogger("minimax2api.proxy")


# ── Account helpers ──────────────────────────────────────────────

def _is_web_agent(acct: Account) -> bool:
    """Detect if an account uses the MiniMax web agent API."""
    return acct.auth_mode == "token" and "agent.minimaxi.com" in (acct.base_url or "")


def _get_web_agent_creds(acct: Account) -> tuple:
    """Extract (jwt_token, real_user_id) from a web agent account.

    Token comes from auth_token (preferred) or api_key (fallback).
    """
    raw = (acct.auth_token or acct.api_key or "").strip()
    return parse_token(raw)


def _pick_account() -> Optional[Account]:
    """Pick an available MiniMax account (round-robin with cooldown)."""
    accounts = config_manager.get_accounts()
    if not accounts:
        return None

    now = time.time()
    active = [a for a in accounts if a.is_active and not a.on_cooldown]
    if not active:
        active = sorted(accounts, key=lambda a: a.cooldown_until)
        if active:
            logger.warning("All accounts on cooldown; using soonest-recovering")
            return active[0]
        return None

    active.sort(key=lambda a: a.last_used)
    return active[0]


def _mark_used(acct: Account):
    acct.last_used = time.time()
    acct.request_count += 1


def _mark_failed(acct: Account, status: int):
    """Apply exponential-backoff cooldown on error."""
    backoff = min(60 * (2 ** min(acct.request_count, 5)), 600)
    acct.cooldown_until = time.time() + backoff
    acct.is_active = False
    logger.warning("Account '%s' cooldown %ds after HTTP %d", acct.name, backoff, status)


# ── Standard API helpers (OpenAI-compatible) ─────────────────────

def _build_headers(acct: Account) -> dict:
    """Build auth headers for standard OpenAI-compatible API."""
    token = acct.auth_token if acct.auth_mode == "token" and acct.auth_token else acct.api_key
    return {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
    }


def _build_payload(model: str, messages: list, params: dict) -> dict:
    """Build OpenAI-compatible payload for standard API."""
    resolved = resolve_model(model)
    payload: dict = {"model": resolved, "messages": messages}

    for field in ("temperature", "top_p", "max_tokens", "stream",
                  "tools", "tool_choice", "stop", "presence_penalty",
                  "frequency_penalty"):
        if field in params and params[field] is not None:
            if field == "temperature":
                payload["temperature"] = max(0.01, min(1.0, float(params[field])))
            else:
                payload[field] = params[field]

    if params.get("reasoning_split"):
        payload["reasoning_split"] = True

    extra = params.get("extra_body") or {}
    if extra:
        payload.update(extra)

    return payload


async def _execute_request(
    acct: Account, payload: dict, stream: bool = False
) -> httpx.Response:
    """Execute HTTP POST to standard API, raising on non-200."""
    url = f"{acct.base_url.rstrip('/')}/chat/completions"
    headers = _build_headers(acct)
    timeout = httpx.Timeout(connect=30.0, read=300.0, write=30.0, pool=30.0)

    async with httpx.AsyncClient(timeout=timeout) as client:
        resp = await client.post(url, headers=headers, json=payload)

    if resp.status_code != 200:
        detail = resp.text[:500]
        logger.warning("MiniMax error: HTTP %d — %.200s", resp.status_code, detail)
        _mark_failed(acct, resp.status_code)
        raise HTTPException(
            status_code=resp.status_code,
            detail={"error": {"message": f"MiniMax 上游错误: {detail}", "type": "upstream_error"}},
        )

    _mark_used(acct)
    return resp


# ── Public API: chat completion ──────────────────────────────────

async def proxy_chat(model: str, messages: list, params: dict, proxy_key: str) -> dict:
    """Non-streaming chat completion.

    Routes to MiniMax web agent adapter if the selected account
    uses ``auth_mode: "token"`` + ``agent.minimaxi.com`` base URL.
    """
    acct = _pick_account()
    if not acct:
        raise HTTPException(status_code=503, detail={
            "error": {"message": "没有可用的账号（已冷却）", "type": "unavailable"},
        })

    # ── Web agent path ──────────────────────────────────────────
    if _is_web_agent(acct):
        jwt_token, real_user_id = _get_web_agent_creds(acct)
        try:
            result = await web_agent_chat(model, messages, jwt_token, real_user_id)
            _mark_used(acct)
            usage_tracker.record(proxy_key, resolve_model(model), 0, 0)
            return result
        except Exception as e:
            _mark_failed(acct, 503)
            raise HTTPException(
                status_code=503,
                detail={"error": {"message": str(e), "type": "upstream_error"}},
            )

    # ── Standard API path ───────────────────────────────────────
    payload = _build_payload(model, messages, params)
    payload["stream"] = False
    resp = await _execute_request(acct, payload)
    data = resp.json()

    usage = data.get("usage", {})
    usage_tracker.record(proxy_key, payload["model"],
                         usage.get("prompt_tokens", 0),
                         usage.get("completion_tokens", 0))
    return data


async def proxy_chat_stream(
    model: str, messages: list, params: dict, proxy_key: str
) -> AsyncGenerator[str, None]:
    """SSE streaming chat completion.

    Routes to MiniMax web agent adapter if the selected account
    uses ``auth_mode: "token"`` + ``agent.minimaxi.com`` base URL.
    """
    acct = _pick_account()
    if not acct:
        yield f"data: {json.dumps({'error': {'message': '没有可用的账号（已冷却）', 'type': 'unavailable'}})}\n\n"
        yield "data: [DONE]\n\n"
        return

    # ── Web agent path ──────────────────────────────────────────
    if _is_web_agent(acct):
        jwt_token, real_user_id = _get_web_agent_creds(acct)
        try:
            async for chunk in web_agent_chat_stream(model, messages, jwt_token, real_user_id):
                yield chunk
            _mark_used(acct)
            usage_tracker.record(proxy_key, resolve_model(model), 0, 0)
        except Exception as e:
            _mark_failed(acct, 503)
            yield f"data: {json.dumps({'error': {'message': str(e), 'type': 'upstream_error'}})}\n\n"
            yield "data: [DONE]\n\n"
        return

    # ── Standard API path ───────────────────────────────────────
    payload = _build_payload(model, messages, params)
    payload["stream"] = True
    url = f"{acct.base_url.rstrip('/')}/chat/completions"
    headers = _build_headers(acct)
    timeout = httpx.Timeout(connect=30.0, read=300.0, write=30.0, pool=30.0)

    try:
        async with httpx.AsyncClient(timeout=timeout) as client:
            async with client.stream("POST", url, headers=headers, json=payload) as resp:
                if resp.status_code != 200:
                    body = await resp.aread()
                    _mark_failed(acct, resp.status_code)
                    yield f"data: {json.dumps({'error': {'message': body.decode()[:500], 'type': 'upstream_error'}})}\n\n"
                    yield "data: [DONE]\n\n"
                    return

                _mark_used(acct)
                prompt_tok = 0
                completion_tok = 0

                async for line in resp.aiter_lines():
                    if not line:
                        continue
                    if line.startswith("data: "):
                        data = line[6:]
                        if data.strip() == "[DONE]":
                            break
                        yield f"data: {data}\n\n"
                        try:
                            chunk = json.loads(data)
                            u = chunk.get("usage", {}) or {}
                            if u.get("total_tokens", 0) > 0:
                                prompt_tok = u.get("prompt_tokens", 0)
                                completion_tok = u.get("completion_tokens", 0)
                        except json.JSONDecodeError:
                            pass

                usage_tracker.record(proxy_key, payload["model"], prompt_tok, completion_tok)
                yield "data: [DONE]\n\n"

    except httpx.ConnectError as e:
        yield f"data: {json.dumps({'error': {'message': f'连接失败: {e}', 'type': 'upstream_connection_error'}})}\n\n"
        yield "data: [DONE]\n\n"
    except httpx.TimeoutException:
        yield f"data: {json.dumps({'error': {'message': '请求超时', 'type': 'upstream_timeout'}})}\n\n"
        yield "data: [DONE]\n\n"


# ── Test / status ────────────────────────────────────────────────

async def test_connection() -> dict:
    """Test with next available account."""
    acct = _pick_account()
    if not acct:
        return {"success": False, "error": "未配置账号"}

    return await _test_account(acct)


async def test_account_by_index(idx: int) -> dict:
    """Test a specific account by index."""
    accounts = config_manager.get_accounts()
    if idx < 0 or idx >= len(accounts):
        return {"success": False, "error": f"账号索引 {idx} 超出范围"}
    return await _test_account(accounts[idx])


async def _test_account(acct: Account) -> dict:
    """Run a minimal test request against a single account.

    Routes to MiniMax web agent adapter if applicable.
    """
    # ── Web agent path ──────────────────────────────────────────
    if _is_web_agent(acct):
        jwt_token, real_user_id = _get_web_agent_creds(acct)
        return await test_adapter(jwt_token, real_user_id)

    # ── Standard API path ───────────────────────────────────────
    payload = {
        "model": config_manager.config.default_model,
        "messages": [{"role": "user", "content": "Hi"}],
        "max_tokens": 16,
    }

    try:
        async with httpx.AsyncClient(timeout=15.0) as client:
            resp = await client.post(
                f"{acct.base_url.rstrip('/')}/chat/completions",
                headers=_build_headers(acct), json=payload,
            )
        if resp.status_code == 200:
            data = resp.json()
            return {
                "success": True,
                "model": data.get("model", payload["model"]),
                "response": (data.get("choices", [{}])[0]
                             .get("message", {})
                             .get("content", "")[:200]),
            }
        return {"success": False, "error": f"HTTP {resp.status_code}: {resp.text[:300]}"}
    except Exception as e:
        return {"success": False, "error": str(e)}


def get_accounts_status() -> list[dict]:
    """Return current runtime status for all accounts."""
    accounts = config_manager.get_accounts()
    result = []
    for a in accounts:
        result.append({
            "name": a.name,
            "is_active": a.is_active,
            "on_cooldown": a.on_cooldown,
            "request_count": a.request_count,
            "last_used": a.last_used,
            "auth_mode": a.auth_mode,
            "api_key_preview": (
                (a.api_key[:8] + "…" + a.api_key[-4:])
                if a.api_key and len(a.api_key) > 12
                else (a.api_key[:12] if a.api_key else "")
            ),
        })
    return result


async def fetch_models() -> dict:
    """Return available models in OpenAI format."""
    return {
        "object": "list",
        "data": [
            {"id": m, "object": "model", "created": 1681940951, "owned_by": "minimax"}
            for m in config_manager.config.available_models
        ],
    }
