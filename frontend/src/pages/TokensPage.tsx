import { useState, useEffect } from "react"
import { Button } from "../components/ui/button"
import { Plus, RefreshCw, Copy, Check, Trash2, KeyRound } from "lucide-react"
import { toast } from "sonner"
import { getAuthHeader } from "../lib/auth"
import { API_BASE } from "../lib/api"

export default function TokensPage() {
  const [proxyKeys, setProxyKeys] = useState<string[]>([])
  const [newKeyInput, setNewKeyInput] = useState("")
  const [copied, setCopied] = useState<string | null>(null)

  const fetchConfig = () => {
    fetch(`${API_BASE}/api/config`, { headers: getAuthHeader() })
      .then(res => {
        if (!res.ok) throw new Error("Unauthorized")
        return res.json()
      })
      .then(data => setProxyKeys(data.proxy_api_keys || []))
      .catch(() => toast.error("刷新失败，请检查会话 Key"))
  }

  useEffect(() => { fetchConfig() }, [])

  const saveKeys = (keys: string[]) => {
    fetch(`${API_BASE}/api/config`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...getAuthHeader() },
      body: JSON.stringify({ proxy_api_keys: keys }),
    }).then(r => {
      if (r.ok) { setProxyKeys(keys); toast.success("已保存") }
      else { toast.error("保存失败") }
    }).catch(() => toast.error("保存失败"))
  }

  const handleAdd = () => {
    const key = newKeyInput.trim()
    if (!key) { toast.error("请输入 Key"); return }
    if (proxyKeys.includes(key)) { toast.error("Key 已存在"); return }
    saveKeys([...proxyKeys, key])
    setNewKeyInput("")
  }

  const handleDelete = (key: string) => {
    saveKeys(proxyKeys.filter(k => k !== key))
  }

  const handleGenerate = () => {
    const newKey = "sk-" + Array.from({ length: 24 }, () =>
      "abcdefghijklmnopqrstuvwxyz0123456789"[Math.floor(Math.random() * 36)]
    ).join("")
    saveKeys([...proxyKeys, newKey])
    copyToClipboard(newKey)
  }

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text)
    setCopied(text)
    setTimeout(() => setCopied(null), 2000)
  }

  return (
    <div className="space-y-6 max-w-4xl">
      <div className="flex justify-between items-center">
        <div>
          <h2 className="text-3xl font-extrabold tracking-tight">API Key 分发</h2>
          <p className="text-muted-foreground mt-1">管理可以访问此网关的下游凭证。</p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => { fetchConfig(); toast.success("已刷新") }}>
            <RefreshCw className="mr-2 h-4 w-4" /> 刷新
          </Button>
          <Button onClick={handleGenerate}>
            <Plus className="mr-2 h-4 w-4" /> 生成新 Key
          </Button>
        </div>
      </div>

      <div className="rounded-2xl border bg-card/40 p-6 space-y-4">
        <h3 className="text-base font-bold flex items-center gap-2"><KeyRound className="h-4 w-4" /> 手动添加 Key</h3>
        <div className="flex gap-4 items-end">
          <div className="flex-1">
            <input type="text" value={newKeyInput} onChange={e => setNewKeyInput(e.target.value)}
              className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm font-mono"
              placeholder="输入新的代理 Key (如 sk-my-key)" />
          </div>
          <Button onClick={handleAdd} variant="secondary" className="h-10">添加</Button>
        </div>
      </div>

      <div className="rounded-xl border bg-card overflow-hidden">
        <div className="flex items-center justify-between p-6 border-b bg-muted/10">
          <h3 className="text-xl font-bold">Key 列表</h3>
          <span className="inline-flex items-center justify-center bg-primary/10 text-primary rounded-full px-3 py-1 text-xs font-bold">{proxyKeys.length}</span>
        </div>
        <table className="w-full text-sm text-left">
          <thead className="bg-muted/50 border-b text-muted-foreground">
            <tr>
              <th className="h-12 px-4 align-middle font-medium w-16">序号</th>
              <th className="h-12 px-4 align-middle font-medium">API Key</th>
              <th className="h-12 px-4 align-middle font-medium text-right">操作</th>
            </tr>
          </thead>
          <tbody>
            {proxyKeys.length === 0 && (
              <tr>
                <td colSpan={3} className="p-4 text-center text-muted-foreground">暂无 API Key</td>
              </tr>
            )}
            {proxyKeys.map((k, i) => (
              <tr key={k} className="border-b transition-colors hover:bg-muted/50">
                <td className="p-4 align-middle font-medium text-muted-foreground">{i + 1}</td>
                <td className="p-4 align-middle font-mono text-xs">{k}</td>
                <td className="p-4 align-middle text-right space-x-2">
                  <Button variant="ghost" size="sm" onClick={() => copyToClipboard(k)}>
                    {copied === k ? <Check className="h-4 w-4 text-green-600" /> : <Copy className="h-4 w-4" />}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => handleDelete(k)} className="text-destructive hover:bg-destructive/10 hover:text-destructive">
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
