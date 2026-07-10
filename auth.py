"""API key extraction and validation for incoming requests."""

from fastapi import Header, HTTPException

from config import config_manager


def extract_api_key(
    authorization: str = Header(None),
    x_api_key: str = Header(None, alias="x-api-key"),
) -> str:
    """Extract and validate proxy API key from request headers.

    Supports ``Authorization: Bearer <key>`` and ``X-API-Key: <key>``.
    Returns the validated key string or raises 401.
    """
    key = None
    if authorization:
        key = authorization.replace("Bearer ", "").strip()
    elif x_api_key:
        key = x_api_key.strip()

    if not key:
        raise HTTPException(
            status_code=401,
            detail={
                "error": {
                    "message": "缺少 API 密钥。请通过 'Authorization: Bearer <key>' 或 'X-API-Key: <key>' 提供。",
                    "type": "authentication_error",
                }
            },
        )

    if not config_manager.validate_proxy_key(key):
        raise HTTPException(
            status_code=401,
            detail={
                "error": {
                    "message": "无效的 API 密钥。请在设置页面中配置正确的代理密钥。",
                    "type": "authentication_error",
                }
            },
        )

    return key
