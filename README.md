# MiniMax2API

> OpenAI 兼容的 MiniMax AI 代理网关，支持多账号轮换、WebUI 管理、MiniMax 网页 Token 接入。

参考项目：[qwen2API](https://github.com/YuJunZhiXue/qwen2API) · [MiMo2API](https://github.com/Fly143/MiMo2API)

---

## 功能

- **OpenAI 兼容接口** — 完全兼容 `/v1/chat/completions`（流式 + 非流式），现有 OpenAI 客户端直连可用
- **双认证模式** — 支持 MiniMax 官方 API Key 和 agent.minimaxi.com 网页 Token
- **多账号轮换** — 多个 MiniMax 账号自动轮换调用，失败时指数退避冷却
- **模型名映射** — `gpt-4o`、`claude-sonnet-4`、`gemini-2.0-flash` 等自动路由到 MiniMax 模型
- **WebUI 管理面板** — 登录密码保护，含运行状态、账号管理、API Key 分发、接口测试、系统设置
- **使用统计** — 按代理 Key 和模型统计请求量与 Token 消耗
- **流式输出** — SSE 实时流式传输

---

## 快速开始

```bash
cd C:\Users\juang\Desktop\minimax
pip install -r requirements.txt
python main.py
```

打开 http://localhost:8000/

**WebUI 登录密码**：`minimax`

调用 API：

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Authorization: Bearer sk-minimax" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "你好"}]}'
```

---

## 配置

编辑 `config.json`：

```json
{
  "proxy_api_keys": ["sk-minimax"],
  "webui_password": "minimax",
  "default_model": "MiniMax-M2.7",
  "accounts": [...]
}
```

| 字段 | 说明 | 默认值 |
|---|---|---|
| `minimax_api_key` | 默认 MiniMax API Key（单账号时使用） | `""` |
| `minimax_base_url` | MiniMax API 地址 | `https://api.minimax.io/v1` |
| `proxy_api_keys` | 允许访问此网关的代理 Key 列表 | `["sk-minimax"]` |
| `webui_password` | WebUI 管理面板登录密码 | `"minimax"` |
| `default_model` | 默认模型名 | `"MiniMax-M2.7"` |
| `accounts` | 多账号配置列表 | `[]` |

### 环境变量

支持通过 `.env` 文件覆盖配置（参考 `.env.example`）：

| 变量 | 说明 |
|---|---|
| `PORT` | 监听端口（默认 8000） |
| `MINIMAX_API_KEY` | 默认 API Key |
| `MINIMAX_BASE_URL` | 默认 API 地址 |
| `DEFAULT_MODEL` | 默认模型 |
| `PROXY_API_KEYS` | 代理 Key 列表（逗号分隔） |

---

## 多账号配置

### 方式一：官方 API Key

```json
{
  "accounts": [
    {
      "name": "account-1",
      "api_key": "sk-your-minimax-api-key",
      "base_url": "https://api.minimax.io/v1",
      "auth_mode": "api_key"
    }
  ]
}
```

### 方式二：网页 Token（从 agent.minimaxi.com 获取）

打开 https://agent.minimaxi.com ，在浏览器控制台执行：

```javascript
console.log(localStorage._token)
```

将输出的 JWT 填入配置：

```json
{
  "accounts": [
    {
      "name": "web-account",
      "base_url": "https://agent.minimaxi.com/v1",
      "auth_mode": "token",
      "auth_token": "eyJhbGciOiJIUzI1NiIs..."
    }
  ]
}
```

---

## API 接口

| 端点 | 说明 |
|---|---|
| `POST /v1/chat/completions` | OpenAI 兼容对话（需 Bearer Auth） |
| `GET /v1/models` | 可用模型列表（需 Bearer Auth） |
| `GET /health` | 健康检查 |
| `POST /api/auth/login` | WebUI 登录 |
| `GET /api/config` | 获取配置 |
| `POST /api/config` | 更新配置 |
| `GET /api/accounts/status` | 账号运行状态 |
| `POST /api/test-account/{idx}` | 测试指定账号 |
| `GET /api/usage` | 使用统计 |

---

## 项目结构

```
minimax/
├── main.py               # FastAPI 服务入口
├── proxy.py              # API 代理核心（请求转发、账号轮换）
├── minimax_adapter.py    # MiniMax 网页 Token 适配器
├── config.py             # 配置管理 + 用量追踪
├── auth.py               # API Key 认证
├── models.py             # Pydantic 数据模型
├── config.json           # 持久化配置
├── requirements.txt      # 依赖
├── .env.example          # 环境变量模板
├── frontend/             # React 前端源码
│   ├── src/
│   │   ├── App.tsx           # 路由 + 登录保护
│   │   ├── layouts/          # AdminLayout（侧栏导航）
│   │   ├── pages/            # Dashboard, Accounts, Test, Tokens, Settings, Login
│   │   ├── components/       # 通用组件
│   │   └── lib/              # 工具（auth, api）
│   ├── index.html
│   └── vite.config.ts
├── static/               # 构建后的前端产物
└── README.md
```

---

## 模型映射

| 输入模型名 | 实际路由到 |
|---|---|
| `gpt-4o`, `gpt-4-turbo`, `claude-sonnet-4` | `MiniMax-M2.7` |
| `gpt-4o-mini`, `gpt-3.5-turbo`, `claude-3-haiku` | `MiniMax-M2.5-highspeed` |
| `gemini-2.0-flash`, `MiniMax-M2.7-highspeed` | `MiniMax-M2.7-highspeed` |
| 其他 MiniMax 原生模型名 | 透传不变 |

可在 WebUI「系统设置」→「模型映射规则」中自定义。
