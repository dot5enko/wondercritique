/**
 * Authenticated app shell — viewport frame + sidebar + routed content.
 *
 * The sidebar is always exactly one level deep and has two modes:
 *  - **Chats** (everything outside settings): "New chat" + the list of design
 *    chats; footer = Settings entry (admin) + signed-in user.
 *  - **Settings** (`/studio/*`, `/settings/*`, admin only): "Back to chats" +
 *    a flat list of configuration pages.
 */
import { useCallback, useMemo, useState, type ReactNode } from 'react'
import { Outlet, useLocation, useNavigate, useOutletContext, useSearchParams } from 'react-router'
import {
  ArrowLeft,
  BookOpen,
  Bot,
  KeyRound,
  Layers,
  LogOut,
  PanelLeftClose,
  Settings,
  ShieldCheck,
  SquarePen,
  Workflow,
  type LucideIcon,
} from 'lucide-react'
import { useAuth } from '../features/auth'
import { Avatar, Badge, IconButton, NavItem, Sidebar, SidebarSection } from '../ui'
import { ThreadList } from './chat/rail'
import { ThreadsProvider, useThreads } from './chat/threads'

interface NavEntry {
  path: string
  label: string
  icon: LucideIcon
}

/** Settings sidebar: what runs (pipelines), who runs it (agents) and what they use. */
const NAV_SETTINGS: NavEntry[] = [
  { path: '/studio/pipelines', label: 'Pipelines', icon: Workflow },
  { path: '/studio/agents', label: 'Agents', icon: Bot },
  { path: '/studio/model-sets', label: 'Models', icon: Layers },
  { path: '/settings/providers', label: 'Providers', icon: KeyRound },
  { path: '/studio/skills', label: 'Skills', icon: BookOpen },
  { path: '/studio/rules', label: 'Rules', icon: ShieldCheck },
]

/** Where the "Settings" entry lands. */
const SETTINGS_HOME = NAV_SETTINGS[0].path

function isSettingsPath(pathname: string): boolean {
  return pathname.startsWith('/studio/') || pathname === '/settings' || pathname.startsWith('/settings/')
}

const SIDEBAR_COLLAPSED_KEY = 'wc_sidebar_collapsed'

/** Layout state the Shell exposes to routed pages (via Outlet context). */
export interface ShellLayout {
  /** Sidebar hidden entirely; the page must render an expand control (`toggleSidebar`). */
  sidebarCollapsed: boolean
  toggleSidebar: () => void
}

/** Read the Shell layout state from a routed page. */
export function useShellLayout(): ShellLayout {
  return useOutletContext<ShellLayout | undefined>() ?? { sidebarCollapsed: false, toggleSidebar: () => {} }
}

export default function Shell() {
  const { user } = useAuth()
  const { pathname } = useLocation()
  const [params] = useSearchParams()
  const settingsMode = user?.role === 'admin' && isSettingsPath(pathname)
  // Settings pages share a 1100px content column; the pipeline graph editor stays full-bleed.
  const constrained = settingsMode && !(pathname.startsWith('/studio/pipelines') && params.get('id'))

  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try {
      return localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === '1'
    } catch {
      return false
    }
  })
  const toggleSidebar = useCallback((): void => {
    setCollapsed((prev) => {
      const next = !prev
      try {
        localStorage.setItem(SIDEBAR_COLLAPSED_KEY, next ? '1' : '0')
      } catch {
        // private mode etc. — state still toggles for this session
      }
      return next
    })
  }, [])
  // Only the chat page can collapse the sidebar: it hosts the expand button in its title bar.
  const canCollapse = !settingsMode && pathname === '/chat'
  const hidden = canCollapse && collapsed
  const layout = useMemo<ShellLayout>(() => ({ sidebarCollapsed: hidden, toggleSidebar }), [hidden, toggleSidebar])

  return (
    <ThreadsProvider>
      <div className="h-dvh w-dvw bg-page p-2.5">
        <div className="flex h-full w-full overflow-hidden rounded-xl border border-line bg-surface shadow-xs">
          {/* Sidebar stays mounted; collapse animates the wrapper width to 0 (content keeps
              its fixed w-60 so it slides out instead of reflowing). */}
          <div
            inert={hidden}
            aria-hidden={hidden || undefined}
            className={`h-full shrink-0 overflow-hidden transition-[width] duration-200 ease-out motion-reduce:transition-none ${hidden ? 'w-0' : 'w-60'}`}
          >
            {settingsMode ? (
              <SettingsSidebar pathname={pathname} />
            ) : (
              <ChatsSidebar
                pathname={pathname}
                collapseButton={
                  canCollapse && (
                    <IconButton icon={<PanelLeftClose />} label="Collapse sidebar" size="sm" onClick={toggleSidebar} />
                  )
                }
              />
            )}
          </div>

          <main className="flex min-w-0 flex-1 flex-col">
            <div className="min-h-0 flex-1 overflow-y-auto">
              <div className={constrained ? 'mx-auto h-full w-full max-w-[1100px]' : 'h-full'}>
                <Outlet context={layout} />
              </div>
            </div>
          </main>
        </div>
      </div>
    </ThreadsProvider>
  )
}

function ChatsSidebar({ pathname, collapseButton }: { pathname: string; collapseButton: ReactNode }) {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const { user } = useAuth()
  const { query, threads } = useThreads()
  const onChat = pathname === '/chat'
  const activeId = onChat ? params.get('thread') : null

  return (
    <Sidebar
      title="Wonder Critique"
      className="w-60"
      actions={
        <>
          <IconButton icon={<SquarePen />} label="New chat" size="sm" onClick={() => navigate('/chat')} />
          {collapseButton}
        </>
      }
      footer={
        <div className="space-y-1">
          {user?.role === 'admin' && (
            <NavItem icon={Settings} label="Settings" onClick={() => navigate(SETTINGS_HOME)} />
          )}
          <UserFooter />
        </div>
      }
    >
      <SidebarSection title="Chats">
        <ThreadList
          threads={threads}
          loading={query.loading && query.data == null}
          error={query.error}
          activeId={activeId}
          onSelect={(id) => navigate(`/chat?thread=${encodeURIComponent(id)}`)}
          onRetry={query.reload}
        />
      </SidebarSection>
    </Sidebar>
  )
}

function SettingsSidebar({ pathname }: { pathname: string }) {
  const navigate = useNavigate()
  return (
    <Sidebar title="Settings" className="w-60" footer={<UserFooter />}>
      <div className="mb-2 pt-1">
        <NavItem icon={ArrowLeft} label="Back to chats" onClick={() => navigate('/chat')} />
      </div>
      <div className="space-y-0.5">
        {NAV_SETTINGS.map(({ path, label, icon }) => (
          <NavItem
            key={path}
            icon={icon}
            label={label}
            active={pathname === path || pathname.startsWith(`${path}/`)}
            onClick={() => navigate(path)}
          />
        ))}
      </div>
    </Sidebar>
  )
}

/** Sidebar footer with the signed-in user; rendered by Shell via Sidebar's footer slot. */
export function UserFooter() {
  const { user, logout } = useAuth()
  if (!user) return null
  return (
    <div className="flex items-center gap-2 px-1 py-1">
      <Avatar name={user.name || user.email} size="sm" />
      <div className="min-w-0 flex-1 leading-tight">
        <p className="truncate text-base font-medium text-ink">{user.name || user.email}</p>
        <p className="truncate text-2xs text-ink-3">{user.email}</p>
      </div>
      <Badge tone={user.role === 'admin' ? 'accent' : 'neutral'}>{user.role}</Badge>
      <IconButton icon={<LogOut />} label="Log out" size="sm" onClick={() => void logout()} />
    </div>
  )
}
