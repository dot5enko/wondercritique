import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import { cx } from './cx'

export function Sidebar({
  title,
  actions,
  footer,
  children,
  className,
}: {
  title: string
  actions?: ReactNode
  footer?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <aside className={cx('flex h-full w-52 shrink-0 flex-col border-r border-line bg-sunken', className)}>
      <header className="flex h-11 shrink-0 items-center gap-2 px-3">
        <span aria-hidden className="size-5 shrink-0 rounded-[6px] bg-accent" />
        <h1 className="min-w-0 flex-1 truncate text-base font-semibold text-ink">{title}</h1>
        {actions}
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">{children}</div>
      {footer && <div className="shrink-0 border-t border-line p-2">{footer}</div>}
    </aside>
  )
}

export function SidebarSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="mb-3">
      <div className="px-2 pb-1 pt-2 text-2xs font-medium text-ink-3">{title}</div>
      <div className="space-y-0.5">{children}</div>
    </section>
  )
}

export function NavItem({
  icon: Icon,
  label,
  active = false,
  onClick,
  trail,
  className,
}: {
  icon?: LucideIcon
  label: string
  active?: boolean
  onClick?: () => void
  trail?: ReactNode
  className?: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-current={active || undefined}
      className={cx(
        'group flex h-7 w-full cursor-pointer items-center gap-2 rounded-md px-2 text-left text-base outline-none transition-colors duration-100 focus-visible:ring-2 focus-visible:ring-accent/40',
        active ? 'bg-surface text-ink shadow-xs' : 'text-ink-2 hover:bg-ink/5 hover:text-ink',
        className,
      )}
    >
      {Icon && <Icon size={15} className={cx('shrink-0', active ? 'text-ink-2' : 'text-ink-3 group-hover:text-ink-2')} />}
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {trail}
    </button>
  )
}
