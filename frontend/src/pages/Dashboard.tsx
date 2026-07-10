import { useEffect, useState } from "react"
import { Server, Activity, ShieldAlert, FileJson, Cpu, Shield, Globe } from "lucide-react"
import { getAuthHeader } from "../lib/auth"
import { API_BASE } from "../lib/api"
import { toast } from "sonner"

type AccountStatus = {
  name: string
  is_active: boolean
  on_cooldown: boolean
  request_count: number
  last_used: number
  auth_mode: string
  api_key_preview: string
}

export default function Dashboard() {
  const [accStatus, setAccStatus] = useState<AccountStatus[]>([])
  const [models, setModels] = useState<string[]>([])
  const [errOnce, setErrOnce] = useState(false)
  const [usage, setUsage] = useState({ total_requests: 0, total_tokens: 0 })

  const fetchData = async () => {
    try {
      const [statusRes, usageRes, modelsRes] = await Promise.all([
        fetch(`${API_BASE}/api/accounts/status`, { headers: getAuthHeader() }),
        fetch(`${API_BASE}/api/usage`, { headers: getAuthHeader() }),
        fetch(`${API_BASE}/api/models`, { headers: getAuthHeader() }),
      ])
      if (statusRes.ok) setAccStatus(await statusRes.json())
      if (usageRes.ok) {
        const u = await usageRes.json()
        setUsage({ total_requests: u.total_requests || 0, total_tokens: u.total_tokens || 0 })
      }
      if (modelsRes.ok) {
        const data = await modelsRes.json()
        setModels(data.data?.map((m: any) => m.id) || data || [])
      }
    } catch {
      if (!errOnce) {
        toast.error("状态获取失败，请在「系统设置」检查您的当前会话 Key。")
        setErrOnce(true)
      }
    }
  }

  useEffect(() => {
    fetchData()
    const timer = setInterval(fetchData, 5000)
    return () => clearInterval(timer)
  }, [])

  const validAccounts = accStatus.filter(a => a.is_active && !a.on_cooldown).length
  const totalAccounts = accStatus.length

  return (
    <div className="space-y-8 max-w-5xl relative">
      <div className="relative z-10">
        <div className="absolute -top-10 -left-10 w-40 h-40 bg-primary/20 blur-[100px] pointer-events-none" />
        <h2 className="text-3xl font-extrabold tracking-tight bg-gradient-to-r from-foreground to-foreground/60 bg-clip-text text-transparent">运行状态</h2>
        <p className="text-muted-foreground mt-2 text-lg">MiniMax 代理全局状态概览（每 5 秒自动刷新）。</p>
      </div>

      <div className="grid gap-6 md:grid-cols-2 lg:grid-cols-4 relative z-10">
        <StatCard icon={<Server className="h-5 w-5 text-primary" />} title="可用账号" value={String(validAccounts)} accent="primary" sub={`共 ${totalAccounts} 个`} />
        <StatCard icon={<Activity className="h-5 w-5 text-blue-400" />} title="总请求数" value={String(usage.total_requests)} accent="blue" sub="累计" />
        <StatCard icon={<ShieldAlert className="h-5 w-5 text-destructive" />} title="总 Tokens" value={String(usage.total_tokens)} accent="destructive" sub="累计消耗" />
        <StatCard icon={<Shield className="h-5 w-5 text-orange-400" />} title="可用模型" value={String(models.length)} accent="orange" sub="自动发现" />
      </div>

      {accStatus.length > 0 && (
        <div className="border border-border/50 bg-card/30 backdrop-blur-xl shadow-2xl relative overflow-hidden">
          <div className="absolute inset-0 bg-gradient-to-b from-black/[0.02] dark:from-white/[0.02] to-transparent pointer-events-none" />
          <div className="flex flex-col space-y-2 p-6 border-b border-border/50 bg-muted/10 relative z-10">
            <h3 className="font-extrabold text-xl tracking-tight flex items-center gap-3">
              <span className="bg-primary w-1 h-8 shadow-[0_0_10px_rgba(6,182,212,0.5)]"></span>
              账号详情
            </h3>
          </div>
          <div className="overflow-x-auto relative z-10">
            <table className="w-full text-sm">
              <thead className="bg-muted/20 text-xs uppercase text-muted-foreground">
                <tr>
                  <th className="text-left px-6 py-3 font-semibold">名称</th>
                  <th className="text-left px-4 py-3 font-semibold">状态</th>
                  <th className="text-right px-4 py-3 font-semibold">请求数</th>
                  <th className="text-right px-4 py-3 font-semibold">最后使用</th>
                  <th className="text-left px-4 py-3 font-semibold">认证方式</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/40">
                {accStatus.map((a, i) => {
                  const badge = a.is_active && !a.on_cooldown ? "bg-emerald-500/15 text-emerald-300 ring-emerald-500/30"
                              : a.on_cooldown ? "bg-orange-500/15 text-orange-300 ring-orange-500/30"
                              : "bg-red-500/15 text-red-300 ring-red-500/30"
                  const statusText = a.is_active && !a.on_cooldown ? "正常"
                                   : a.on_cooldown ? "冷却中"
                                   : "已禁用"
                  return (
                    <tr key={i} className="hover:bg-muted/10 transition-colors">
                      <td className="px-6 py-3 font-mono text-xs text-foreground/80">{a.name || a.api_key_preview || `账号 ${i+1}`}</td>
                      <td className="px-4 py-3">
                        <span className={`inline-flex items-center px-2 py-0.5 text-xs font-bold ring-1 ${badge}`}>{statusText}</span>
                      </td>
                      <td className="px-4 py-3 text-right font-mono">{a.request_count}</td>
                      <td className="px-4 py-3 text-right font-mono text-xs text-muted-foreground">
                        {a.last_used ? new Date(a.last_used * 1000).toLocaleString('zh-CN') : '从未'}
                      </td>
                      <td className="px-4 py-3 text-left font-mono text-xs">{a.auth_mode === 'token' ? '🎫 Token' : '🔑 API Key'}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <div className="border border-border/50 bg-card/30 backdrop-blur-xl shadow-2xl relative overflow-hidden">
        <div className="absolute inset-0 bg-gradient-to-b from-black/[0.02] dark:from-white/[0.02] to-transparent pointer-events-none" />
        <div className="flex flex-col space-y-2 p-8 border-b border-border/50 bg-muted/10 relative z-10">
          <h3 className="font-extrabold text-2xl tracking-tight flex items-center gap-3">
            <span className="bg-primary w-1 h-8 shadow-[0_0_10px_rgba(6,182,212,0.5)]"></span>
            API 接口
          </h3>
          <p className="text-base text-muted-foreground ml-5">兼容 OpenAI 协议的调用入口。</p>
        </div>
        <div className="p-0 relative z-10">
          <div className="divide-y divide-border/50 text-sm">
            <EndpointRow icon={<FileJson className="h-5 w-5 text-emerald-500 dark:text-emerald-400" />} iconBg="bg-emerald-500/10" path="POST /v1/chat/completions" tag="Chat" tagColor="emerald" />
            <EndpointRow icon={<Cpu className="h-5 w-5 text-blue-500 dark:text-blue-400" />} iconBg="bg-blue-500/10" path="GET /v1/models" tag="Models" tagColor="blue" />
            <EndpointRow icon={<Globe className="h-5 w-5 text-cyan-500 dark:text-cyan-400" />} iconBg="bg-cyan-500/10" path="GET /" tag="WebUI" tagColor="cyan" />
            <EndpointRow icon={<Shield className="h-5 w-5 text-slate-600 dark:text-slate-400" />} iconBg="bg-slate-500/10" path="GET /health" tag="健康检查" tagColor="slate" />
          </div>
        </div>
      </div>
    </div>
  )
}

function StatCard({ icon, title, value, accent, sub }: { icon: React.ReactNode; title: string; value: string; accent: string; sub?: string }) {
  const shadowMap: Record<string, string> = {
    primary: "hover:shadow-primary/5",
    blue: "hover:shadow-blue-500/5",
    destructive: "hover:shadow-destructive/10",
    orange: "hover:shadow-orange-500/5",
  }
  const gradMap: Record<string, string> = {
    primary: "from-primary/10",
    blue: "from-blue-500/10",
    destructive: "from-destructive/10",
    orange: "from-orange-500/10",
  }
  return (
    <div className={`group border border-border/50 bg-card/40 backdrop-blur-md shadow-xl ${shadowMap[accent]} transition-all duration-500 overflow-hidden relative`}>
      <div className={`absolute inset-0 bg-gradient-to-br ${gradMap[accent]} to-transparent opacity-0 group-hover:opacity-100 transition-opacity duration-500`} />
      <div className="p-6 relative z-10">
        <div className="flex flex-row items-center justify-between space-y-0 pb-4">
          <h3 className="tracking-tight text-sm font-semibold text-foreground/80 uppercase">{title}</h3>
          <div className="p-2 bg-primary/10">{icon}</div>
        </div>
        <div className="text-4xl font-black bg-gradient-to-r from-foreground to-foreground/70 bg-clip-text text-transparent">
          {value}
        </div>
        {sub ? <div className="text-xs text-muted-foreground mt-2">{sub}</div> : null}
      </div>
    </div>
  )
}

function EndpointRow({ icon, iconBg, path, tag, tagColor }: { icon: React.ReactNode; iconBg: string; path: string; tag: string; tagColor: string }) {
  return (
    <div className="flex justify-between items-center px-8 py-5 hover:bg-black/5 dark:hover:bg-white/[0.02] transition-colors">
      <div className="flex items-center gap-4">
        <div className={`p-2 ${iconBg}`}>{icon}</div>
        <div className="font-semibold text-foreground/80">{path}</div>
      </div>
      <span className={`inline-flex items-center px-3 py-1 text-xs font-bold bg-${tagColor}-500/10 text-${tagColor}-600 dark:bg-${tagColor}-500/20 dark:text-${tagColor}-300 ring-1 ring-${tagColor}-500/20 dark:ring-${tagColor}-500/30`}>{tag}</span>
    </div>
  )
}
