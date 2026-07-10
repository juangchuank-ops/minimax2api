"""Region detection and base URL auto-selection for MiniMax API.

Automatically detects if running in China and suggests the appropriate base URL:
- China (CN): https://api.minimaxi.com/v1
- International: https://api.minimax.io/v1
"""

import logging
import time
from typing import Optional

import httpx

logger = logging.getLogger("minimax2api.region")

# Cache detection result for 1 hour
_detection_cache: Optional[str] = None
_detection_cache_ts: float = 0
_CACHE_TTL = 3600


def detect_region() -> str:
    """Detect current region and return recommended base URL.
    
    Returns:
        "cn" if in China, "intl" otherwise
    """
    global _detection_cache, _detection_cache_ts
    
    now = time.time()
    if _detection_cache and (now - _detection_cache_ts) < _CACHE_TTL:
        return _detection_cache
    
    # Try to detect by testing connectivity
    cn_url = "https://api.minimaxi.com/v1/models"
    intl_url = "https://api.minimax.io/v1/models"
    
    region = "intl"  # default
    
    try:
        # Test CN endpoint first (usually faster if in China)
        with httpx.Client(timeout=5.0) as client:
            try:
                resp = client.get(cn_url)
                if resp.status_code in (200, 401, 403):  # reachable
                    region = "cn"
                    logger.info("检测到中国区网络环境，使用 api.minimaxi.com")
            except (httpx.ConnectError, httpx.TimeoutException):
                # CN unreachable, try international
                try:
                    resp = client.get(intl_url)
                    if resp.status_code in (200, 401, 403):
                        region = "intl"
                        logger.info("检测到国际网络环境，使用 api.minimax.io")
                except (httpx.ConnectError, httpx.TimeoutException):
                    logger.warning("两个 API 端点均无法访问，使用默认国际端点")
                    region = "intl"
    except Exception as e:
        logger.warning("区域检测失败: %s，使用默认国际端点", e)
        region = "intl"
    
    _detection_cache = region
    _detection_cache_ts = now
    return region


def get_recommended_base_url() -> str:
    """Get recommended base URL based on region detection.
    
    Returns:
        Full base URL (e.g., "https://api.minimaxi.com/v1")
    """
    region = detect_region()
    if region == "cn":
        return "https://api.minimaxi.com/v1"
    return "https://api.minimax.io/v1"


def validate_base_url(base_url: str, timeout: float = 5.0) -> tuple[bool, Optional[str]]:
    """Validate if a base URL is accessible.
    
    Args:
        base_url: Base URL to validate (e.g., "https://api.minimax.io/v1")
        timeout: Request timeout in seconds
    
    Returns:
        (is_valid, error_message)
    """
    if not base_url:
        return False, "Base URL 为空"
    
    # Add /models if not present
    test_url = base_url.rstrip("/") + "/models"
    
    try:
        with httpx.Client(timeout=timeout) as client:
            resp = client.get(test_url)
            # 200/401/403 means endpoint is reachable (401/403 means auth required, which is OK)
            if resp.status_code in (200, 401, 403):
                return True, None
            return False, f"HTTP {resp.status_code}"
    except httpx.ConnectError as e:
        return False, f"连接失败: {e}"
    except httpx.TimeoutException:
        return False, "请求超时"
    except Exception as e:
        return False, f"未知错误: {e}"


def suggest_alternative_url(base_url: str) -> Optional[str]:
    """Suggest an alternative base URL if current one is not accessible.
    
    Args:
        base_url: Current base URL that's failing
    
    Returns:
        Alternative URL or None
    """
    if "minimaxi.com" in base_url:
        return "https://api.minimax.io/v1"
    elif "minimax.io" in base_url:
        return "https://api.minimaxi.com/v1"
    return None
