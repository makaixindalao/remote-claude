import { lazy, Suspense, useEffect, useState } from 'react'
import { ThemeProvider } from 'next-themes'
import { SidebarInset, SidebarProvider } from '@/components/ui/sidebar'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { AppSidebar } from '@/components/app-sidebar'
import { Login } from '@/components/login'
import { StatusBar } from '@/components/status-bar'
import { DataProvider } from '@/hooks/use-data'
import { closePanel } from '@/lib/file-panel'
import { parseHash, useHash } from '@/lib/route'
import { ChatView } from '@/views/chat'
import { HomeView } from '@/views/home'
import { NewChatView } from '@/views/new-chat'
import { ProjectView } from '@/views/project'
import { SessionView } from '@/views/session'
import { SettingsView } from '@/views/settings'

// xterm 占了前端包的一大半，只在打开终端时才下载
const TermView = lazy(() => import('@/views/terminal').then((m) => ({ default: m.TermView })))

export default function App() {
  const [auth, setAuth] = useState<'checking' | 'in' | 'out'>('checking')

  useEffect(() => {
    fetch('/api/me')
      .then((r) => setAuth(r.ok ? 'in' : 'out'))
      .catch(() => setAuth('out'))
    const onUnauthorized = () => setAuth('out')
    window.addEventListener('rcweb:unauthorized', onUnauthorized)
    return () => window.removeEventListener('rcweb:unauthorized', onUnauthorized)
  }, [])

  async function logout() {
    await fetch('/api/logout', { method: 'POST' }).catch(() => {})
    setAuth('out')
  }

  return (
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
      <TooltipProvider>
        {auth === 'out' && <Login onDone={() => setAuth('in')} />}
        {auth === 'in' && (
          <DataProvider>
            <SidebarProvider className="h-dvh min-h-0">
              <AppSidebar onLogout={logout} />
              <SidebarInset className="min-h-0 min-w-0 overflow-hidden">
                <Routes />
                <StatusBar />
              </SidebarInset>
            </SidebarProvider>
          </DataProvider>
        )}
        <Toaster position="top-center" />
      </TooltipProvider>
    </ThemeProvider>
  )
}

function Routes() {
  const hash = useHash()
  const route = parseHash(hash)
  // 右侧打开的文件是按当前页面的工作目录解析的，换页面就收起来
  useEffect(() => closePanel(), [hash])
  // key 保证换一个对话 / 终端时整个视图重建，不串状态
  switch (route.name) {
    case 'new':
      return <NewChatView key={route.project ?? ''} project={route.project} />
    case 'project':
      return <ProjectView key={route.project} project={route.project} />
    case 'session':
      return <SessionView key={route.id} project={route.project} id={route.id} agent={route.agent} />
    case 'chat':
      return <ChatView key={route.id} id={route.id} />
    case 'term':
      return (
        <Suspense fallback={null}>
          <TermView key={route.session} {...route} />
        </Suspense>
      )
    case 'settings':
      return <SettingsView tab={route.tab ?? 'appearance'} />
  }
  return <HomeView />
}
