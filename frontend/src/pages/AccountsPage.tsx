import { useEffect, useMemo, useState } from "react"
import { Button } from "../components/ui/button"
import { Trash2, Plus, RefreshCw, ShieldCheck } from "lucide-react"
import { toast } from "sonner"
import { getAuthHeader } from "../lib/auth"
import { API_BASE } from "../lib/api"

type AccountItem = {
  api_key: string
  name: string
  base_url: string
  auth_mode: string
  auth_token: string
  is_active: boolean
  request_count: number
}

type AccountStatus = {
  name: string
  is_active: boolean
  on_cooldown: boolean
  request_count: number
  last_used: number
  auth_mode: string
  api_key_preview: string
}

function statusStyle(status: { is_active: boolean; on_cooldown: boolean }) {
  if (status.is_active && !status.on_cooldown) return "bg-green-500/10 text-green-700 dark:text-green-400 ring-green-500/20"
  if (status.on_cooldown) return "bg-yellow-500/10 text-yellow-700 dark:text-yellow-300 ring-yellow-500/20"
  return "bg-red-500/10 text-red-700 dark:text-red-400 ring-red-500/20"
}

function statusText(status: { is_active: boolean; on_cooldown: boolean }) {
  if (status.is_active && !status.on_cooldown) return "可用"
  if (status.on_cooldown) return "冷却中"
  return "已禁用"
}

export default function AccountsPage() {
  const [accounts, setAccounts] = useState<AccountItem[]>([])
  const [statuses, setStatuses] = useState<AccountStatus[]>([])
  const [apiKey, setApiKey] = useState("")
  const [name, setName] = useState("")
  const [baseUrl, setBaseUrl] = useState("https://api.minimax.io/v1")
  const [authMode, setAuthMode] = useState<"api_key" | "token">("api_key")
  const [authToken, setAuthToken] = useState("")

  const fetchData = () => {
    Promise.all([
      fetch(`${API_BASE}/api/config`, { headers: getAuthHeader() }).then(r => r.json()),
      fetch(`${API_BASE}/api/accounts/status`, { headers: getAuthHeader() }).then(r => r.ok ? r.json() : []),
    ]).then(([cfg, status]) => {
      setAccounts(cfg.accounts || [])
      setStatuses(status)
    }).catch(() => toast.error("刷新账号列表失败，请检查会话 Key"))
  }

  useEffect(() => { fetchData() }, [])

  const stats = useMemo(() => {
    const result = { valid: 0, cooldown: 0, disabled: 0 }
    for (const s of statuses) {
      if (s.is_active && !s.on_cooldown) result.valid += 1
      else if (s.on_cooldown) result.cooldown += 1
      else result.disabled += 1
    }
    return result
  }, [statuses])

  const handleAdd = () => {
    if (!apiKey.trim() && !authToken.trim()) {
      toast.error("请填写 API Key 或 Token")
      return
    }
    const newAccounts = [...accounts, {
      api_key: apiKey.trim(),
      name: name.trim() || (apiKey.trim() || authToken.trim()).slice(0, 8),
      base_url: baseUrl.trim() || "https://api.minimax.io/v1",
      auth_mode: authMode,
      auth_token: authToken.trim(),
      is_active: true,
      request_count: 0,
    }]
    const cfg = { ...{ minimax_api_key: "", proxy_api_keys: [] }, accounts: newAccounts }
    fetch(`${API_BASE}/api/config`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...getAuthHeader() },
      body: JSON.stringify(cfg),
    }).then(r => {
      if (r.ok) {
        toast.success("账号已添加")
        setApiKey(""); setName(""); setAuthToken("")
        fetchData()
      } else { toast.error("添加失败") }
    }).catch(() => toast.error("添加请求失败"))
  }

  const handleDelete = (idx: number) => {
    const newAccounts = accounts.filter((_, i) => i !== idx)
    const cfg = { ...{ minimax_api_key: "", proxy_api_keys: [] }, accounts: newAccounts }
    fetch(`${API_BASE}/api/config`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...getAuthHeader() },
      body: JSON.stringify(cfg),
    }).then(r => {
      if (r.ok) { toast.success("账号已删除"); fetchData() }
      else { toast.error("删除失败") }
    }).catch(() => toast.error("删除请求失败"))
  }

  const handleTest = (idx: number) => {
    const id = toast.loading(`正在测试账号 ${idx+1}...`)
    fetch(`${API_BASE}/api/test-account/${idx}`, {
      method: "POST",
      headers: getAuthHeader(),
    }).then(r => r.json()).then(data => {
      if (data.success) {
        toast.success(`测试成功: ${data.model}`, { id })
      } else {
        toast.error(`测试失败: ${data.error || "未知错误"}`, { id, duration: 8000 })
      }
      fetchData()
    }).catch(() => toast.error("测试请求失败", { id }))
  }

  return (
    <div className="space-y-6 relative">
      <div className="flex justify-between items-center">
        <div>
          <h2 className="text-3xl font-extrabold tracking-tight">账号管理</h2>
          <p className="text-muted-foreground mt-1">统一管理上游 MiniMax 账号池。</p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => { fetchData(); toast.success("账号列表已刷新") }}>
            <RefreshCw className="mr-2 h-4 w-4" /> 刷新状态
          </Button>
        </div>
      </div>

      <div className="grid gap-3 md:grid-cols-3">
        <div className="rounded-xl border bg-card p-4"><div className="text-sm text-muted-foreground">可用</div><div className="text-2xl font-bold">{stats.valid}</div></div>
        <div className="rounded-xl border bg-card p-4"><div className="text-sm text-muted-foreground">冷却中</div><div className="text-2xl font-bold">{stats.cooldown}</div></div>
        <div className="rounded-xl border bg-card p-4"><div className="text-sm text-muted-foreground">已禁用</div><div className="text-2xl font-bold">{stats.disabled}</div></div>
      </div>

      <div className="rounded-2xl border bg-card/40 p-6 space-y-4">
        <h3 className="text-base font-bold">手动添加账号</h3>
        <div className="flex flex-col md:flex-row gap-4 items-end">
          <div className="flex-1">
            <label className="text-xs font-semibold mb-1.5 block">名称（选填）</label>
            <input type="text" value={name} onChange={e => setName(e.target.value)} className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm" placeholder="账号名称" />
          </div>
          <div className="w-full md:w-48">
            <label className="text-xs font-semibold mb-1.5 block">认证方式</label>
            <select value={authMode} onChange={e => setAuthMode(e.target.value as "api_key" | "token")} className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm">
              <option value="api_key">🔑 API Key</option>
              <option value="token">🎫 网页 Token</option>
            </select>
          </div>
          <div className="flex-1">
            <label className="text-xs font-semibold mb-1.5 block">{authMode === "token" ? "Token（必填）" : "API Key（必填）"}</label>
            <input type="text" value={apiKey} onChange={e => setApiKey(e.target.value)} className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm" placeholder={authMode === "token" ? "localStorage._token 的值" : "MiniMax API Key"} />
          </div>
          {authMode === "token" && (
            <div className="flex-1">
              <label className="text-xs font-semibold mb-1.5 block">Token 确认</label>
              <input type="text" value={authToken} onChange={e => setAuthToken(e.target.value)} className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm" placeholder="同上" />
            </div>
          )}
          <div className="flex-1">
            <label className="text-xs font-semibold mb-1.5 block">Base URL</label>
            <input type="text" value={baseUrl} onChange={e => setBaseUrl(e.target.value)} className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm" placeholder="https://api.minimax.io/v1" />
          </div>
          <Button onClick={handleAdd} variant="secondary" className="h-10 w-full md:w-auto font-semibold">
            <Plus className="mr-2 h-4 w-4" /> 添加
          </Button>
        </div>
      </div>

      <div className="rounded-2xl border bg-card/30 overflow-hidden">
        <div className="flex items-center justify-between p-6 border-b bg-muted/10">
          <h3 className="text-xl font-bold">账号列表</h3>
          <span className="inline-flex items-center justify-center bg-primary/10 text-primary rounded-full px-3 py-1 text-xs font-bold">{accounts.length}</span>
        </div>
        <table className="w-full text-sm text-left">
          <thead className="bg-muted/30 border-b text-muted-foreground text-xs uppercase tracking-wider font-semibold">
            <tr>
              <th className="h-12 px-6 align-middle">名称</th>
              <th className="h-12 px-6 align-middle">状态</th>
              <th className="h-12 px-6 align-middle">认证</th>
              <th className="h-12 px-6 align-middle">请求数</th>
              <th className="h-12 px-6 align-middle">操作</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border/50">
            {accounts.length === 0 && (
              <tr>
                <td colSpan={5} className="px-6 py-12 text-center text-muted-foreground">暂无账号，请手动添加。</td>
              </tr>
            )}
            {accounts.map((acc, i) => {
              const s = statuses[i] || { is_active: acc.is_active, on_cooldown: false }
              return (
                <tr key={i} className="transition-colors hover:bg-black/5 dark:hover:bg-white/5">
                  <td className="px-6 py-4 align-middle font-medium font-mono text-foreground/90">{acc.name || `账号 ${i+1}`}</td>
                  <td className="px-6 py-4 align-middle">
                    <span className={`inline-flex items-center rounded-full px-2.5 py-1 text-xs font-bold ring-1 ${statusStyle(s)}`}>
                      {statusText(s)}
                    </span>
                  </td>
                  <td className="px-6 py-4 align-middle font-mono text-xs">{acc.auth_mode === "token" ? "🎫 Token" : "🔑 API Key"}</td>
                  <td className="px-6 py-4 align-middle font-mono text-xs">{acc.request_count || 0}</td>
                  <td className="px-6 py-4 align-middle text-right">
                    <div className="flex items-center justify-end gap-2">
                      <Button variant="outline" size="sm" onClick={() => handleTest(i)} title="测试此账号">
                        <ShieldCheck className="h-4 w-4 mr-1" /> 测试
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => handleDelete(i)} className="text-destructive hover:bg-destructive/10 hover:text-destructive" title="删除账号">
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}
