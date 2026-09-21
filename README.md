# MiniMax2API

把 [MiniMax Agent](https://agent.minimax.io/) 的 Web 端能力封装成 **OpenAI 兼容 API**，并配一套完整的**管理台 + 号池调度**。

前端界面参考 [grok2api](https://github.com/chenyme/grok2api) 的设计语言实现（React 19 + Vite + Tailwind 4，自建零依赖 shadcn 风格组件）。后端是纯 Go 标准库，无第三方依赖，单二进制 + 单 JSON 文件即可跑起来。

---

## 特性

**API 层**
- `POST /v1/chat/completions` — 支持流式（SSE）与非流式，兼容 OpenAI 请求/响应格式
- `POST /v1/images/generations` — 图像生成
- `POST /v1/videos/generations` — 视频生成（MiniMax H3.0 / H3 Max / Hailuo 2.3，见「视频生成」）
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
- 领取前会先跑一遍 agent 侧初始化，领完再对一次账——**这两步都不是可选的**，省掉第一步积分会静默不到账，省掉第二步「报成功但没发」就永远看不见（见「上游初始化」与「签到与积分」）

> 签到是**国际站专属**能力。国内站账号会被跳过并给出原因，而不是被判为失败。

**浏览器指纹**

MiniMax 的每个 API 请求都要带一个 `yy` 签名，而这个签名是**对包含浏览器指纹的完整 URL 计算的**——令牌必须和它被签发时的指纹一起重放。因此指纹（`uuid` / `device_id` / 屏幕尺寸）是账号的一部分：

- 加号时留空则自动生成一组自洽的指纹，大多数情况够用
- 想复刻抓包到的原始会话，可以在「高级设置」里填真实值
- 指纹不完整（缺 `uuid` 或 `device_id`）的账号会被判定为**不可调度**，而不是被选中后在上游失败

> **两个字段的形状不一样，`device_id` 必须是纯数字。**
>
> 网页端把指纹放在两处：`localStorage.UNIQUE_USER_ID`（`uuid`，带横杠的 UUID）和 `sessionStorage.tab_device_id`（`device_id`，一个八位数字，bundle 在 sessionStorage 为空时的兜底是 `1e7 + rand(9e7)`）。上游对 `uuid` 宽容，对 `device_id` 不宽容：**非数字的 `device_id` 会让所有 `/minimax-cloud/…` 请求返回**
>
> ```
> 400 {"error":"internal error","code":"UNEXPECTED_ERROR","base_resp":{"status_code":1406011050}}
> ```
>
> 而这个报错**没有提到任何字段**，读起来像上游故障。更麻烦的是它**只影响 `/minimax-cloud/…`**：`/v1/api/user/info` 和签到端点照样 200。所以「签到一直好好的、一发消息就 400」正是它的表现——生成指纹时给两个字段共用一个 32 位 hex 生成器就会踩到这里。

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

| 模型 ID | 类型 | 说明 |
| --- | --- | --- |
| `minimax-agent` | chat | 通用 Agent，自动规划并调用工具（默认） |
| `minimax-m3` | chat | 对话模式，响应更快 |
| `minimax-m3-thinking` | chat | 深度思考模式，推理内容走 `reasoning_content` |
| `minimax-image` | image | 图像生成，走 `/v1/images/generations` |
| `minimax-h3` | video | H3.0，质量优先；支持多模态参考；消耗账号积分 |
| `minimax-h3-max` | video | H3 Max，约 20 秒完成；仅文生视频与首/尾帧；480P/768P；5-15 秒 |
| `minimax-hailuo-2-3` | video | Hailuo 2.3，成本更低；可用 Token Plan；输出无声视频 |

> **chat 类模型 ID 最终打的是同一个上游 Agent**。MiniMax Agent 的 Web API 里没有模型选择器，它返回什么取决于账号本身的权限。模型目录的作用是：给那些非要填模型名的客户端一个合法值，以及让管理台能按标签统计用量。
>
> **video 类模型不是这么回事**：H3 在上游**根本不是一个可选模型**，而是一个插件。模型 ID 在这里承载的是「生成参数」而不是「路由选择」——详见下一节。
>
> 推理内容会被自动分离到 `reasoning_content`，不会混进正文——这是适配器按 payload 字段分流的，不依赖上游的事件编号。

---

## 视频生成

**H3 不是模型，是插件。** 这一点决定了整个接入方式，值得先说清楚：

- `GET /minimax-cloud/api/v1/config` 返回的是**权威模型清单**，里面只有对话模型（`MiniMax-M3`、`MiniMax-M2.7`、`MiniMax-M2.7-highspeed`）——**没有 H3**。
- H3 挂在 `video-creater` 插件下（`GET /minimax-cloud/api/v1/plugins/enabled`）。在网页端，它是通过输入框里的**引用 chip** 触发的，不是通过 model 字段。
- 网页端**没有任何视频端点**：真正提交生成的是 Agent 服务端的 Connector 工具（`connector__matrix__submit_video_generation`），客户端从不直接调用它。

所以这个网关能控制的，只有「把这一轮话说成什么样」——外加一个请求字段。实测下来这就是全部的接入面：

1. chip 在纯文本里序列化成 `@video-creater`，插件自己的 skill 就写着「用户显式 @video-creater 时使用」。
   **已对真上游验证**：一条纯文本 `@video-creater` 消息确实路由到了插件，它按自己的说明答出了能力契约。
2. 生成参数走消息末尾的 `<video-generation-options>` 块，不在请求体里。
3. **请求体里的 `client_intent: "video_generation"`**。取值来自 bundle 自己的工具类型表——四个视频工具（`BatchTextToVideo`、`BatchImageToVideo`、`VideosRead`、`VideosUnderstand`）全都映射到这一个字符串，同表里还有 `generate_image`、`audio_generation`、`describe_image`，所以它是一套路由词汇而不是一个孤立的魔法值；同一份 bundle 也把它声明成消息请求的一个可选字段。

   > 坦白说清：**它有效果这件事没有证据。** 早期试过一次，但那一次打的是后来被证明是错的入口，所以什么也隔离不出来。现在每一轮视频都带上它，理由是**网页端就是这么发的**，不是因为已经证明它会改变结果。

```jsonc
// POST /v1/videos/generations
{
  "model": "minimax-h3-max",   // 省略时默认 minimax-h3-max（唯一能同步返回的）
  "prompt": "一只猫在弹钢琴",
  "duration": 8,               // 整数秒，省略时用设置里的默认值
  "ratio": "16:9",
  "resolution": "480P",
  "image_url": "https://…"     // 可选，首帧图生视频
}
```

上游实际收到的这一轮话长这样（`client_intent` 是同一请求的字段，不在文本里）：

```
@video-creater 一只猫在弹钢琴

<video-generation-options>
{"duration":8,"model":"MiniMax-H3-Max","ratio":"16:9","resolution":"480P"}
</video-generation-options>
```

块的格式不是猜的：它是网页端 bundle 里**写和读这一对函数**共同定义的形状，而两者互相吻合（写入端 `` `<${tag}>\n${JSON}\n</${tag}>` `` 前面空一行；读取端正则锚定在消息**末尾**）。`internal/minimax/video_test.go` 把上游那个正则原样搬过来，跑在本地写入端的输出上——改写格式会让它直接红。

### ★ 成品不在对话流里

**生成的视频不会出现在 SSE 流里。** 这是最容易误判的一环：一轮真的成功过（Agent 提交了任务、文件真的存在、积分真的扣了），从 completion 接口看却可能是**零 media**——因为文件写进了账号的网盘，而流里只说了话。

文件由两步免费 GET 报出来：

| 步骤 | 接口 | 给出什么 |
| --- | --- | --- |
| 1 | `GET /minimax-cloud/api/v1/session/{id}/input-summaries` | `summaries[].artifacts[]`，含 `node_id`、`category`（`videos`）、`mime_type`、`name`、`size_bytes`、`created_at` |
| 2 | `GET /minimax-cloud/api/v1/drive/file/{node_id}/download-url` | 一条**带签名的 OSS 直链**，有效期约两小时 |

两个坑：第 2 步返回的 `download_url` **不带 scheme**（`matrix-internal.oss-….aliyuncs.com/Mavis/…`），必须补上 `https://`；而且它是一条**带过期的凭据**，不该被缓存或写进日志。

网关在每轮视频之后都会查一次，按 `created_at >= 本轮开始时刻` 过滤，所以同一个会话里旧轮次的文件不会被当成这一轮的产出。查询失败**不影响**这一轮的结果——那一轮自己的答复才是答案。这两个路径在「系统设置 → 上游」里分别叫**会话摘要路径**与**网盘文件路径**。


**响应**：

```jsonc
{ "created": 1790000000, "model": "minimax-h3-max",
  "status": "succeeded",                      // 或 "pending"
  "data": [{ "url": "/media/media_ab12cd34.mp4", "source_url": "https://…" }] }
```

`status` 让调用方不必解析正文就能分清「这一轮拿到成品了」和「没拿到」。但它的能力**到此为止**——它无法再区分「任务在跑」和「上游根本没执行」，那两种情况的差别只写在 `detail` 里：

| 模型 | 典型耗时 | 同步接口能不能等到 |
| --- | --- | --- |
| `minimax-h3-max` | 约 20 秒 | 能，直接拿到可播放的 mp4 |
| `minimax-h3` / `minimax-hailuo-2-3` | 15–30 分钟 | **不能**。超时后返回 `status: "pending"` 和 Agent 自己的原话——这一轮 HTTP 等不到结果 |

> 把慢模型当成快模型处理，只会把「一个可用的中间答复」变成「一个网关超时」。所以这里不假装能同步等到。

### ⚠️ 实测结论：格式与账号能力都已验证，剩下的是调度与取件

这个接入面被真上游验证过四次（同一账号、境外出口、`MiniMax-H3-Max` / 5s / 16:9 / 768P）：

- ✅ **`@video-creater` 确实路由到插件**，options 块**四个参数一个不差被解析**——Agent 会原样复述
  `user-specified model MiniMax-H3-Max, duration 5, ratio 16:9, resolution 768P`。
  这段文本上游完全消化得了，**格式不需要再猜**。
- ✅ **账号侧能力是有的**。免费 GET 直接读到：

  ```json
  {"plugins":[{"name":"video-creater","display_name":"video-creator",
               "icon_url":"agent-cdn.minimax.io/plugin-marketplace/v1/icons/video-creater/1.4.2/…"}]}
  ```

  `GET /minimax-cloud/api/v1/skill` 也照常列出 `mcode-tools-master`（描述里写明它是「调用任何
  Connector 工具、以及多模态生成的主要入口」，CLI 就在 Agent 容器的 PATH 上）。
  **所以「账号没装插件」这条假设可以划掉了。**
- ⚠️ **`GET /channel/connections` 返回空是正常的**，别把它当成缺能力：它是初始化序列的第三步，
  列的是飞书 / Telegram / 微信这类**绑定的聊天通道**，账号没绑过任何 IM 就是空表。
- ❌ **四次都没在同步接口里拿到 mp4**。前两次的原因是 Agent 容器里没有可用的执行通道：它逐个查过四个位置，
  全空——PATH 上的 `mcode-tools`、`<connected-app-tools>`、`<plugin-mcp-tools>`、`mavis`。
  它按插件 skill 的要求拒绝改用裸 HTTP，也拒绝在拿不到成品时谎报成功，只留下自己的原话：

  > The plugin skill lists `video-creater:write-h3-prompts` only, with no connected-app-tools or plugin-mcp-tools.

- 🔁 **一次抓包把入口本身推翻了**。网页端发消息用的是
  `POST agent-stream.minimax.io/minimax-cloud/api/v1/session/{id}/message`，而网关当时用的是
  `agent.minimax.io` + `/archon/api/v1/session/{id}/message` —— **`/archon/` 这个前缀在网页端 17 个业务接口里一个都不存在**。
  同时查到的还有：query 是 **22 个参数**而不是当时的 6 个；网页端开会话用的是 **mavis** agent，不是 `general`。

  三条都已按抓包改掉（含存量实例的迁移）。

**还差什么**：`client_intent` 与取件两步都**照着证据补齐**了，但都没有在真上游跑过一次完整的成功轮次。
网页端那条成功记录（同一账号、`tool_call_count=5`、网盘里留下一个 801 KB 的 mp4）是它们成立的依据，
但它证明的是「网页端能」，不是「这个网关现在能」。要确认，需要再花一轮积分发一次真实请求。

> 取件那两步是**可验证的**：产物清单与下载链接都已经用真实账号单独跑通（清单读出 node_id，
> 下载链接读出带签名的 OSS URL）。没验证过的只有「我们自己发的这一轮，能不能让上游真的产出文件」。

**★ 扣费与产出是解耦的**：四次真机请求把余额从 **1187 打到 564**（约 620 积分），**零产出**。
第三轮最贵，一次 492 —— 因为那一轮没有明确提到插件，Agent 就自己反复找路，想了 3 分 4 秒。
**不要把「余额下降」当成「生成成功」的证据。**
排查这类问题时，先用不花钱的 `GET`（`/config`、`/skill`、`/plugins/enabled`）确认账号能力，再决定要不要花钱发消息。

**判断到底发生了什么，只能读 `detail`**——也就是 Agent 的原话，它是唯一可信的状态源。
`status: "pending"` 加上一段「我没有这个工具 / 我无法提交」的解释，意思是上游拒了，不是网关吞了。
注意这句话在「这一轮没产出」时是准确的，但它**不能**推出「以后也不会产出」——成品在网盘里，见上一节。

**参数为什么要填满**：插件自己的 skill 会**先问清楚没指定的项再开始生成**，而接口调用没人可答。所以少一个参数不是「走上游默认值」，而是这一轮什么都不产出。调用方没传的项会用设置里的默认值补齐（`video.defaultDuration` / `defaultRatio` / `defaultResolution`）。

**计费**：H3 和 H3 Max 消耗**账号积分**、不占 Token Plan；Hailuo 2.3 是更低成本、可用 Token Plan 的无声选项。积分用尽会由号池跳过该账号（与签到共用同一套判断）。

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
- **上游**：国际站/国内站地址、默认 Agent ID、会话与消息路径、**agent 列表 / 初始化 / 连接列表三条路径**、`model` 字段模板、默认屏幕尺寸、语言、请求超时、流空闲超时、**代理（国际站必填，见「出口 IP」）**、身份查询路径、User-Agent
- **路由**：调度策略、冷却基数/上限、最大重试次数、容量等待、粘性会话 TTL、是否优先空闲账号
- **审计**：保留天数、最大记录数、是否记录请求体、请求体长度上限
- **媒体**：生成文件目录、公开访问前缀、总容量上限、是否自动转存
- **签到**：开关、每日时刻、账号间隔、请求超时、是否跳过零积分账号、积分新鲜期、积分刷新间隔、时区偏移、浏览器指纹字段、三条端点路径
- **视频生成**：插件名（`@` 后面的引用）、参数标签名、默认画幅 / 分辨率 / 时长、单轮超时

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

网关自己不会犯这个错：**发往回环地址的上游请求一律绕过代理**（`127.0.0.0/8`、`::1`、`localhost`）。代理是用来上外网的，而 `127.0.0.1` 按定义不在外网上；把它塞过去只会拿回一个**空 body 的 `502`**，同样读起来像上游挂了。私有网段（`10.x` 等）**不**绕过——那些地址往往正是要靠代理才能到达。

### 调用流程

```
POST {base}/minimax-cloud/api/v1/agent/{agent_id}/session       → 建会话，拿 session_id
POST {stream}/minimax-cloud/api/v1/session/{session_id}/message → SSE 流，取 msg_content
```

注意两条路径**不在同一个域名上**。发消息走的是 `agent-stream.<domain>`（国际站是 `agent-stream.minimax.io`），其余所有接口都走 `agent.<domain>`。两者**不是同一个入口的两种写法**：同一条路径放在 API 域名上会进到另一个入口，那一侧**能收到消息、能正常回复，但没有渲染工具**——视频请求会变成一段「描述了这个视频」的友好文字，没有视频、也没有报错。这正是本项目在视频上反复踩的坑。

`upstream.streamBaseURL` 留空时按规律推导：`agent.x` → `agent-stream.x`；其它地址（自建中转、测试服务器）保持原样，不会被套上一个它们没有的域名。国内站走的是同一套前端，所以同样推导——这条是**外推**而非抓包实测，但推导错了会是一个响亮的 DNS 错误，不会静默成功。

三条路径都能在「系统设置 → 上游」里改（`sessionPath` / `messagePath` / `streamBaseURL`，前两者支持 `{agent_id}`、`{session_id}` 占位符）。

**`{agent_id}` 是一个数字，不是角色名。** 上游把 agent 的*角色*（`general` / `coder` / `mavis` …）和它的*编号*分得很开：编号在 agent 列表的 `name` 字段里（这个字段名起得有误导性，它装的其实是数字 id，人类可读的名字在 `display_name` 里）。把 `general` 当 id 传上去，上游会回一个 **200 但不给你 `session_id`** —— 请求看起来完全成功，实际什么都没开。所以加号时后台会调 `GET /minimax-cloud/api/v1/agent` 把这个数字问回来，存在账号上；缺它的账号会被判定为**不可调度**，不会拿一个注定空转的请求去换一个假成功。

驱动哪个角色由 `DefaultAgentRole` 决定，当前是 **`mavis`**（`general` 作为次选）：网页端抓包里开会话用的是 mavis agent，而带视频工具链的 `mcode-tools-master` skill 也发布在 `Mavis/` 目录下——两条证据指向同一个选择。这个选择是**可变的**，所以账号上缓存的编号会被重新审视：如果上游的 agent 列表证明它属于另一个角色，就换成新角色对应的编号；手填的编号（不在列表里的）不动。

### 查询参数

网页端每条业务请求都带一组固定的元数据 query，**22 个参数、顺序固定**，`unix` 在第 5 位，`client` / `region` 在最后。顺序是协议的一部分——`yy` 是对编码后 URL 取摘要，重排就等于换了一个 URL。`unix` 与 `yy` 必须来自**同一次时钟读数**：实现里两者由 `newRequest` 用同一个 `now` 一起产出，URL 组装也放在那里，这样从外部就不可能拼出「签名描述的是另一个 URL」的组合。

### 上游初始化（`/config`）

**发消息和签到之前，都必须先跑一遍 agent 侧的初始化序列**，顺序不能反：

```
GET /minimax-cloud/api/v1/config              ← 创建/激活账号在 agent 侧的用户记录
GET /minimax-cloud/api/v1/agent               ← agent 列表（顺带拿到数字 id）
GET /minimax-cloud/api/v1/channel/connections
```

两个后果，都很难自己查出来：

- **不调它，新号发消息会 500**，错误文案是 `[1400010501] Environment Variables not configured` —— 通篇没提「你没初始化」，看着像环境变量配错了。
- **顺序反了（先签到后 `/config`）积分会静默丢失，且事后补调救不回来。** 领取接口照样回 `claim_result=1`，所以失败和成功长得一模一样。所以签到流程里这一步失败会**直接跳过领取**并写明原因 —— 丢掉一天是可见、可重跑的；顺序错了则是丢掉一天还报成功。

三条路径都能在「系统设置 → 上游」里改（`configPath` / `agentListPath` / `connectionsPath`）。视频取件用的另外两条同理（`summariesPath` / `driveFilePath`，支持 `{session_id}`、`{node_id}`），详见「视频生成 → 成品不在对话流里」。

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
GET  {base}/minimax-cloud/api/v1/credit/details   → 发放明细（用于对账，见下）
```

**顺序**：每次签到先跑一遍 agent 侧初始化序列（`/config` → `/agent` → `/channel/connections`），再读面板、再领取、再读余额。这一步不是仪式感，见上面「上游初始化」。

**对账**：领取接口会回 `claim_result=1`，**不管积分有没有真的发下来**。所以领完之后会去 `credit/details` 看有没有一笔发放时间落在这次领取之后的记录——没有就标记 `unpaid` 并在控制台写明，让「报成功但没到账」这件事可见。判定看的是**发放时间**而不是余额：一笔被花到 0 的发放仍然证明积分到过账。发放列表有可见性延迟（实测超过一分钟），所以领完有一个宽限期，期内不下结论；这个标记是可撤销的，发放出现后会自动清掉。

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
  internal/minimax/     上游协议客户端（签名、会话、SSE、令牌解析、签到/积分、身份与 agent 发现）
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
  render.mjs            真实浏览器渲染 + 控制台流程检查（CDP，需本机 Chrome）
  secret_scan.py        工作树 + 全历史密钥扫描（推送前跑）
  hooks/pre-commit      提交前自动跑 secret_scan.py --tree-only
```

### 持久化设计

`data/app.json` 是唯一的状态文件。所有变更先改内存，再由**单个后台协程**防抖（40ms）后原子落盘（写临时文件 + rename）。

请求处理路径**不会**在持锁期间做磁盘 I/O，因此慢速或被占用的文件系统不会拖垮服务。运行时设置额外维护一份 `atomic.Value` 快照，使得已经持有写锁的回调（例如账号探测结果回写）也能安全读取配置——Go 的 `sync.RWMutex` 不可重入，这一点是硬性要求。

> ⚠️ **默认值是「安装时冻结」的，这一点会影响升级。**
> 全新安装会把整套默认设置写进 `app.json`，而 `Normalize` 只补**缺失**的值、不动**已存在**的值。
> 所以**改一个默认值只会影响到新安装**：存量实例的文件里躺着旧值，且它看起来完全正常，没有任何症状。
> 一个坏默认值就这样活过了一次「修好默认值」的发布。
>
> 处理办法是**迁移**：`Normalize` 里维护一张已知坏值的清单，命中就改回默认。
> 目前有三条。前两条实测不可用（一条回的是 SPA 的 HTML，一条回 200 但不开会话）；
> 第三条是另一类——它**能用**，只是进错了门，详见「调用流程」里关于两个域名的说明：
>
> | 字段 | 旧值 | 修成 |
> | --- | --- | --- |
> | `upstream.sessionPath` | `/agent/{agent_id}/session` | `/minimax-cloud/api/v1/agent/{agent_id}/session` |
> | `upstream.agentID` | `general`（这是角色名，不是编号） | 空（由发现流程填真实编号） |
> | `upstream.messagePath` | `/archon/api/v1/session/{session_id}/message` | `/minimax-cloud/api/v1/session/{session_id}/message` |
>
> 迁移结果会**写回磁盘**，不留「文件与实际生效配置不一致」的状态。
> 以后再加默认值修复，记得同时加进这张清单——否则修了等于没修。
>
> 另外 `agentID` 在**读取时**也会跳过角色名，所以即使实例还没重启过，存量的 `general` 也不会被当成编号发出去。
>
> `streamBaseURL` 没有默认值——它留空时**推导**，所以不需要迁移，也不会被冻住。

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
| `internal/store` | 配置快照的并发读写、写锁内重入读配置（死锁回归）、快照隔离、**打开旧设置文件时把已知坏值迁移并写回磁盘** |
| `internal/pool` | 账号筛选（禁用/失效/无令牌/无指纹/冷却过期）、四种调度策略、粘性会话、退避与封顶 |
| `internal/minimax` | 签名公式与逐字符的 `encodeURIComponent` 对照、query 顺序与编码、按区域选主机、SSE 帧解析（推理/正文分离、媒体收集、错误识别）、JWT 令牌解析、签到两条 query 串的差异（签名含 `op_ticket=undefined`、请求不含）、真实抓包的 `x-signature` 对照、回环地址绕过代理、生成的 `device_id` 必须是纯数字、`uuid` 是 v4 UUID、会话响应是 HTML 页面时拒绝当成 `session_id`、`/config` 返回体决定 agent id、配置默认值不得与包内常量漂移、传输层错误里不得出现令牌、**产物清单按 `created_at` 过滤出本轮的文件、`download_url` 补 scheme、取不到链接的产物不冒充媒体** |
| `internal/signin` | 错过的时间点补签、重复领取记为「已签」、国内站账号跳过、失效令牌退役、失败也占掉当天（避免上游故障变成请求循环）、积分接口故障不牵连账号健康、扫描不可重入、**初始化序列跑在领取之前且失败即跳过领取**、领取面板用上游回显的那一份、对账只认发放时间不认余额、宽限期内不下结论 |
| `internal/admin` | 设置接口逐字段与 struct 的 json tag 比对（防新设置漏接线）、生成的指纹形状、区域推断、令牌解析 |
| `internal/config` | 已知坏默认值的迁移（旧会话路径、`agentID = general`、旧消息路径）、迁移不误伤刻意的覆盖值 |
| `internal/gateway` | 端到端请求路径——鉴权、限流、故障转移、OpenAI 响应格式、流式、审计、图像、内联图片拒绝、**视频轮次带 `client_intent` 而普通对话不带**、**只存在于网盘的成品也能被取回**、取件失败不污染这一轮的结果 |
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

签到没有账号就测不了，所以这个脚本在**自己进程里**起一个假上游，把两个一次性账号的 `baseURL` 指过去，再从外部驱动真实的管理台接口。它验的是单测覆盖不到的那部分：两条 query 串在经过真实 HTTP 客户端之后仍然一条含 `op_ticket=undefined`、一条不含；余额确实是从 `op_credit_summary.total_remaining_amount` 读的（含它发字符串这件事）；零积分账号真的会离开调度池、而读数过期后又会回来；国内站账号是被跳过而不是被发错协议；agent 侧初始化序列真的跑在领取**之前**；以及 `{agent_id}` 是从上游问回来的数字而不是角色名。

> 假上游必须实现整条初始化序列（`/config`、`/agent`、`/channel/connections`）。签到流程第一步就是它，缺一个就会让每次签到都变成「初始化失败、跳过领取」——那是正确行为，但作为测试毫无信息量。这个坑踩过一次：加了初始化前置之后，`signin_e2e.py` 从 62/62 掉到 29/51，原因全在假上游。

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

它还会真的走一遍**添加账号**流程：开对话框 → 只填令牌（名称留空）→ 点保存 → 断言账号数 +1、弹窗关闭，然后把自己建的账号删掉。加这一段是因为**「路由能渲染」和「流程能用」是两件事**：一个「后端说可选、前端说必填」的分歧能让整个添加流程走不通，而所有页面照样渲染得干干净净，前三个脚本和逐页渲染检查都不会吭一声。

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

**Q：签到一直是好的，一发消息就 400 `internal error` / `errorCode 50001`？**
先查 `device_id` 是不是纯数字。上游对 `uuid` 宽容、对 `device_id` 不宽容，而非数字的 `device_id` 只让 `/minimax-cloud/…` 挂掉，`/v1/api/user/info` 和签到端点照常 200 —— 所以表现就是「签到没事、聊天全废」。这个报错不点名任何字段，看着像上游故障。老版本给两个指纹字段共用一个 32 位 hex 生成器，正是这个坑；现在 `device_id` 固定生成八位数字（`uuid` 保持带横杠的 UUID）。已有账号在号池管理里编辑一下、清空 `device_id` 让系统重新生成即可。

**Q：日志里是 `400 {"error":"internal error"...}` 或者 `400` 带一串 `status_code`？**
和 `400 invalid signature` 不是一回事：**签名错会明说 `invalid signature`**。没说的 `400` 基本是请求里的某个字段格式不对——最典型的就是 `device_id` 非数字。别回头怀疑盐值。

**Q：`/config` 是什么，为什么签到之前要调它？**
agent 侧的初始化。不调它新号发消息会 500（报 `Environment Variables not configured`，文案完全误导）；而**签到前不调它、或者顺序反了，积分会静默丢失且事后补不回来**。所以网关把这条序列放在每次签到的最前面，失败就跳过领取并写明原因。详见「上游初始化」一节。

**Q：`agentID` 填 `general` 行不行？**
不行。`general` 是 agent 的*角色*，不是*编号*。传角色名上去上游会回 **200 但不返回 `session_id`** —— 请求看起来完全成功，实际什么都没开。加号时后台会自己去 `/minimax-cloud/api/v1/agent` 把数字编号问回来，一般不用手填。老版本的默认值正是 `general`，所以存量实例的 `app.json` 里可能还留着它：现在**读取时会跳过角色名**，加载时也会把它改回空，不用手动清。

**Q：视频请求返回了一段「描述这个视频」的文字，没有视频也没有报错？**
典型的**进错门**。发消息必须走 `agent-stream.<domain>` 上的 `/minimax-cloud/api/v1/session/{id}/message`；同一条路径放在 API 域名上是另一个入口，它能收到消息、能正常回复，但那一侧的 agent **没有渲染工具**，于是它只能把「我打算生成什么」写成文字还给你。老版本的默认值正是 API 域名 + `/archon/…` 路径，所以存量实例的 `app.json` 里可能还留着它，升级时会自动迁移。想手动核对，看「系统设置 → 上游」的**对话流地址**与**消息路径**两项。

**Q：为什么 agent 用的是 `mavis` 而不是 `general`？**
两条独立证据指向它：网页端抓包里开会话用的是 mavis agent，而带视频工具链的 `mcode-tools-master` skill 也发布在 `Mavis/` 目录下。`general` 保留为次选，所以没有 mavis 的账号照常能用。账号上缓存的编号会在每次准备时重新审视：如果上游的 agent 列表证明它属于另一个角色就换掉，手填的编号不动。

**Q：升级之后，旧版本留下的一些设置好像还是老样子？**
这是**设计使然，而且是坑**。全新安装会把整套默认设置写进 `app.json`，而 `Normalize` 只补缺失的值、不动已存在的值——所以改一个默认值只影响新安装，存量实例会带着旧值继续跑，且完全没有症状。

修法是**迁移**：`Normalize` 里维护一张已知坏值的清单（当前两条：旧会话路径、`agentID = general`），命中就改回默认并**写回磁盘**。以后再加默认值修复，记得同时加进那张清单，否则修了等于没修。详见「持久化设计」。

**Q：点「保存」添加账号没反应，只弹一个「此项必填」？**
老版本的前端把「名称」当成必填挡住了，而后端在名称留空时会**按账号标识自动命名**，README 说的也是「粘令牌即可」——两边对同一个字段的要求不一致，于是照文档操作反而走不通。

现在名称是显式的**可选**字段（标签旁标了「可选」，下方说明留空会怎样），两个路径都不再拦：新建留空则自动命名，编辑留空则保持原名。填不填只影响显示，不影响能不能用。

> 这类「后端说可选、前端说必填」的分歧，路由级和字段级的检查都发现不了——每个页面都渲染正常，字段也都在。所以 `tools/render.mjs` 里补了一段**流程检查**：真的开对话框、只粘令牌、点保存，断言账号数 +1 且弹窗关闭。

**Q：配了代理之后，指向本机/内网的上游地址连不上（空 body `502`）？**
老版本会把发往回环地址的请求也塞给代理，而代理按定义到不了 `127.0.0.1`，于是返回一个空 body 的 `502`，读起来像上游挂了。现在回环地址（`127.0.0.0/8`、`::1`、`localhost`）一律绕过代理；私有网段不绕过，因为那些地址往往正是要靠代理才能到。

**Q：上游改版后全部 404 / 400？**
上游路径变了。重新抓一次包，在「系统设置 → 上游」里更新会话路径、消息路径和 `model` 字段模板，不用重新编译。

**Q：所有请求都返回 `401`，而且响应体是空的？**
十有八九不是令牌坏了，而是**出口 IP 在国内**。国际站的业务请求有地域围栏，直连一律空 body `401`，配上网关代理立刻 200。第二顺位的原因是账号缺 `user_id`（`realUserID`）——同样表现。分诊办法：签名错是 `400 invalid signature`，`401` 就是身份或出口的问题，别再回头怀疑盐值。详见「账号身份」和「出口 IP」两节。

**Q：代理配了，还是 401？**
看账号的 `user_id` 列是不是空的。国际站和国内站**都**要求它，且它不等于 JWT 里的 `user.id`（填错等于没填）。正常流程下加号时会自动从上游问回来；如果是老数据或当时没配代理导致发现失败，删掉重新加一次即可。签到遇到这种情况会**跳过并写明原因**，不会把账号误判成坏号退役。

**Q：想换调度策略？**
管理台 → 系统设置 → 路由 → 调度策略。单人用推荐 `least_inflight`，多账号均匀分摊用 `round_robin`。

**Q：`/v1/videos/generations` 返回 `status: "pending"` 且 `data` 是空的，是失败了吗？**
**不能只看 `status`。** `pending` 的准确含义是「这一轮没有产出视频」，它把两种完全不同的情况合在了一起：

1. 任务确实提交了，只是这一轮等不到成品——慢模型（`minimax-h3` / `minimax-hailuo-2-3`）官方标注 15–30 分钟，任何同步 HTTP 接口都等不到；
2. 上游**根本没有执行**——Agent 的容器里没有可用的工具通道（实测遇到过，见「视频生成」一节的实测结论）。

区别只写在 `detail` 里，也就是 Agent 自己的原话，**请读它**。如果 `detail` 在解释「我没有这个工具 / 我无法提交」，那这一轮**不会**稍后变成成功——但积分可能已经扣掉了。

顺带一个容易踩的：**别把「这一轮没产出」读成「账号没这个能力」**。账号侧的插件与技能是可以用免费 GET 直接确认的（`/plugins/enabled`、`/skill`），而且成品可能只是躺在网盘里没被取回来——见「成品不在对话流里」。

想要一个能同步拿到 mp4 的模型，用 `minimax-h3-max`（约 20 秒）。想要慢模型的话，需要的是「提交 + 稍后查询」的异步接口，目前这个网关没有——见「视频生成」一节。

**Q：视频生成请求发出去了，但 Agent 反过来问我时长/分辨率？**
说明参数没填满。插件会**先问清楚没指定的项再开始生成**，而接口调用没人可答——所以少一个参数不是走上游默认值，而是这一轮什么都不产出。给 `duration` / `ratio` / `resolution` 都传上，或者确认系统设置 →「视频生成」里的默认值不是空的。

**Q：模型页多了几个模型，是自动加的吗？**
是。`minimax-h3` / `minimax-h3-max` / `minimax-hailuo-2-3` 是内置条目，启动时会**合并**进已有目录。

这里值得说明为什么是「合并」而不是「有就用、没有才建」：全新安装会把整套内置模型写进 `app.json`，如果只在这份清单为空时才播种，那么**后续版本新增的模型只会出现在新安装上**，存量实例上毫无症状——清单看起来只是一份普通的、短一点的清单。这跟设置默认值那个坑是同一个形状，所以修法也一样：比对内置清单、补齐缺的、**写回磁盘**。已经存在的条目原样保留，你在管理台关掉的模型不会被重新打开。

**Q：生成的视频/图片下载不下来？**
转存走的是和 API **同一个代理**（`upstream.proxy`）。生成物在 MiniMax 的 CDN 上，账号的出口围栏对它同样适用——本地直连下载会失败，而且失败形态是超时或连接重置，读起来像 CDN 挂了，而不是「请求走错了出口」。回环地址同样绕过代理。

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
