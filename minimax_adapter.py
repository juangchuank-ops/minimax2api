"""MiniMax Web Agent API Adapter.

Translates OpenAI-compatible requests into MiniMax's web agent API
(agent.minimaxi.com) and back, handling device registration,
request signing, message sending, and response polling.

Based on Chat2API / MiniMax-Free-API implementations.
"""

import hashlib
import json
import logging
import time
import uuid as uuid_mod
from typing import AsyncGenerator, Optional

import httpx

logger = logging.getLogger("minimax2api.adapter")

# ── Constants ────────────────────────────────────────────────────

AGENT_BASE_URL = "https://agent.minimaxi.com"

FAKE_HEADERS = {
    "Accept": "application/json, text/plain, */*",
    "Accept-Encoding": "gzip, deflate, br, zstd",
    "Accept-Language": "zh-CN,zh;q=0.9",
    "Cache-Control": "no-cache",
    "Origin": "https://agent.minimaxi.com",
    "Pragma": "no-cache",
    "Sec-Fetch-Dest": "empty",
    "Sec-Fetch-Mode": "cors",
    "Sec-Fetch-Site": "same-origin",
    "User-Agent": (
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
        "AppleWebKit/537.36 (KHTML, like Gecko) "
        "Chrome/142.0.0.0 Safari/537.36"
    ),
}

FAKE_USER_DATA = {
    "device_platform": "web",
    "biz_id": "3",
    "app_id": "3001",
    "version_code": "22201",
    "os_name": "Mac",
    "browser_name": "chrome",
    "device_memory": 8,
    "cpu_core_num": 11,
    "browser_language": "zh-CN",
    "browser_platform": "MacIntel",
    "screen_width": 1920,
    "screen_height": 1080,
    "lang": "zh",
    "timezone_offset": 28800,
    "sys_language": "zh",
    "client": "web",
}

DEVICE_INFO_TTL = 10800  # seconds

# ── Device info cache ────────────────────────────────────────────

_device_cache: dict = {}
_device_cache_ts: dict = {}


def _md5(s: str) -> str:
    return hashlib.md5(s.encode("utf-8")).hexdigest()


def _unix() -> int:
    return int(time.time())


def _unix_ms() -> str:
    return str(int(time.time() * 1000))


def _uuid() -> str:
    return str(uuid_mod.uuid4())


def _parse_jwt_user_id(jwt_token: str) -> str:
    """Extract user.id from a MiniMax JWT token payload."""
    try:
        parts = jwt_token.split(".")
        if len(parts) != 3:
            return ""
        payload_b64 = parts[1]
        padding = 4 - (len(payload_b64) % 4)
        if padding != 4:
            payload_b64 += "=" * padding
        import base64
        decoded = base64.b64decode(payload_b64).decode("utf-8")
        payload = json.loads(decoded)
        return payload.get("user", {}).get("id", "")
    except Exception:
        return ""


# ── Request signing ──────────────────────────────────────────────

def _build_query_string(jwt_token: str, real_user_id: str,
                        device_id: str = "", uuid_val: str = "",
                        use_random_uuid: bool = False) -> str:
    """Build the query string from FAKE_USER_DATA plus runtime values.

    For device registration, ``uuid`` should be a random UUID.
    For regular API calls, ``uuid`` should be ``real_user_id``
    (matching Chat2API's implementation).
    """
    ts = _unix_ms()
    data = dict(FAKE_USER_DATA)
    if use_random_uuid:
        data["uuid"] = uuid_val or _uuid()
    else:
        data["uuid"] = real_user_id
    data["user_id"] = real_user_id
    data["unix"] = ts
    data["token"] = jwt_token
    if device_id:
        data["device_id"] = device_id
    return "&".join(f"{k}={v}" for k, v in data.items() if v is not None)


def _compute_yy(full_uri_with_qs: str, data_json: str) -> str:
    """Compute the 'yy' header value."""
    from urllib.parse import quote
    unix_ms_val = _unix_ms()
    encoded = quote(full_uri_with_qs, safe="")
    return _md5(f"{encoded}_{data_json}{_md5(unix_ms_val)}ooui")


def _compute_signature(timestamp: int, jwt_token: str, data_json: str) -> str:
    """Compute the 'x-signature' header value."""
    return _md5(f"{timestamp}{jwt_token}{data_json}")


def _build_signed_headers(
    jwt_token: str, real_user_id: str, request_body: dict,
    path: str, device_id: str = "", uuid_val: str = "",
) -> dict:
    """Build all headers for a signed MiniMax API request."""
    ts = _unix()
    ts_ms = _unix_ms()
    data_json = json.dumps(request_body, separators=(",", ":"), ensure_ascii=False)

    qs = _build_query_string(jwt_token, real_user_id, device_id, uuid_val)
    full_uri = f"{path}?{qs}" if qs else path
    yy = _compute_yy(full_uri, data_json)
    sig = _compute_signature(ts, jwt_token, data_json)

    return {
        **FAKE_HEADERS,
        "Content-Type": "application/json",
        "Referer": "https://agent.minimaxi.com/",
        "token": jwt_token,
        "x-timestamp": str(ts),
        "x-signature": sig,
        "yy": yy,
    }


# ── Token parsing ────────────────────────────────────────────────

def parse_token(raw_token: str) -> tuple:
    """Parse a MiniMax web agent token.

    Returns (jwt_token, real_user_id).
    Token format can be ``realUserID+JWTtoken`` or just ``JWTtoken``.
    """
    if "+" in raw_token:
        parts = raw_token.split("+", 1)
        return parts[1].strip(), parts[0].strip()

    # Plain JWT — parse user ID from payload
    uid = _parse_jwt_user_id(raw_token)
    return raw_token.strip(), uid


# ── Device registration ──────────────────────────────────────────

async def register_device(
    jwt_token: str, real_user_id: str,
    client: httpx.AsyncClient,
) -> dict:
    """Register a device with MiniMax and return device info.

    Caches by (jwt_token + real_user_id) for DEVICE_INFO_TTL seconds.
    """
    cache_key = f"{jwt_token}:{real_user_id}"
    now = _unix()
    if cache_key in _device_cache and (now - _device_cache_ts.get(cache_key, 0)) < DEVICE_INFO_TTL:
        return _device_cache[cache_key]

    random_uuid = _uuid()
    body = {"uuid": random_uuid}
    ts = _unix()
    data_json = json.dumps(body, separators=(",", ":"))
    qs = _build_query_string(jwt_token, real_user_id, uuid_val=random_uuid, use_random_uuid=True)
    path = f"/v1/api/user/device/register?{qs}"
    yy = _compute_yy(path, data_json)
    sig = _compute_signature(ts, jwt_token, data_json)

    headers = {
        **FAKE_HEADERS,
        "Content-Type": "application/json",
        "Referer": "https://agent.minimaxi.com/",
        "token": jwt_token,
        "x-timestamp": str(ts),
        "x-signature": sig,
        "yy": yy,
    }

    resp = await client.post(
        f"{AGENT_BASE_URL}{path}",
        json=body,
        headers=headers,
        timeout=15.0,
    )

    if resp.status_code != 200:
        logger.warning("Device registration failed: HTTP %d — %.200s",
                       resp.status_code, resp.text[:200])
        raise RuntimeError(f"MiniMax device registration failed: HTTP {resp.status_code}")

    data = resp.json()
    status = data.get("statusInfo", {})
    if status.get("code") != 0:
        raise RuntimeError(f"MiniMax device registration rejected: {status.get('message', 'unknown')}")

    result = {
        "deviceId": data.get("data", {}).get("deviceIDStr", ""),
        "realUserID": data.get("data", {}).get("realUserID", real_user_id),
        "uuid": random_uuid,
    }
    _device_cache[cache_key] = result
    _device_cache_ts[cache_key] = now
    logger.info("Device registered: deviceId=%.16s realUserID=%s",
                result["deviceId"], result["realUserID"])
    return result


# ── Build message payload ────────────────────────────────────────

def _build_msg_payload(messages: list, chat_id: str = "") -> dict:
    """Convert OpenAI-format messages to MiniMax send_msg format.

    Uses the format from Chat2API/MiniMax-Free-API.
    """
    system_text = ""
    other_msgs = []

    for m in messages:
        content = ""
        if isinstance(m.get("content"), str):
            content = m["content"]
        elif isinstance(m.get("content"), list):
            texts = [p.get("text", "") for p in m["content"] if p.get("type") == "text"]
            content = "\n".join(texts)
        if not content:
            continue

        if m.get("role") == "system":
            system_text = content
        else:
            other_msgs.append(f"{m['role']}:{content}")

    # Build text in Chat2API format
    text_parts = []
    if system_text:
        text_parts.append(f"system:{system_text}")
    text_parts.extend(other_msgs)

    # Only add assistant prompt if there are 2+ messages
    if len(other_msgs) >= 2:
        text_parts.append("assistant:\n")

    text = "\n".join(text_parts)
    if not text:
        text = "hi"

    payload = {
        "msg_type": 1,
        "text": text,
        "chat_type": 1,
        "attachments": [],
        "selected_mcp_tools": [],
        "backend_config": {},
        "sub_agent_ids": [],
    }
    if chat_id:
        payload["chat_id"] = chat_id
    return payload


# ── Send message ─────────────────────────────────────────────────

async def _send_message(
    jwt_token: str, real_user_id: str, device_info: dict,
    payload: dict, client: httpx.AsyncClient,
) -> dict:
    """Send a message and return (chat_id, msg_id)."""
    path = "/matrix/api/v1/chat/send_msg"
    qs = _build_query_string(jwt_token, real_user_id,
                              device_info.get("deviceId", ""),
                              device_info.get("uuid", ""))
    url = f"{AGENT_BASE_URL}{path}?{qs}"
    headers = _build_signed_headers(
        jwt_token, real_user_id, payload, path,
        device_id=device_info.get("deviceId", ""),
        uuid_val=device_info.get("uuid", ""),
    )
    resp = await client.post(
        url,
        json=payload,
        headers=headers,
        timeout=15.0,
    )
    if resp.status_code != 200:
        detail = resp.text[:300]
        logger.warning("Send message failed: HTTP %d — %.200s",
                       resp.status_code, detail)
        raise RuntimeError(f"MiniMax send message failed: HTTP {resp.status_code}: {detail}")

    data = resp.json()
    br = data.get("base_resp", {})
    if br.get("status_code") != 0:
        msg = br.get("status_msg", "unknown")
        raise RuntimeError(f"MiniMax send message rejected: {msg}")

    return {
        "chat_id": data.get("chat_id", ""),
        "msg_id": data.get("msg_id", ""),
    }


# ── Poll for response ────────────────────────────────────────────

async def _poll_response(
    chat_id: str,
    jwt_token: str, real_user_id: str, device_info: dict,
    client: httpx.AsyncClient,
    max_polls: int = 240,
    poll_interval: float = 0.5,
) -> dict:
    """Poll get_chat_detail until AI response is ready.

    Returns the first message with msg_type=2 (assistant).
    Raises RuntimeError on timeout.
    """
    body = {"chat_id": chat_id}
    path = "/matrix/api/v1/chat/get_chat_detail"

    for i in range(max_polls):
        await _async_sleep(poll_interval)

        qs = _build_query_string(jwt_token, real_user_id,
                                  device_info.get("deviceId", ""),
                                  device_info.get("uuid", ""))
        url = f"{AGENT_BASE_URL}{path}?{qs}"
        headers = _build_signed_headers(
            jwt_token, real_user_id, body, path,
            device_id=device_info.get("deviceId", ""),
            uuid_val=device_info.get("uuid", ""),
        )
        resp = await client.post(
            url,
            json=body,
            headers=headers,
            timeout=15.0,
        )
        if resp.status_code != 200:
            if i == 0:
                logger.warning("Poll HTTP %d: %.100s", resp.status_code, resp.text[:100])
            continue

        data = resp.json()
        br = data.get("base_resp", {})
        if br.get("status_code") != 0:
            if i == 0:
                logger.warning("Poll base_resp: %s", br)
            continue

        messages = data.get("messages", [])
        ai_msgs = [m for m in messages if m.get("msg_type") == 2]
        if not ai_msgs:
            continue

        ai_msg = ai_msgs[-1]
        if ai_msg.get("msg_content"):
            logger.info("AI response after %d polls", i + 1)
            return ai_msg

    raise RuntimeError("No AI response after polling timeout")


async def _async_sleep(seconds: float):
    """Async sleep without asyncio import at top level."""
    import asyncio
    await asyncio.sleep(seconds)


# ── Convert response to OpenAI format ────────────────────────────

def _to_openai_response(ai_msg: dict, model: str, chat_id: str) -> dict:
    """Convert a MiniMax AI message dict to OpenAI chat completion format."""
    content = ai_msg.get("msg_content", "")
    thinking = ai_msg.get("extra_info", {}).get("thinking_content", "")

    choice = {
        "index": 0,
        "message": {"role": "assistant", "content": content},
        "finish_reason": "stop",
    }
    if thinking:
        choice["message"]["reasoning_content"] = thinking

    return {
        "id": f"chatcmpl-{chat_id}",
        "object": "chat.completion",
        "created": int(time.time()),
        "model": model,
        "choices": [choice],
        "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
    }


# ── Public API: non-streaming ────────────────────────────────────

async def web_agent_chat(
    model: str, messages: list,
    jwt_token: str, real_user_id: str,
) -> dict:
    """Non-streaming chat via MiniMax web agent API.

    Returns OpenAI-compatible response dict.
    """
    async with httpx.AsyncClient(timeout=30.0) as client:
        # 1. Register device
        device_info = await register_device(jwt_token, real_user_id, client)

        # 2. Build & send message
        payload = _build_msg_payload(messages)
        result = await _send_message(
            jwt_token, real_user_id, device_info, payload, client,
        )
        chat_id = result["chat_id"]

        # 3. Poll for response
        ai_msg = await _poll_response(
            chat_id, jwt_token, real_user_id, device_info, client,
        )

    return _to_openai_response(ai_msg, model, chat_id)


# ── Public API: streaming (polling-based) ────────────────────────

async def web_agent_chat_stream(
    model: str, messages: list,
    jwt_token: str, real_user_id: str,
) -> AsyncGenerator[str, None]:
    """Streaming chat via MiniMax web agent API.

    Yields OpenAI-compatible SSE chunks.
    """
    import asyncio

    chat_id = ""
    device_info = None
    ai_msg = None

    try:
        async with httpx.AsyncClient(timeout=30.0) as client:
            # 1. Register device
            device_info = await register_device(jwt_token, real_user_id, client)

            # 2. Send message
            payload = _build_msg_payload(messages)
            result = await _send_message(
                jwt_token, real_user_id, device_info, payload, client,
            )
            chat_id = result["chat_id"]

            # 3. Poll with incremental content delivery
            last_content = ""
            last_thinking = ""
            body = {"chat_id": chat_id}
            path = "/matrix/api/v1/chat/get_chat_detail"
            max_polls = 180
            sent_role = False

            for poll_i in range(max_polls):
                await asyncio.sleep(0.5)

                qs = _build_query_string(jwt_token, real_user_id,
                                          device_info.get("deviceId", ""),
                                          device_info.get("uuid", ""))
                poll_url = f"{AGENT_BASE_URL}{path}?{qs}"
                headers = _build_signed_headers(
                    jwt_token, real_user_id, body, path,
                    device_id=device_info.get("deviceId", ""),
                    uuid_val=device_info.get("uuid", ""),
                )
                resp = await client.post(
                    poll_url,
                    json=body,
                    headers=headers,
                    timeout=15.0,
                )
                if resp.status_code != 200:
                    continue
                data = resp.json()
                br = data.get("base_resp", {})
                if br.get("status_code") != 0:
                    continue

                chat_data = data.get("chat", {})
                chat_status = chat_data.get("chat_status", 0)
                messages_list = data.get("messages", [])
                ai_msgs = [m for m in messages_list if m.get("msg_type") == 2]

                if not ai_msgs:
                    continue

                current = ai_msgs[-1]
                current_content = current.get("msg_content", "") or ""
                current_thinking = (
                    current.get("extra_info", {}).get("thinking_content", "") or ""
                )

                # Emit thinking delta
                if current_thinking and len(current_thinking) > len(last_thinking):
                    delta = current_thinking[len(last_thinking):]
                    if delta.strip():
                        if not sent_role:
                            yield _sse_chunk(chat_id, model, {"role": "assistant"})
                            sent_role = True
                        yield _sse_chunk(chat_id, model,
                                         {"reasoning_content": delta})
                    last_thinking = current_thinking

                # Emit content delta
                if current_content and len(current_content) > len(last_content):
                    delta = current_content[len(last_content):]
                    if delta:
                        if not sent_role:
                            yield _sse_chunk(chat_id, model, {"role": "assistant"})
                            sent_role = True
                        yield _sse_chunk(chat_id, model, {"content": delta})
                    last_content = current_content

                # Check completion
                if chat_status == 2 and current_content:
                    yield _sse_chunk(chat_id, model, {},
                                     finish_reason="stop")
                    ai_msg = current
                    break

    except Exception as e:
        logger.error("Streaming error: %s", e)
        yield f"data: {json.dumps({'error': {'message': str(e), 'type': 'upstream_error'}})}\n\n"

    yield "data: [DONE]\n\n"


def _sse_chunk(chat_id: str, model: str, delta: dict,
               finish_reason: Optional[str] = None) -> str:
    """Build an SSE data chunk in OpenAI format."""
    chunk = {
        "id": f"chatcmpl-{chat_id}",
        "object": "chat.completion.chunk",
        "created": int(time.time()),
        "model": model,
        "choices": [{
            "index": 0,
            "delta": delta,
            "finish_reason": finish_reason,
        }],
    }
    return f"data: {json.dumps(chunk, ensure_ascii=False)}\n\n"


# ── Test ─────────────────────────────────────────────────────────

async def test_adapter(jwt_token: str, real_user_id: str) -> dict:
    """Test the MiniMax web agent adapter with a simple request."""
    try:
        async with httpx.AsyncClient(timeout=30.0) as client:
            device_info = await register_device(jwt_token, real_user_id, client)
            payload = _build_msg_payload([{"role": "user", "content": "Hi"}])
            result = await _send_message(
                jwt_token, real_user_id, device_info, payload, client,
            )
            ai_msg = await _poll_response(
                result["chat_id"], jwt_token, real_user_id, device_info, client,
                max_polls=120, poll_interval=0.5,
            )
        return {
            "success": True,
            "response": (ai_msg.get("msg_content", "") or "")[:200],
            "chat_id": result["chat_id"],
        }
    except Exception as e:
        return {"success": False, "error": str(e)}
