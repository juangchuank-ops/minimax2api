"""Configuration management for MiniMax2API proxy.

Sources (priority high → low):
  1. Environment variables
  2. config.json file
  3. Built-in defaults

Enhancements from reference projects:
  - Multi-account support  (MiMo2API, qwen2API)
  - Usage tracking         (MiMo2API)
  - Model name mapping     (all three)
"""

import json
import os
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, List


# ── Model name mapping ──────────────────────────────────────────
MODEL_MAP: Dict[str, str] = {
    "gpt-4o": "MiniMax-M2.7",
    "gpt-4o-2024-08-06": "MiniMax-M2.7",
    "gpt-4o-mini": "MiniMax-M2.5-highspeed",
    "gpt-4-turbo": "MiniMax-M2.7",
    "gpt-4": "MiniMax-M2.7",
    "gpt-3.5-turbo": "MiniMax-M2.5-highspeed",
    "claude-sonnet-4-20250514": "MiniMax-M2.7",
    "claude-sonnet-4": "MiniMax-M2.7",
    "claude-3.5-sonnet": "MiniMax-M2.7",
    "claude-3-haiku": "MiniMax-M2.5-highspeed",
    "gemini-2.0-flash": "MiniMax-M2.7-highspeed",
    "gemini-1.5-pro": "MiniMax-M2.7",
    "MiniMax-M2.7": "MiniMax-M2.7",
    "MiniMax-M2.7-highspeed": "MiniMax-M2.7-highspeed",
    "MiniMax-M2.5": "MiniMax-M2.5",
    "MiniMax-M2.5-highspeed": "MiniMax-M2.5-highspeed",
}


def resolve_model(client_model: str) -> str:
    """Map client model name → MiniMax model (passthrough if unknown)."""
    return MODEL_MAP.get(client_model, client_model)


DEFAULT_MODELS = [
    "MiniMax-M2.7", "MiniMax-M2.7-highspeed",
    "MiniMax-M2.5", "MiniMax-M2.5-highspeed",
    "MiniMax-M2.1", "MiniMax-M2.1-highspeed",
    "MiniMax-M2",
]


# ── Account ─────────────────────────────────────────────────────
@dataclass
class Account:
    """A MiniMax account — can use API key or web auth token.

    ``auth_mode`` is either ``"api_key"`` (default) or ``"token"``.
    When ``"token"``, the ``auth_token`` field is used as Bearer
    credential and the base_url typically points at the web API
    (e.g. ``https://agent.minimaxi.com``).
    """
    api_key: str = ""
    name: str = ""
    base_url: str = "https://api.minimax.io/v1"
    auth_mode: str = "api_key"      # "api_key" | "token"
    auth_token: str = ""             # web session token (Bearer)
    is_active: bool = True
    request_count: int = 0
    last_used: float = 0.0
    cooldown_until: float = 0.0

    @property
    def on_cooldown(self) -> bool:
        return self.cooldown_until > time.time()

    def to_dict(self) -> dict:
        return {
            "api_key": self.api_key,
            "name": self.name,
            "base_url": self.base_url,
            "auth_mode": self.auth_mode,
            "auth_token": self.auth_token,
            "is_active": self.is_active,
            "request_count": self.request_count,
        }


# ── Usage stats ─────────────────────────────────────────────────
@dataclass
class UsageStats:
    requests: int = 0
    prompt_tokens: int = 0
    completion_tokens: int = 0

    @property
    def total_tokens(self) -> int:
        return self.prompt_tokens + self.completion_tokens

    def to_dict(self) -> dict:
        return {
            "requests": self.requests,
            "prompt_tokens": self.prompt_tokens,
            "completion_tokens": self.completion_tokens,
            "total_tokens": self.total_tokens,
        }


# ── Config ──────────────────────────────────────────────────────
@dataclass
class Config:
    minimax_api_key: str = ""
    minimax_base_url: str = "https://api.minimax.io/v1"
    proxy_api_keys: List[str] = field(default_factory=lambda: ["sk-default"])
    default_model: str = "MiniMax-M2.7"
    available_models: List[str] = field(default_factory=lambda: DEFAULT_MODELS.copy())
    webui_password: str = "minimax"
    accounts: List[dict] = field(default_factory=list)

    def to_dict(self) -> dict:
        return {
            "minimax_api_key": self.minimax_api_key,
            "minimax_base_url": self.minimax_base_url,
            "proxy_api_keys": self.proxy_api_keys,
            "default_model": self.default_model,
            "available_models": self.available_models,
            "webui_password": self.webui_password,
            "accounts": self.accounts,
        }


# ── Usage tracker ───────────────────────────────────────────────
class UsageTracker:
    """Thread-safe in-memory usage stats."""

    def __init__(self):
        self._lock = threading.RLock()
        self._by_key: Dict[str, UsageStats] = {}
        self._by_model: Dict[str, UsageStats] = {}

    def record(self, proxy_key: str, model: str, prompt: int = 0, completion: int = 0):
        with self._lock:
            if proxy_key not in self._by_key:
                self._by_key[proxy_key] = UsageStats()
            if model not in self._by_model:
                self._by_model[model] = UsageStats()
            self._by_key[proxy_key].requests += 1
            self._by_key[proxy_key].prompt_tokens += prompt
            self._by_key[proxy_key].completion_tokens += completion
            self._by_model[model].requests += 1
            self._by_model[model].prompt_tokens += prompt
            self._by_model[model].completion_tokens += completion

    def get_stats(self) -> dict:
        with self._lock:
            total_req = sum(s.requests for s in self._by_key.values())
            total_tok = sum(s.total_tokens for s in self._by_key.values())
            return {
                "total_requests": total_req,
                "total_tokens": total_tok,
                "by_key": {k: v.to_dict() for k, v in self._by_key.items()},
                "by_model": {k: v.to_dict() for k, v in self._by_model.items()},
            }


# ── Config Manager ──────────────────────────────────────────────
class ConfigManager:
    """Thread-safe config backed by JSON + env overrides."""

    def __init__(self, config_file: str = "config.json"):
        self.config_file = Path(config_file)
        self.config = Config()
        self._lock = threading.RLock()
        self._load()
        self._apply_env()

    def _load(self):
        if not self.config_file.exists():
            self._save()
            return
        try:
            data = json.loads(self.config_file.read_text("utf-8"))
            self.config = Config(
                minimax_api_key=data.get("minimax_api_key", ""),
                minimax_base_url=data.get("minimax_base_url", "https://api.minimax.io/v1"),
                proxy_api_keys=data.get("proxy_api_keys", ["sk-default"]),
                default_model=data.get("default_model", "MiniMax-M2.7"),
                available_models=data.get("available_models", DEFAULT_MODELS.copy()),
                webui_password=data.get("webui_password", "minimax"),
                accounts=data.get("accounts", []),
            )
        except Exception as exc:
            print(f"[Config] load error: {exc}")
            self.config = Config()
            self._save()

    def _save(self):
        with self._lock:
            self.config_file.write_text(
                json.dumps(self.config.to_dict(), indent=2, ensure_ascii=False), "utf-8"
            )

    def _apply_env(self):
        with self._lock:
            if v := os.environ.get("MINIMAX_API_KEY", "").strip():
                self.config.minimax_api_key = v
            if v := os.environ.get("MINIMAX_BASE_URL", "").strip():
                self.config.minimax_base_url = v
            if v := os.environ.get("DEFAULT_MODEL", "").strip():
                self.config.default_model = v
            if v := os.environ.get("PROXY_API_KEYS", "").strip():
                self.config.proxy_api_keys = [k.strip() for k in v.split(",") if k.strip()]

    # ── public API ─────────────────────────────────────────────

    def validate_proxy_key(self, key: str) -> bool:
        with self._lock:
            return key in self.config.proxy_api_keys

    def get_minimax_key(self) -> str:
        with self._lock:
            return self.config.minimax_api_key

    def get_base_url(self) -> str:
        with self._lock:
            return self.config.minimax_base_url

    def get_config(self) -> dict:
        with self._lock:
            d = self.config.to_dict()
            key = d.get("minimax_api_key", "")
            d["minimax_api_key_masked"] = (
                key[:8] + "..." + key[-4:] if len(key) > 12 else ("***" if key else "")
            )
            return d

    def update_config(self, new_cfg: dict):
        with self._lock:
            self.config = Config(
                minimax_api_key=new_cfg.get("minimax_api_key", self.config.minimax_api_key),
                minimax_base_url=new_cfg.get("minimax_base_url", self.config.minimax_base_url),
                proxy_api_keys=new_cfg.get("proxy_api_keys", self.config.proxy_api_keys),
                default_model=new_cfg.get("default_model", self.config.default_model),
                available_models=new_cfg.get("available_models", self.config.available_models),
                webui_password=new_cfg.get("webui_password", self.config.webui_password),
                accounts=new_cfg.get("accounts", self.config.accounts),
            )
            self._save()

    # ── account helpers ────────────────────────────────────────

    def get_accounts(self) -> List[Account]:
        with self._lock:
            if self.config.accounts:
                return [Account(**a) for a in self.config.accounts]
            if self.config.minimax_api_key:
                return [Account(
                    api_key=self.config.minimax_api_key,
                    name="default",
                    base_url=self.config.minimax_base_url,
                )]
            return []

    def save_accounts(self, accounts: List[Account]):
        with self._lock:
            self.config.accounts = [a.to_dict() for a in accounts]
            self._save()


# ── Global singletons ───────────────────────────────────────────
config_manager = ConfigManager()
usage_tracker = UsageTracker()
