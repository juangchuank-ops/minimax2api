import { useState, useEffect } from "react"
import { Settings2, RefreshCw, KeyRound, ServerCrash, Code } from "lucide-react"
import { Button } from "../components/ui/button"
import { toast } from "sonner"
import { getAuthHeader } from "../lib/auth"
import { API_BASE } from "../lib/api"

export default function SettingsPage() {
  const [sessionKey, setSessionKey] = useState("")
  const [config, setConfig] = useState<any>(null)
  const [minimaxApiKey, setMinimaxApiKey] = useState("")
  const [minimaxBaseUrl, setMinimaxBaseUrl] = useState("https://api.minimax.io/v1")
  const [defaultModel, setDefaultModel] = useState("MiniMax-M2.7")
  const [modelAliases, setModelAliases] = useState("")

  const loadSessionKey = () => {
    setSessionKey(localStorage.getItem('minimax2api_proxy_key') || "sk-minimax")
  }

  const fetchConfig = () => {
    fetch(`${API_BASE}/api/config`, { headers: getAuthHeader() })
      .then(res => {
        if (!res.ok) throw new Error("Unauthorized")
        return res.json()
      })
      .then(data => {
        setConfig(data)
        setMinimaxApiKey(data.minimax_api_key || "")
        setMinimaxBaseUrl(data.minimax_base_url || "https://api.minimax.io/v1")
        setDefaultModel(data.default_model || "MiniMax-M2.7")
        setModelAliases(JSON.stringify(data.model_aliases || {}, null, 2))
      })
      .catch(() => toast.error("配置获取失败，请检查会话 Key"))
  }

  useEffect(() => {
    loadSessionKey()
    fetchConfig()
  }, [])

  const handleSaveSessionKey = () => {
    if (!sessionKey.trim()) { toast.error("请输入 Key"); return }
    localStorage.setItem('minimax2api_proxy_key', sessionKey.trim())
    toast.success("Key 已保存到本地")
  }

  const handleClearSessionKey = () => {
    localStorage.removeItem('minimax2api_proxy_key')
    setSessionKey("")
    toast.success("Key 已清除")
  }

  const handleSaveConfig = () => {
    const data: any = { minimax_api_key: minimaxApiKey, minimax_base_url: minimaxBaseUrl, default_model: defaultModel }
    if (config?.accounts) data.accounts = config.accounts
    if (config?.proxy_api_keys) data.proxy_api_keys = config.proxy_api_keys
    fetch(`${API_BASE}/api/config`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...getAuthHeader() },
      body: JSON.stringify(data),
    }).then(r => {
      if (r.ok) { toast.success("配置已保存"); fetchConfig() }
      else toast.error("保存失败")
    }).catch(() => toast.error("保存失败"))
  }

  const handleSaveAliases = () => {
    try {
      const parsed = JSON.parse(modelAliases)
      fetch(`${API_BASE}/api/config`, {
        method: "POST",
        headers: { "Content-Type": "application/json", ...getAuthHeader() },
        body: JSON.stringify({ ...config, model_aliases: parsed }),
      }).then(r => {
        if (r.ok) { toast.success("模型映射规则已更新"); fetchConfig() }
        else toast.error("保存失败")
      })
    } catch (e) {
      toast.error("JSON 格式错误，请检查语法")
    }
  }

  const baseUrl = API_BASE || `http://${window.location.hostname}:8000`

  const curlExample = `# OpenAI 流式对话
  curl ${baseUrl}/v1/chat/completions \\
    -H "Content-Type: application/json" \\
    -H "Authorization: Bearer YOUR_API_KEY" \\
    -d '{
      "model": "MiniMax-M2.7",
      "messages": [{"role": "user", "content": "Hello"}],
      "stream": true
    }'

  # 非流式对话
  curl ${baseUrl}/v1/chat/completions \\
    -H "Content-Type: application/json" \\
    -H "Authorization: Bearer YOUR_API_KEY" \\
    -d '{
      "model": "gpt-4o",
      "messages": [{"role": "user", "content": "你好"}],
      "stream": false
    }'

  # 列出模型
  curl ${baseUrl}/v1/models \\
    -H "Authorization: Bearer YOUR_API_KEY"`

  return (
    <div className="w-full max-w-5xl mx-auto min-w-0 overflow-x-hidden space-y-6">
      <div className="flex justify-between items-center flex-wrap gap-4">
        <div className="min-w-0">
          <h2 className="text-3xl font-extrabold tracking-tight">系统设置</h2>
          <p className="text-muted-foreground mt-1">管理控制台认证与网关配置。</p>
        </div>
        <Button variant="outline" onClick={() => {fetchConfig(); toast.success("配置已刷新")}}>
          <RefreshCw className="mr-2 h-4 w-4" /> 刷新配置
        </Button>
      </div>

      <div className="grid gap-6 min-w-0">
        {/* Session Key */}
        <div className="rounded-xl border bg-card text-card-foreground shadow-sm min-w-0">
          <div className="flex flex-col space-y-1.5 p-6 border-b bg-muted/30">
            <div className="flex items-center gap-2">
              <KeyRound className="h-5 w-5 text-primary" />
              <h3 className="font-semibold leading-none tracking-tight">当前会话 Key</h3>
            </div>
            <p className="text-sm text-muted-foreground">WebUI 使用此 Key 进行管理操作（保存在浏览器本地）。</p>
          </div>
          <div className="p-6">
            <div className="flex gap-2 items-center flex-wrap">
              <input type="password" value={sessionKey}
                onChange={e => setSessionKey(e.target.value)}
                placeholder="sk-minimax 或其他代理 Key"
                className="flex h-10 flex-1 min-w-[200px] rounded-md border border-input bg-background px-3 py-2 text-sm" />
              <Button onClick={handleSaveSessionKey}>保存</Button>
              <Button variant="ghost" onClick={handleClearSessionKey}>清除</Button>
            </div>
          </div>
        </div>

        {/* Connection Info */}
        <div className="rounded-xl border bg-card text-card-foreground shadow-sm min-w-0">
          <div className="flex flex-col space-y-1.5 p-6 border-b bg-muted/30">
            <div className="flex items-center gap-2">
              <ServerCrash className="h-5 w-5 text-primary" />
              <h3 className="font-semibold leading-none tracking-tight">连接信息</h3>
            </div>
          </div>
          <div className="p-6">
            <div className="space-y-1 min-w-0">
              <label className="text-sm font-medium">API 基础地址 (Base URL)</label>
              <input type="text" readOnly value={baseUrl}
                className="flex h-10 w-full rounded-md border border-input bg-muted px-3 py-2 text-sm font-mono text-muted-foreground" />
            </div>
          </div>
        </div>

        {/* MiniMax Config */}
        <div className="rounded-xl border bg-card text-card-foreground shadow-sm min-w-0">
          <div className="flex flex-col space-y-1.5 p-6 border-b bg-muted/30">
            <div className="flex items-center gap-2">
              <Settings2 className="h-5 w-5 text-primary" />
              <h3 className="font-semibold leading-none tracking-tight">MiniMax API 配置</h3>
            </div>
            <p className="text-sm text-muted-foreground">默认 API 凭据（当账号池为空时使用）。</p>
          </div>
          <div className="p-6 space-y-4">
            <div className="flex justify-between items-center py-2 border-b flex-wrap gap-2">
              <span className="text-sm font-medium">当前系统版本</span>
              <span className="font-mono text-sm">{config?.version || "1.1.0"}</span>
            </div>
            <div className="flex flex-col gap-4">
              <div className="space-y-1">
                <label className="text-sm font-medium">默认 API Key</label>
                <input type="password" value={minimaxApiKey}
                  onChange={e => setMinimaxApiKey(e.target.value)}
                  className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm font-mono"
                  placeholder="MiniMax API Key" />
              </div>
              <div className="space-y-1">
                <label className="text-sm font-medium">Base URL</label>
                <input type="text" value={minimaxBaseUrl}
                  onChange={e => setMinimaxBaseUrl(e.target.value)}
                  className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                  placeholder="https://api.minimax.io/v1" />
              </div>
              <div className="space-y-1">
                <label className="text-sm font-medium">默认模型</label>
                <input type="text" value={defaultModel}
                  onChange={e => setDefaultModel(e.target.value)}
                  className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                  placeholder="MiniMax-M2.7" />
              </div>
            </div>
            <div className="flex justify-end pt-2">
              <Button size="sm" onClick={handleSaveConfig}>保存 MiniMax 配置</Button>
            </div>
          </div>
        </div>

        {/* Model Mapping */}
        <div className="rounded-xl border bg-card text-card-foreground shadow-sm min-w-0">
          <div className="flex flex-col space-y-1.5 p-6 border-b bg-muted/30">
            <h3 className="font-semibold leading-none tracking-tight">模型映射规则 (Model Aliases)</h3>
            <p className="text-sm text-muted-foreground">下游传入的模型名称自动路由至 MiniMax 实际模型。JSON 格式。</p>
          </div>
          <div className="p-6">
            <textarea rows={8} value={modelAliases}
              onChange={e => setModelAliases(e.target.value)}
              className="flex min-h-[160px] w-full rounded-md border border-input bg-slate-950 text-slate-300 px-3 py-2 text-sm font-mono"
              style={{ whiteSpace: "pre", overflowX: "auto" }} />
            <div className="mt-4 flex justify-end">
              <Button onClick={handleSaveAliases}>保存映射</Button>
            </div>
          </div>
        </div>

        {/* Usage Example */}
        <div className="rounded-xl border bg-card text-card-foreground shadow-sm min-w-0">
          <div className="flex flex-col space-y-1.5 p-6 border-b bg-muted/30">
            <div className="flex items-center gap-2">
              <Code className="h-5 w-5 text-primary" />
              <h3 className="font-semibold leading-none tracking-tight">使用示例</h3>
            </div>
          </div>
          <div className="p-6 min-w-0">
            <pre className="bg-slate-950 rounded-lg p-4 text-xs font-mono text-slate-300 whitespace-pre-wrap break-all max-h-[400px] overflow-y-auto overflow-x-hidden">
              {curlExample}
            </pre>
          </div>
        </div>
      </div>
    </div>
  )
}
