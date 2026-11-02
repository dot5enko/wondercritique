/**
 * Authenticated app shell — viewport frame + sidebar navigation + routed
 * content. Nav sections: Workspace (Design chat — every chat is a pipeline run),
 * Configure (admin: pipelines, agents and what agents use) and Admin (settings).
 */
import { Outlet, useLocation, useNavigate } from 'react-router'
import {
  BookOpen,
  Bot,
  Layers,
  LogOut,
  MessageSquare,
  MessagesSquare,
  Settings,
  ShieldCheck,
  Workflow,
  type LucideIcon,
} from 'lucide-react'
import { useAuth } from '../features/auth'
import { Avatar, Badge, IconButton, NavItem, Sidebar, SidebarSection } from '../ui'

interface NavEntry {
  path: string
  label: string
  icon: LucideIcon
}

const NAV_MAIN: NavEntry[] = [{ path: '/chat', label: 'Design chat', icon: MessageSquare }]

/** Configure = what runs (pipelines) and who runs it (agents + what agents use). */
const NAV_CONFIGURE: NavEntry[] = [
  { path: '/studio/pipelines', label: 'Pipelines', icon: Workflow },
  { path: '/studio/agents', label: 'Agents', icon: Bot },
]

const NAV_AGENT_PARTS: NavEntry[] = [
  { path: '/studio/model-sets', label: 'Models', icon: Layers },
  { path: '/studio/skills', label: 'Skills', icon: BookOpen },
  { path: '/studio/rules', label: 'Rules', icon: ShieldCheck },
  { path: '/agent-chat', label: 'Agent chat', icon: MessagesSquare },
]

const NAV_ADMIN: NavEntry[] = [{ path: '/settings', label: 'Settings', icon: Settings }]

function NavItems({ entries, pathname }: { entries: NavEntry[]; pathname: string }) {
  const navigate = useNavigate()
  return (
    <>
      {entries.map(({ path, label, icon }) => (
        <NavItem
          key={path}
          icon={icon}
          label={label}
          active={pathname === path || pathname.startsWith(`${path}/`)}
          onClick={() => navigate(path)}
        />
      ))}
    </>
  )
}

export default function Shell() {
  const { user } = useAuth()
  const { pathname } = useLocation()
  const isAdmin = user?.role === 'admin'

  return (
    <div className="h-dvh w-dvw bg-page p-2.5">
      <div className="flex h-full w-full overflow-hidden rounded-xl border border-line bg-surface shadow-xs">
        <Sidebar title="Wonder Critique" footer={<UserFooter />}>
          <SidebarSection title="Workspace">
            <NavItems entries={NAV_MAIN} pathname={pathname} />
          </SidebarSection>

          {isAdmin && (
            <SidebarSection title="Configure">
              <NavItems entries={NAV_CONFIGURE} pathname={pathname} />
            </SidebarSection>
          )}

          {isAdmin && (
            <SidebarSection title="Used by agents">
              <NavItems entries={NAV_AGENT_PARTS} pathname={pathname} />
            </SidebarSection>
          )}

          {isAdmin && (
            <SidebarSection title="Admin">
              <NavItems entries={NAV_ADMIN} pathname={pathname} />
            </SidebarSection>
          )}
        </Sidebar>

        <main className="flex min-w-0 flex-1 flex-col">
          <div className="min-h-0 flex-1 overflow-y-auto">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
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
