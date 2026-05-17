import { useState } from "react"
import { Button } from "../components/ui/button"
import { Send, RefreshCw, Bot } from "lucide-react"
import { getAuthHeader } from "../lib/auth"
import { API_BASE } from "../lib/api"

type Msg = { role: string; content: string }

export default function TestPage() {
  const [model, setModel] = useState("MiniMax-M2.7")
  const [models, setModels] = useState<string[]>(["MiniMax-M2.7"])
  const [messages, setMessages] = useState<Msg[]>([{ role: "user", content: "你好" }])
  const [result, setResult] = useState("")
  const [loading, setLoading] = useState(false)

  const handleSend = async () => {
    setLoading(true)
    setResult("")
    try {
      const res = await fetch(`${API_BASE}/v1/chat/completions`, {
        method: "POST",
        headers: { "Content-Type": "application/json", ...getAuthHeader() },
        body: JSON.stringify({ model, messages, stream: false }),
      })
      if (!res.ok) {
        const err = await res.json().catch(() => ({}))
        const msg = err?.detail?.error?.message || err?.error?.message || `HTTP ${res.status}`
        throw new Error(msg)
      }
      const data = await res.json()
      setResult(JSON.stringify(data, null, 2))
    } catch (e: any) {
      setResult(`错误: ${e.message}`)
    } finally {
      setLoading(false)
    }
  }

  const fetchModels = async () => {
    try {
      const res = await fetch(`${API_BASE}/api/models`, { headers: getAuthHeader() })
      if (res.ok) {
        const d = await res.json()
        const ids = d.data?.map((m: any) => m.id) || d || []
        if (ids.length > 0) setModels(ids)
      }
    } catch {}
  }

  return (
    <div className="space-y-6 max-w-5xl">
      <div className="flex justify-between items-center">
        <div>
          <h2 className="text-3xl font-extrabold tracking-tight">接口测试</h2>
          <p className="text-muted-foreground mt-1">向本代理发送对话请求并查看原始响应。</p>
        </div>
        <Button variant="outline" size="sm" onClick={fetchModels}><RefreshCw className="mr-2 h-4 w-4" /> 刷新模型</Button>
      </div>

      <div className="flex gap-4 items-end flex-wrap">
        <div className="flex-1 min-w-[200px]">
          <label className="text-xs font-semibold mb-1.5 block">模型</label>
          <select value={model} onChange={e => setModel(e.target.value)}
            className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm">
            {models.map(m => <option key={m} value={m}>{m}</option>)}
          </select>
        </div>
        <div className="flex-1 min-w-[200px]">
          <label className="text-xs font-semibold mb-1.5 block">消息内容</label>
          <input type="text" value={messages[0]?.content || ""}
            onChange={e => setMessages([{ role: "user", content: e.target.value }])}
            className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
            placeholder="输入测试消息..." />
        </div>
        <Button variant="default" onClick={handleSend} disabled={loading} className="h-10">
          {loading ? <RefreshCw className="mr-2 h-4 w-4 animate-spin" /> : <Send className="mr-2 h-4 w-4" />}
          发送
        </Button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div className="rounded-xl border bg-card/40">
          <div className="flex items-center gap-2 p-4 border-b bg-muted/10">
            <Bot className="h-4 w-4 text-primary" />
            <h3 className="font-semibold text-sm">请求消息</h3>
          </div>
          <pre className="p-4 text-xs font-mono text-slate-300 bg-slate-950 min-h-[200px] overflow-auto whitespace-pre-wrap">
            {JSON.stringify({ model, messages }, null, 2)}
          </pre>
        </div>
        <div className="rounded-xl border bg-card/40">
          <div className="flex items-center gap-2 p-4 border-b bg-muted/10">
            <Bot className="h-4 w-4 text-emerald-500" />
            <h3 className="font-semibold text-sm">响应</h3>
            {loading && <RefreshCw className="h-4 w-4 animate-spin text-muted-foreground" />}
          </div>
          <pre className="p-4 text-xs font-mono text-slate-300 bg-slate-950 min-h-[200px] max-h-[500px] overflow-auto whitespace-pre-wrap">
            {result || <span className="text-muted-foreground">响应将显示在此处...</span>}
          </pre>
        </div>
      </div>
    </div>
  )
}
