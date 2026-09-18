# MiniMax2API

把 [MiniMax Agent](https://agent.minimax.io/) 的 Web 端能力封装成 **OpenAI 兼容 API**，并配一套完整的**管理台 + 号池调度**。

前端界面参考 [grok2api](https://github.com/chenyme/grok2api) 的设计语言实现（React 19 + Vite + Tailwind 4，自建零依赖 shadcn 风格组件）。后端是纯 Go 标准库，无第三方依赖，单二进制 + 单 JSON 文件即可跑起来。

---

## 特性

**API 层**
- `POST /v1/chat/completions` — 支持流式（SSE）与非流式，兼容 OpenAI 请求/响应格式
- `POST /v1/images/generations` — 图像生成
- `GET /v1/models` — 模型列表
- `GET /health` — 健康检查 + 号池概览

**双区域账号池（国内 + 国际）**

MiniMax 的国内站（`agent.minimaxi.com`）和国际站（`agent.minimax.io`）是**两套独立的账号体系**，一方的令牌在另一方会被拒绝。所以区域是**账号级属性**而不是全局开关：

| 区域 | 站点 | 典型注册方式 | 推断依据 |
| --- | --- | --- | --- |
| `cn` | agent.minimaxi.com | 国内手机号 | 令牌中手机号为 `+86` |
| `global` | agent.minimax.io | 邮箱 / 海外手机号 | 其余情况 |

- 加号时自动从令牌里解出**邮箱或手机号**作为账号标识，号池列表直接显示，不用再靠人脑记
- 区域三态：**自动识别**（默认，按令牌里的手机号区号推断）、`cn`、`global`。识别不出来时按国际站处理
- 同一号池里两种账号可以混放，调度时各走各的上游域名
- 批量导入默认「自动识别」，国内国际混在一份列表里也能一次导完；选定区域则整批强制覆盖
- 单行可覆盖区域：`cn:令牌`、`global:令牌`，或 `名称----令牌----cn`（注意 `名称----令牌` 里的名称只是名字，不参与区域判断）
- 编辑时换上新令牌且不指定区域，区域会自动跟随新令牌——避免把国内令牌留在国际站上
- 加号**只需要粘 JWT**：`realUserID` 由服务端自己向上游问回来。它不在令牌里、也无法从令牌推导，但每个签名请求都要求它——详见「账号身份」
- 国际站请求必须走境外出口（`upstream.proxy`），否则每个请求都是空 body 的 `401`——详见「出口 IP」

**号池管理**
- 多账号令牌池，支持分组、优先级、单账号并发上限
- 四种调度策略：`least_inflight`（默认）/ `round_robin` / `priority` / `random`
- 粘性会话：同一会话（`user` 字段或 `X-Session-Id` 头）优先复用同一账号
- 失败自动降级：指数退避冷却（`cooldown`）、凭证失效标记（`invalid`）
- 请求级故障转移：单次请求内最多重试 `MaxAttempts` 个账号
- 批量导入（粘贴多行令牌）、导出、批量启停/改并发/清冷却/删号
- 后台异步探测连通性，不阻塞接口

**每日签到与积分**
- 后台按天自动给**每个国际站账号**签到，默认每天 09:05（可改），账号之间留 `GapSeconds` 间隔避免同 IP 并发
- 签到结果（今日状态、连续天数、本次得分、七日面板）直接落在账号记录里，号池列表多两列：**签到** 与 **积分**
- 积分用完的账号在请求中**自动跳过**，不用手工停用
- 单账号可手动「立即签到」/「刷新积分」，也可一键「全部签到」
- 跳过的账号记 `skipped` 而不是 `failed`——**国内站账号会被明确跳过**，因为国内站的签到参数是另一套（用 `timezone_id` 而不是 `timezone_offset`），拿国际协议去签只会得到一个和「令牌失效」长得一模一样的拒绝

> 签到是**国际站专属**能力。国内站账号会被跳过并给出原因，而不是被判为失败。

**浏览器指纹**

MiniMax 的每个 API 请求都要带一个 `yy` 签名，而这个签名是**对包含浏览器指纹的完整 URL 计算的**——令牌必须和它被签发时的指纹一起重放。因此指纹（`uuid` / `device_id` / 屏幕尺寸）是账号的一部分：

- 加号时留空则自动生成一组自洽的指纹，大多数情况够用
- 想复刻抓包到的原始会话，可以在「高级设置」里填真实值
- 指纹不完整（缺 `uuid` 或 `device_id`）的账号会被判定为**不可调度**，而不是被选中后在上游失败

**管理台**
- 仪表盘：调用量趋势、模型分布、账号排行、资源占用
- 号池管理、客户端密钥、模型目录、生成画廊、请求审计、系统设置
- 中英双语，明暗双主题
- 管理端会话基于 Bearer Token，密码用 HMAC-SHA256 迭代 12 万次加盐存储

**审计**
- 每次网关请求落一条记录（模型、账号、状态码、耗时、token 数）
- 可配置保留天数与最大条数，后台 janitor 定时清理
- 请求体记录可选、可限长

---

## 快速开始

### 环境要求

- Go 1.24+
- Node.js 20+（只在需要重新构建前端时用到）
- **一个境外的出站代理**（部署在境外机器上则不需要）——上游有地域围栏，直连全是空 body 的 `401`，详见「出口 IP」

### 构建

```bash
# 1. 前端（产物输出到 frontend/dist）
cd frontend
npm install
npm run build

# 2. 后端（产物是仓库根目录的 minimax2api 可执行文件）
cd ../backend
go build -o ../minimax2api ./cmd/minimax2api
```

### 运行

```bash
# 指定初始管理员密码（首次启动写入，之后改密走管理台）
MINIMAX2API_ADMIN_PASSWORD=你的密码 ./minimax2api -addr 127.0.0.1:8080
```

打开 `http://127.0.0.1:8080`，用 `admin` / 你设置的密码登录。

> 不传 `MINIMAX2API_ADMIN_PASSWORD` 时，首次启动会随机生成一个密码并打印在控制台。

### 加号

**取令牌**：登录 MiniMax Agent，随便发一条消息，F12 打开开发者工具，在 `Application → Local Storage` 里找到 **`_token`** 的值（一个 JWT）。

**录入**：管理台 → **号池管理** → 添加账号，把这个 JWT 整段粘进「登录令牌」即可。区域会自动判断，指纹会自动生成。

> **只粘 JWT 就够了**——`realUserID` 会由服务端自己去上游问回来，见下方「账号身份」。
>
> 想手工指定也可以，格式是 `realUserID+JWT`（形如 `9007199254740993+eyJhbGciOi...`）。粘了这个就不会再去做自动发现。

保存后系统会在后台探测一次连通性，不卡界面。

批量加号时，导入框每行一个，支持这些写法：

```
eyJhbGciOiJIUzI1NiIs...
主号----eyJhbGciOiJIUzI1NiIs...
cn:eyJhbGciOiJIUzI1NiIs...
备用号----eyJhbGciOiJIUzI1NiIs...----global
```

导入弹窗里的**区域**默认是「自动识别」，逐行读令牌里的手机号区号。国内号和国际号混在一份列表里直接一次导完，不用分批。如果整批都是同一个站点的账号（或者令牌里没有手机号、推断不出来），就在那里选定 `国内` / `国际` 强制覆盖。

> 已经存在的账号会被**更新**而不是报错——令牌过期后重新导入同一行就能刷新。
>
> `主号----令牌` 里的「主号」只是账号名，不参与区域判断；要按行指定区域得写成三段的 `名称----令牌----cn`。

### 调用

```bash
# 先在管理台「客户端密钥」建一个 key
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-mm-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-agent",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

---

## 模型映射

| 模型 ID | 说明 |
| --- | --- |
| `minimax-agent` | 通用 Agent，自动规划并调用工具（默认） |
| `minimax-m3` | 对话模式，响应更快 |
| `minimax-m3-thinking` | 深度思考模式，推理内容走 `reasoning_content` |
| `minimax-image` | 图像生成，走 `/v1/images/generations` |

> **所有模型 ID 最终打的是同一个上游 Agent**。MiniMax Agent 的 Web API 里没有模型选择器，它返回什么取决于账号本身的权限。模型目录的作用是：给那些非要填模型名的客户端一个合法值，以及让管理台能按标签统计用量。
>
> 推理内容会被自动分离到 `reasoning_content`，不会混进正文——这是适配器按 payload 字段分流的，不依赖上游的事件编号。

---

## 配置

### 启动参数 / 环境变量

| 参数 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-addr` | `MINIMAX2API_ADDR` | `127.0.0.1:8080` | 监听地址 |
| `-data` | `MINIMAX2API_DATA` | `data` | 数据目录 |
| `-static` | `MINIMAX2API_STATIC` | `frontend/dist` | 前端产物目录 |
| `-admin-user` | `MINIMAX2API_ADMIN_USER` | `admin` | 初始管理员用户名 |
| `-admin-password` | `MINIMAX2API_ADMIN_PASSWORD` | 随机 | 初始管理员密码 |

### 运行时设置

以下配置存在 `data/app.json` 里，可在管理台「系统设置」直接改，改完即时生效：

- **服务**：最大并发请求数、管理员用户名
- **上游**：国际站/国内站地址、默认 Agent ID、会话与消息路径、`model` 字段模板、默认屏幕尺寸、语言、请求超时、流空闲超时、**代理（国际站必填，见「出口 IP」）**、身份查询路径、User-Agent
- **路由**：调度策略、冷却基数/上限、最大重试次数、容量等待、粘性会话 TTL、是否优先空闲账号
- **审计**：保留天数、最大记录数、是否记录请求体、请求体长度上限
- **媒体**：生成文件目录、公开访问前缀、总容量上限、是否自动转存
- **签到**：开关、每日时刻、账号间隔、请求超时、是否跳过零积分账号、积分新鲜期、积分刷新间隔、时区偏移、浏览器指纹字段、三条端点路径

---

## 上游协议

MiniMax Agent 没有公开 API，这里是把它 Web 端的调用方式复刻出来。**签名算法来自对前端 bundle 的逆向**，端点与请求体结构则做成了运行时可配置，以便上游改版时不用重新编译。

### 请求签名

每个请求带四个头：

| 请求头 | 计算方式 |
| --- | --- |
| `token` | 账号的 JWT |
| `x-timestamp` | 当前 Unix 秒 |
| `x-signature` | `MD5(秒级时间戳 + 静态盐 + 请求体)` |
| `yy` | `MD5(encodeURIComponent(完整URL) + "_" + 请求体 + MD5(毫秒级时间戳) + "ooui")` |

两个签名各有分工，这也是为什么账号需要携带指纹：

- **`x-signature` 与 URL 无关**，只覆盖时间戳、盐和请求体
- **`yy` 绑定了完整 URL**，而 URL 的 query 里装着 `uuid` / `device_id` / `user_id` / 屏幕尺寸

所以 query 的**参数顺序也是协议的一部分**——`yy` 是对编码后的 URL 取摘要，顺序一变签名就废了。实现里没有用 `net/url` 的 `Values.Encode()`（它会按键排序），而是按固定顺序手工拼接。

> 顺带一提，Go 的 `url.QueryEscape` 不能替代 JS 的 `encodeURIComponent`：前者把空格编码成 `+`、保留 `~` 却转义 `!*'()`，而后者正好相反。差一个字符签名就对不上，所以 `encodeURIComponent` 是照着 JS 语义自己实现的，并有单元测试逐字符钉住。

### 账号身份：`user_id` 与 `realUserID`

**签名对不上和身份不对，上游给的是两个不同的错**，这也是排查时最快的分诊手段：

| 上游应答 | 含义 |
| --- | --- |
| `400 invalid signature` | 签名算错了（盐、时间戳、body、`yy` 的 URL 拼接） |
| `401` + **空 body** | 签名是对的，是**身份或出口**不对 |

签名这一层做完之后，还有**两个各自独立、缺一不可**的条件，缺任何一个都只给你一个空 body 的 `401`：

1. **query 里必须带 `?user_id=<realUserID>`**，且值必须正确
2. **出口 IP 必须在境外**

这两条都踩过坑，值得单独说清楚：

- **`realUserID` 不等于 JWT 里的 `user.id`。** 同一个账号里这两个值是**两个不同的数字**（都是 18 位，肉眼扫过去很像，但不是一回事）。把 JWT 里那个填回去，效果和**不填**一模一样——都是 `401`。它**无法从令牌推导**，网页端是把它放在 `localStorage.user_detail_agent` 里的，服务端读不到。
- **这个要求是全站的，不是某个端点的怪癖。** `/minimax-cloud/api/v1/agent`、`/minimax-cloud/api/v1/signin/config`、`/matrix/api/v1/user/profile` 实测一致：带 `user_id` 200，不带 401。
- **`realUserID` 超过了 2^53**，所以它必须以**字符串**读入。走一遍 JSON number → float64 会被静默舍入成另一个数（`9007199254740993` 会变成 `9007199254740992`），那就是另一个账号了。
- **出口 IP 是硬门槛，不是优化项。** 同一个请求、同一个令牌，走代理 200，直连 401。所以 `upstream.proxy` 在国际站上基本是**必填**的（见「系统设置 → 上游 → 代理」）。

> **为什么代码里要专门防这个**：`401` 在适配层会被归到 `ErrInvalidCredential`，而调度器看到这个错误会把账号**标记为 invalid 退役**。也就是说，一个纯粹因为少带 `user_id` 而 401 的健康账号，会被当成死号踢出号池。所以「拿不到 `user_id` 就跳过签到」不是偷懒，是防止误杀（见 `internal/signin` 的跳过原因）。

**自动发现**：`GET {base}/v1/api/user/info` 是唯一一个**不需要 `user_id`** 也能返回 200 的端点，应答里 `data.userInfo.realUserID` 就是那个值。加号时后台会先调它把 `user_id` 补上，再去做连通性探测——所以**只粘 JWT 就能用**。这条路径失败时（没配代理、上游不可达）账号仍然会被建出来，只是签到会因为缺 `user_id` 而被跳过并写明原因，不会静默退役。

### 出口 IP

国际站的业务请求全部有地域围栏，**部署在国内机器上必须给上游配代理**，否则每一个请求都是空 body 的 `401`，看起来极像令牌失效。`upstream.proxy` 支持 `http://` / `socks5://`，只作用于出站的上游请求，不影响管理台访问。

排查时注意别被本地环境骗了：Windows / Git Bash 下如果设了 `http_proxy`，**连本机回环的请求也会被塞给代理**，`curl` 会显示连不上（`000`），看着像服务没起来。测本地端口记得加 `--noproxy '*'`。

### 调用流程

```
POST {base}/agent/{agent_id}/session          → 建会话，拿 session_id
POST {base}/archon/api/v1/session/{id}/message → SSE 流，取 msg_content
```

两条路径都能在「系统设置 → 上游」里改（`sessionPath` / `messagePath`，支持 `{agent_id}`、`{session_id}` 占位符）。

### 流式解析

适配器**按 payload 字段分流，不依赖上游的事件类型编号**：

```
data:{"type":6,"agent_message_chunk":{"msg_content":"你好"}}
```

`type` 是数字且未公开，一旦上游重新编号，写死数字的解析器就会静默失效。所以判断依据是「payload 里出现了 `msg_content` 还是 `reasoning_content`」，未知帧会被安全跳过而不是报错。错误帧则通过 `error_msg` / `status_msg` / 非零 `status_code` 识别。

### 关于图片

MiniMax 的附件走的是带签名的上传通道，无法从外部复刻，因此图片以 **URL 形式**转发给上游：

- `http(s)` 图片地址直接透传
- `data:` 内联图片会写入媒体目录并改用「媒体公开前缀」重新发布——上游需要能自己下载到它；没配置公开前缀时会直接报错说明，而不是发出去静默失败

### 签到与积分

签到走的是另一条通道，端点同样可配置：

```
GET  {base}/minimax-cloud/api/v1/signin/status    → 七日面板与今日状态
POST {base}/minimax-cloud/api/v1/signin/claim     → 领取（请求体固定为 {}）
POST {base}/matrix/api/v1/commerce/get_membership_info → 剩余积分
```

签到查询里的坑比普通请求多，这里记下三条，都是照着抓包逐字节对出来的：

**一、`op_ticket=undefined` 同时存在于签名和不存在于请求里。** 前端把 `op_ticket: undefined` 塞进了参数对象，`URLSearchParams` 会把它字符串化成字面量 `undefined`——所以**签名串里确实有 `op_ticket=undefined`**；但 axios 在真正序列化请求时会丢掉 undefined，所以**线上 URL 里没有它**。这是两条不同的 query 串，实现里分别拼成 `signedPath` 和 `wirePath`：

```go
// forSignature 决定拼哪一条：签名串保留 op_ticket=undefined，请求串丢掉它。
signinParams(settings, cred, unixMs, forSignature)
```

**二、这里的 `yy` 摘要的是相对路径，不是完整 URL。** axios 把 `baseURL` 和 `url` 分开保存，参与签名的只有后者。这和 `client.go` 里的 Agent 端点不同——那边签的是绝对 URL。照抄会得到清一色的签名错误。

**三、`timezone_offset` 是 `-60 * getTimezoneOffset()`。** 所以 UTC+8 传的是 `28800`，不是 `-480`。少一个负号就签到失败。

另外两个细节：`yy` 的载荷在 GET 和 POST 下**都是 `"{}"`**（只有 `x-signature` 用真实请求体，GET 时是空串）；参数顺序仍然是协议的一部分，22 组键值按抓包顺序原样复刻。

积分余额也有一个坑：真正生效的字段是 `get_membership_info` 里的 `op_credit_summary.total_remaining_amount`，而且它是个**字符串**（`"400"`）。同级的 `opcredit_balance`、`total_remains_credit` 是迁移前的旧字段，在已经迁移的账号上**一律读 0**——照着它们判断会把有余额的账号全部排除出调度。`is_migrated_to_op: true` 就是迁移标记，`get_credit_details` 则已经废弃（返回 200 但每个字段都是 0）。

> 零积分账号被跳过是**有新鲜期**的：只在读数比 `CreditFreshMin`（默认 6 小时）新时才生效。过期后余额算作未知，账号重新参与调度——否则一个隔夜的 0 会把上游本愿意供应的额度永久锁死。

---

## 架构

```
                    ┌──────────────────────────────┐
   OpenAI 客户端 ──▶│  gateway   /v1/*             │
                    │  · 鉴权（客户端密钥）         │
                    │  · 限流（RPM / 并发）         │
                    │  · SSE 转发                  │
                    └──────────┬───────────────────┘
                               │ Acquire / Release
                    ┌──────────▼───────────────────┐
                    │  pool   号池调度              │
                    │  · 策略选择 / 粘性会话        │
                    │  · 冷却退避 / 故障转移        │
                    │  · 零积分账号跳过             │
                    └──────────┬───────────────────┘
                               │
      管理台 ──▶┌──────────────▼───────────────────┐
                │  admin   /admin/api/*            │
                │  · 账号 / 密钥 / 模型 / 审计      │
                └──────────┬───────────────────────┘
                           │
                ┌──────────▼───────────────────────┐
                │  store   内存态 + 单文件持久化    │
                │  · 读走 RWMutex，写走后台单写者   │
                │  · Settings 走 atomic 快照        │
                └──────────┬───────────────────────┘
                           │
   每日定时 ──▶┌───────────▼──────────────────────┐
               │  signin  签到调度 + 积分轮询      │
               │  · 错过的时间点立即补             │
               │  · 国内站账号明确跳过             │
               └───────────┬──────────────────────┘
                           │
                ┌──────────▼───────────────────────┐
                │  minimax  上游客户端              │
                │  · 签名 / 会话 / SSE 帧解析       │
                │  · 签到 / 积分                    │
                └──────────────────────────────────┘
```

### 目录

```
backend/
  cmd/minimax2api/      入口：路由装配、静态托管、优雅关闭
  internal/config/      启动参数 + 运行时设置模型
  internal/store/       状态与持久化（含回归测试）
  internal/pool/        号池调度
  internal/minimax/     上游协议客户端（签名、会话、SSE、令牌解析、签到/积分）
  internal/signin/      每日签到调度 + 积分轮询（不含 HTTP，客户端注入）
  internal/gateway/     OpenAI 兼容层 + 限流
  internal/admin/       管理台 API
frontend/
  src/app/              壳层与路由
  src/components/ui/    自建 UI 组件（零依赖）
  src/features/         各功能页
  src/shared/           API 客户端、鉴权、i18n、工具
tools/
  smoke.py              端到端冒烟测试
  contract.py           前后端接口契约检查（路径 / 方法）
  fields.py             DTO 字段契约检查（响应字段 / TS 类型）
  signin_e2e.py         签到 + 积分链路端到端（内置假上游）
  render.mjs            真实浏览器渲染检查（CDP，需本机 Chrome）
  secret_scan.py        工作树 + 全历史密钥扫描（推送前跑）
  hooks/pre-commit      提交前自动跑 secret_scan.py --tree-only
```

### 持久化设计

`data/app.json` 是唯一的状态文件。所有变更先改内存，再由**单个后台协程**防抖（40ms）后原子落盘（写临时文件 + rename）。

请求处理路径**不会**在持锁期间做磁盘 I/O，因此慢速或被占用的文件系统不会拖垮服务。运行时设置额外维护一份 `atomic.Value` 快照，使得已经持有写锁的回调（例如账号探测结果回写）也能安全读取配置——Go 的 `sync.RWMutex` 不可重入，这一点是硬性要求。

---

## 测试

```bash
cd backend
go test ./...            # 单元测试（含并发/重入锁回归）
go vet ./...
```

覆盖五个核心包，其中四个完全不依赖网络：

| 包 | 覆盖内容 |
| --- | --- |
| `internal/store` | 配置快照的并发读写、写锁内重入读配置（死锁回归）、快照隔离 |
| `internal/pool` | 账号筛选（禁用/失效/无令牌/无指纹/冷却过期）、四种调度策略、粘性会话、退避与封顶 |
| `internal/minimax` | 签名公式与逐字符的 `encodeURIComponent` 对照、query 顺序与编码、按区域选主机、SSE 帧解析（推理/正文分离、媒体收集、错误识别）、JWT 令牌解析、签到两条 query 串的差异（签名含 `op_ticket=undefined`、请求不含）、真实抓包的 `x-signature` 对照 |
| `internal/signin` | 错过的时间点补签、重复领取记为「已签」、国内站账号跳过、失效令牌退役、失败也占掉当天（避免上游故障变成请求循环）、积分接口故障不牵连账号健康、扫描不可重入 |
| `internal/gateway` | 端到端请求路径——鉴权、限流、故障转移、OpenAI 响应格式、流式、审计、图像、内联图片拒绝 |
| `internal/gateway`（上游桩） | 用 `httptest` 顶替上游，因此不需要真实令牌就能覆盖完整链路 |

> `pool` 里的死锁与并发用例用 `channel + timeout` 断言，而不是裸 `t.Fatal`——测试进程卡住时，超时能给出失败信息而不是整体挂起。

端到端：

```bash
# 先启动服务，然后：
python tools/smoke.py --base http://127.0.0.1:8080 --password 你的密码 --skip-upstream
```

`--skip-upstream` 会跳过真正打上游的用例（没有有效令牌时会一直等到超时），其余约 50 项断言全部覆盖管理台、网关错误路径与号池行为。

签到链路：

```bash
python tools/signin_e2e.py --base http://127.0.0.1:8080 --password 你的密码
```

签到没有账号就测不了，所以这个脚本在**自己进程里**起一个假上游，把两个一次性账号的 `baseURL` 指过去，再从外部驱动真实的管理台接口。它验的是单测覆盖不到的那部分：两条 query 串在经过真实 HTTP 客户端之后仍然一条含 `op_ticket=undefined`、一条不含；余额确实是从 `op_credit_summary.total_remaining_amount` 读的（含它发字符串这件事）；零积分账号真的会离开调度池、而读数过期后又会回来；国内站账号是被跳过而不是被发错协议。

加 `--recon` 还能顺手做一次**跨实现签名对照**——把服务实际发出去的请求喂给逆向工作区那份独立验证过 12/12 的 Python 签名器，两个实现算出同一个 `yy` 才算过：

```bash
python tools/signin_e2e.py --password 你的密码 \
    --recon "C:/path/to/逆向minimax国际签到接口/code"
```

> 这个脚本会**真签到**（它就是去触发一次扫描），也会建号删号。对着一次性实例跑，别对着正在服务的生产实例跑。

接口契约：

```bash
python tools/contract.py --base http://127.0.0.1:8080 --password 你的密码
```

前端是编译产物，路由写错只会在浏览器里变成 404。这个脚本把控制台**实际会发的每一个请求**都重放一遍，只有 404/405 才算失败（400 是参数校验、401 是鉴权，都说明路由命中了）。改完任一侧的接口路径后跑一下，能立刻发现前后端对不上的地方。

字段契约：

```bash
python tools/fields.py --base http://127.0.0.1:8080 --password 你的密码
```

路由对了不代表字段对得上——后端漏掉或改了一个字段名，页面只会静默显示空白。这个脚本从前端 api 层的 `export type Xxx = {...}` 解析出每个 DTO 期望的字段，再拿真实响应逐字段核对。集合为空时自动降级为「前端 DTO vs Go struct 的 json tag」静态比对，保证覆盖率不打折。

嵌套对象会被展开成点路径（`resources.routableAccounts`）逐个核对，而不是只看第一层。判定用的是「路径存在性」而非「值非空」，因为 `quota: AccountQuota | null` 这类字段本来就允许是 `null`。

脚本每次运行都会先自检路径判定函数本身，避免出现「永远返回通过」的假绿。

真实渲染（需要本机有 Chrome）：

```bash
# 先起一个带调试端口的 headless Chrome
# macOS / Linux：
chrome --headless=new --disable-gpu --no-first-run --no-default-browser-check \
       --remote-allow-origins=* --remote-debugging-port=9222 \
       --user-data-dir=/tmp/chrome-minimax about:blank

# Windows（Git Bash）——--user-data-dir 必须用 Windows 路径，见下方说明：
"/c/Program Files/Google/Chrome/Application/chrome.exe" --headless=new --disable-gpu \
       --no-first-run --no-default-browser-check --remote-allow-origins=* \
       --remote-debugging-port=9222 \
       --user-data-dir="C:\Users\你的用户名\AppData\Local\Temp\chrome-minimax" about:blank

node tools/render.mjs http://127.0.0.1:8080 http://127.0.0.1:9222 你的密码
```

> **Windows 上 `--user-data-dir` 必须写 Windows 路径**（如 `"C:\Users\你\AppData\Local\Temp\chrome-minimax"`）。传 `/tmp/...` 时 Chrome 会照常启动、日志一片空白，但**调试端口永远不会监听**，`curl http://127.0.0.1:9222/json/version` 一直连接被拒——看起来像端口被占，实际是路径没被认。另外本地回环请求记得加 `--noproxy '*'`，否则环境里的 `http_proxy` 会把它塞给代理，同样是连不上的假象。

前三个脚本都是 HTTP 层面的，看不见 React 渲染崩溃、未捕获的 Promise 异常或者一片空白。这个脚本通过 CDP 驱动真实 Chrome，逐页走一遍控制台，检查：有没有抛异常 / 有没有落到错误边界 / `#root` 是否为空 / 页面标题对不对。顺带验证鉴权守卫——已登录访问 `/login` 必须被弹回仪表盘。

跑完记得收尾，否则会在后台留一堆进程和磁盘垃圾：

```bash
taskkill //F //IM chrome.exe //T     # Git Bash 里参数要写双斜杠
```

> 如果渲染检查报 `SecurityError: Failed to read the 'localStorage' property`，那是**目标站不可达**——导航失败后页面停在 `about:blank`，而它的 origin 是 opaque 的，读写 localStorage 一律被拒。先去 `curl <base>/health` 确认服务起来了，别去改 localStorage 相关代码。

### 推送前：密钥扫描

```bash
python tools/secret_scan.py              # 工作树 + 全部历史（178 个 blob）
python tools/secret_scan.py --tree-only  # 只扫工作树，够快，可以放进 pre-commit
```

这个仓库的历史上曾经躺着**一个可用的 MiniMax JWT**（旧 Python 版的 `config.json`，已清除）。所以扫描器**默认走完整历史**，而不只是当前工作树——`git filter-repo` 只重写你指给它的那些提交，密钥躺在**另一个文件**里就会被完整地漏过去，只有把每个可达 blob 都读一遍才知道结果。

退出码为 1 表示有命中，可以直接用来卡住推送。**命中不等于真泄漏**：手工造的测试令牌和文档示例长得跟真凭据一模一样，所以脚本把每条命中（**打码后**）连同路径与 blob 一起打出来，由人判断。占位符（`your-…`、`admin12345`、i18n 里的 `password: "Password"`、桩令牌那种 4 字符签名）会被过滤掉——一个天天误报的扫描器会训练人无视它，那比不扫还糟。

**装成 pre-commit 钩子**（每个 clone 各配一次，只写仓库级配置，不动全局）：

```bash
git config core.hooksPath tools/hooks
```

之后每次提交前会自动跑 `--tree-only`（快），命中就拦下并给出绕过方式。钩子只在**找不到可用 Python 时跳过**而不是拦住——一个因为缺解释器而卡死每次提交的钩子，只会让人条件反射地加 `--no-verify`，等于没装。

> 真出现命中时的处理顺序：**先吊销/轮换那个凭据**，这才是真正堵住口子的动作；重写历史是次要的，而且强推后的旧对象**仍可按 SHA 访问**，要等 GitHub Support 在服务端跑一次 GC 才真正消失。

---

## 部署

生产建议：

1. `go build -ldflags "-s -w"` 出精简二进制，配合 `frontend/dist` 一起丢到服务器
2. 用 systemd / supervisor 常驻，监听 `127.0.0.1:8080`
3. 前置 Nginx 反代，注意 **关闭响应缓冲**，否则 SSE 流式会被攒批：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 600s;
    chunked_transfer_encoding on;
}
```

4. 管理台只对内网开放，或加一层访问控制
5. `data/` 目录做好备份——账号令牌都在里面

### 静态资源缓存

后端对前端产物分了两档，不需要在 Nginx 里额外配：

| 路径 | 响应头 | 原因 |
| --- | --- | --- |
| `/`、SPA 回退路由 | `Cache-Control: no-cache` | 入口 HTML 里写着带 hash 的 bundle 文件名，缓存住会导致重新构建后仍指向已不存在的旧文件 |
| `/assets/*` | `Cache-Control: public, max-age=31536000, immutable` | Vite 的产物名带内容 hash，内容变了文件名就变，可以放心长缓存 |

---

## 常见问题

**Q：账号一直显示 `cooldown`，日志里是 `credential rejected`？**
令牌过期了。重新登录站点取一份新的 `_token`，在号池管理里编辑该账号或重新导入即可。

**Q：国内站账号加进去一直失败？**
检查区域是否选对。国内站令牌拿到国际站域名上必然被拒——在号池管理里把区域改成 `国内`，或者干脆选「自动识别」让系统读令牌里的 `+86`。批量导入时如果整批都是国内号，把导入弹窗的区域选成 `国内` 一次覆盖掉最省事。

**Q：令牌里没有手机号，区域会怎么算？**
按国际站（`global`）处理。邮箱注册的账号本来就在国际站，所以这个兜底对绝大多数情况是对的；如果你手上是那种没绑手机号的国内号，手动把区域指到 `国内` 即可。

**Q：提示「Token 中不能包含空白字符」？**
粘贴时带上了换行或空格。令牌本身是一串连续字符，只粘令牌那一段。

**Q：流式响应是一坨出来的，不是逐字？**
反代开了响应缓冲。参考上面的 Nginx 配置关掉 `proxy_buffering`。

**Q：上游改版后全部 404 / 400？**
上游路径变了。重新抓一次包，在「系统设置 → 上游」里更新会话路径、消息路径和 `model` 字段模板，不用重新编译。

**Q：所有请求都返回 `401`，而且响应体是空的？**
十有八九不是令牌坏了，而是**出口 IP 在国内**。国际站的业务请求有地域围栏，直连一律空 body `401`，配上网关代理立刻 200。第二顺位的原因是账号缺 `user_id`（`realUserID`）——同样表现。分诊办法：签名错是 `400 invalid signature`，`401` 就是身份或出口的问题，别再回头怀疑盐值。详见「账号身份」和「出口 IP」两节。

**Q：代理配了，还是 401？**
看账号的 `user_id` 列是不是空的。国际站和国内站**都**要求它，且它不等于 JWT 里的 `user.id`（填错等于没填）。正常流程下加号时会自动从上游问回来；如果是老数据或当时没配代理导致发现失败，删掉重新加一次即可。签到遇到这种情况会**跳过并写明原因**，不会把账号误判成坏号退役。

**Q：想换调度策略？**
管理台 → 系统设置 → 路由 → 调度策略。单人用推荐 `least_inflight`，多账号均匀分摊用 `round_robin`。

**Q：生成的图片存哪了？**
默认转存到 `data/generated/`，通过 `/media/` 对外提供。可在系统设置里改目录、公开前缀和容量上限。

**Q：签到显示「已跳过」，是坏了吗？**
不是。跳过有两种原因，账号记录里会写明是哪一种：国内站账号走的是另一套签到参数（`timezone_id` 而不是 `timezone_offset`），拿国际协议去签只会得到一个和「令牌失效」长得一样的拒绝；另一种是账号缺 `user_id`（`realUserID`），这时发起请求必定 401，而 401 会让账号被当成坏号退役，所以宁可不发。两种情况都是**明确跳过并附原因**，跳过是成功语义，不会影响账号健康。

**Q：账号有积分，却被判成 0 分跳过了？**
先看积分列是不是旧的。余额只在读数比「积分新鲜期」（默认 6 小时）新时才用于路由，过期后账号会重新参与调度，不会永久锁死。另外真正生效的字段是 `op_credit_summary.total_remaining_amount`，同级的 `opcredit_balance` 在已迁移账号上一律读 0——如果你自己在别处读余额，注意别读错字段。

**Q：签到时间点机器是关着的，会漏签吗？**
不会。已经过去但当天没执行过的时间点会被判为**立即到期**，开机后马上补一次。判断「当天执行过没有」是直接看账号记录里的签到日期，不额外存一份状态——两份状态就有对不上的可能。

**Q：签到失败会一直重试吗？**
不会。失败的尝试**同样占掉当天**。否则上游一次故障会被每个 tick 重放一遍，变成请求风暴；想立刻重试就在号池管理里对单个账号点「立即签到」。

---

## 免责声明

本项目仅用于**学习与技术研究**，对接的是第三方服务的 Web 端接口，而该服务并未提供公开 API。使用者需自行确保其使用方式符合目标服务的服务条款及所在地法律法规，因使用本项目产生的任何后果由使用者自行承担。

需要明确知道的几件事：

- **签名算法是从前端 bundle 里逆向出来的**，其中的静态盐值是硬编码的。上游随时可能更换算法或盐值，届时本项目会失效——这不是 bug，是这类项目的固有属性。
- **批量使用账号可能触发上游的风控**，导致账号被限制或封禁。请只使用你自己的账号，并自行评估风险。
- **自动签到同理**。签到是官方给单账号的日常福利，把一批账号放进来按天自动领，本质上就是自动化操作多账号，请自行判断这在你所处的场景下是否合适。
- 请勿用于**商业转售、二次分发额度**或任何绕过付费的用途。
- 本项目与 MiniMax 官方无任何关联，未获其授权或认可。

---

## 许可证

本项目以 **GNU Affero General Public License v3.0（AGPL-3.0）** 发布，协议全文见 [LICENSE](LICENSE)。

```
Copyright (C) 2026 juangchuank-ops
```

你可以自由使用、修改、分发本项目，前提是：

- **改了要公开**：若你把修改后的版本通过网络对外提供服务（不只是分发二进制），必须把修改后的完整源码同样以 AGPL-3.0 提供给使用者。
- **保留声明**：分发时保留版权声明与许可证文本。

需要说明的一点：上面免责声明里写的「请勿用于商业转售」是**作者意图的请求**，不是许可证的附加限制。AGPL-3.0 本身允许商业使用——它约束的是**闭源**，而不是**收费**。真正拦得住的是「拿去做闭源服务」，拦不住「拿去卖」。若这与预期不符，需要另行选择非商业许可，但那样会让「能否复用」变得不明确，请自行权衡。
