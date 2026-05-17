import { Outlet, Link, useLocation } from "react-router-dom"
import { Activity, Key, Settings, LayoutDashboard, MessageSquare, Menu, X } from "lucide-react"
import { useState } from "react"

export default function AdminLayout() {
  const loc = useLocation()
  const [mobileOpen, setMobileOpen] = useState(false)

  const navs = [
    { name: "运行状态", path: "/", icon: LayoutDashboard },
    { name: "账号管理", path: "/accounts", icon: Activity },
    { name: "API Key", path: "/tokens", icon: Key },
    { name: "接口测试", path: "/test", icon: MessageSquare },
    { name: "系统设置", path: "/settings", icon: Settings },
  ]

  return (
    <div className="flex min-h-screen w-full bg-background text-foreground transition-colors duration-300">
      {/* Mobile sidebar backdrop */}
      {mobileOpen && (
        <div 
          className="fixed inset-0 bg-black/20 dark:bg-black/50 z-40 md:hidden backdrop-blur-sm transition-opacity"
          onClick={() => setMobileOpen(false)}
        />
      )}

      <aside className={`fixed md:static inset-y-0 left-0 w-64 flex-col border-r border-border/40 bg-card/90 md:bg-card/50 backdrop-blur-xl flex z-50 shadow-2xl shadow-black/5 dark:shadow-black/50 transition-transform duration-300 ${
        mobileOpen ? "translate-x-0" : "-translate-x-full md:translate-x-0"
      }`}>
        <div className="h-16 flex items-center justify-between px-6 border-b border-border/40">
          <div className="flex items-center gap-2.5">
            <div className="w-7 h-7 rounded-none bg-gradient-to-br from-cyan-400 to-teal-500 flex items-center justify-center shadow-lg shadow-cyan-500/20">
              <span className="text-white font-black text-xs leading-none">M</span>
            </div>
            <div>
              <div className="font-extrabold text-base tracking-tight text-foreground leading-tight">MiniMax</div>
              <div className="text-[10px] text-muted-foreground font-medium tracking-widest uppercase leading-none">代理网关</div>
            </div>
          </div>
          <button className="md:hidden text-muted-foreground hover:text-foreground transition-colors" onClick={() => setMobileOpen(false)}>
            <X className="h-5 w-5" />
          </button>
        </div>
        <nav className="flex-1 space-y-2 p-4">
          {navs.map(n => {
            const active = loc.pathname === n.path
            return (
              <Link
                key={n.path}
                to={n.path}
                onClick={() => setMobileOpen(false)}
                className={`flex items-center gap-3 px-3 py-2.5 text-sm font-medium transition-all duration-300 ${
                  active 
                  ? "bg-primary/10 text-primary shadow-[inset_0_1px_0_0_rgba(0,0,0,0.05)] dark:shadow-[inset_0_1px_0_0_rgba(255,255,255,0.1)] ring-1 ring-primary/20 border-l-2 border-primary pl-[10px]" 
                  : "text-muted-foreground hover:bg-black/5 dark:hover:bg-white/5 hover:text-foreground border-l-2 border-transparent"
                }`}
              >
                <n.icon className={`h-4 w-4 ${active ? "drop-shadow-[0_0_8px_rgba(6,182,212,0.5)]" : ""}`} />
                {n.name}
              </Link>
            )
          })}
        </nav>
      </aside>

      <main className="flex-1 flex flex-col overflow-hidden relative">
         <header className="h-16 flex items-center justify-between px-6 border-b border-border/40 bg-card/80 backdrop-blur-xl md:hidden z-10 shadow-sm">
            <div className="flex items-center gap-2.5">
              <div className="w-6 h-6 rounded-none bg-gradient-to-br from-cyan-400 to-teal-500 flex items-center justify-center">
                <span className="text-white font-black text-[10px] leading-none">M</span>
              </div>
              <span className="font-extrabold text-base tracking-tight text-foreground">MiniMax</span>
            </div>
            <button className="text-muted-foreground hover:text-foreground transition-colors" onClick={() => setMobileOpen(true)}>
              <Menu className="h-6 w-6" />
            </button>
         </header>
        <div className="flex-1 p-6 md:p-8 overflow-y-auto overflow-x-hidden z-0">
          <div className="max-w-6xl mx-auto min-w-0 animate-fade-in-up">
            <Outlet />
          </div>
        </div>
      </main>
    </div>
  )
}
