import {
  createContext,
  useContext,
  useState,
  type ReactNode,
} from 'react'
import { ChevronDown, CircleAlert, CircleCheck, Info, TriangleAlert } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { cx } from './cx'

/* ---------- Badge ---------- */

export type BadgeTone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger'

const BADGE_TONES: Record<BadgeTone, string> = {
  neutral: 'bg-ink/5 text-ink-2 ring-line-strong/50',
  accent: 'bg-accent-soft text-accent-ink ring-accent/25',
  success: 'bg-success-soft text-success ring-success/25',
  warning: 'bg-warning-soft text-warning ring-warning/25',
  danger: 'bg-danger-soft text-danger ring-danger/25',
}

export function Badge({
  tone = 'neutral',
  dot = false,
  children,
  className,
}: {
  tone?: BadgeTone
  dot?: boolean
  children: ReactNode
  className?: string
}) {
  return (
    <span
      className={cx(
        'inline-flex items-center gap-1 rounded-full px-2 py-px text-2xs font-medium ring-1 ring-inset',
        BADGE_TONES[tone],
        className,
      )}
    >
      {dot && <span aria-hidden className="size-1.5 rounded-full bg-current opacity-70" />}
      {children}
    </span>
  )
}

/* ---------- Kbd & Shortcut ---------- */

export function Kbd({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <kbd
      className={cx(
        'inline-flex h-4.5 min-w-4.5 items-center justify-center rounded border border-line bg-surface px-1 font-mono text-[10px] font-medium text-ink-2 shadow-xs',
        className,
      )}
    >
      {children}
    </kbd>
  )
}

const KEY_GLYPHS: Record<string, string> = {
  mod: '⌘',
  ctrl: '⌃',
  shift: '⇧',
  alt: '⌥',
  enter: '↵',
  esc: 'Esc',
  backspace: '⌫',
  tab: '⇥',
  space: '␣',
}

export function Shortcut({ keys, className }: { keys: string[]; className?: string }) {
  return (
    <span className={cx('inline-flex items-center gap-0.5', className)}>
      {keys.map((k, i) => (
        <Kbd key={i}>{KEY_GLYPHS[k.toLowerCase()] ?? k}</Kbd>
      ))}
    </span>
  )
}

/* ---------- StatusDot ---------- */

export type StatusTone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger'

const STATUS_TONES: Record<StatusTone, string> = {
  neutral: 'bg-ink-3',
  accent: 'bg-accent',
  success: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-danger',
}

export function StatusDot({ tone = 'accent', pulse = false, className }: { tone?: StatusTone; pulse?: boolean; className?: string }) {
  return (
    <span aria-hidden className={cx('relative flex size-2 shrink-0', className)}>
      {pulse && (
        <span className={cx('absolute inline-flex size-full animate-ping rounded-full opacity-50', STATUS_TONES[tone])} />
      )}
      <span className={cx('relative inline-flex size-2 rounded-full', STATUS_TONES[tone])} />
    </span>
  )
}

/* ---------- Avatar ---------- */

const AVATAR_SIZES = {
  xs: 'size-5 text-[9px]',
  sm: 'size-6 text-2xs',
  md: 'size-7 text-xs',
}

export function Avatar({ name, size = 'sm', className }: { name: string; size?: 'xs' | 'sm' | 'md'; className?: string }) {
  const initials = name
    .split(' ')
    .map((w) => w[0])
    .filter(Boolean)
    .slice(0, 2)
    .join('')
    .toUpperCase()
  return (
    <span
      title={name}
      className={cx(
        'grid shrink-0 select-none place-items-center rounded-full bg-accent-soft font-medium text-accent-ink ring-1 ring-inset ring-accent/25',
        AVATAR_SIZES[size],
        className,
      )}
    >
      {initials}
    </span>
  )
}

/* ---------- Tooltip (CSS-only) ---------- */

export function Tooltip({
  content,
  side = 'top',
  children,
  className,
}: {
  content: ReactNode
  side?: 'top' | 'bottom' | 'right'
  children: ReactNode
  className?: string
}) {
  const sideCls = {
    top: 'bottom-full left-1/2 mb-1.5 -translate-x-1/2',
    bottom: 'top-full left-1/2 mt-1.5 -translate-x-1/2',
    right: 'left-full top-1/2 ml-1.5 -translate-y-1/2',
  }[side]
  return (
    <span className={cx('group/tt relative inline-flex', className)}>
      {children}
      <span
        role="tooltip"
        className={cx(
          'pointer-events-none absolute z-50 whitespace-nowrap rounded-md bg-ink px-2 py-1 text-2xs font-medium text-white opacity-0 shadow-pop transition-opacity duration-100 group-hover/tt:delay-300 group-hover/tt:opacity-100',
          sideCls,
        )}
      >
        {content}
      </span>
    </span>
  )
}

/* ---------- Card ---------- */

export function Card({ children, className, plain }: { children: ReactNode; className?: string; plain?: boolean }) {
  return <div className={cx('rounded-lg bg-surface', !plain && 'border border-line', className)}>{children}</div>
}

export function CardHeader({
  title,
  description,
  actions,
  className,
  plain,
}: {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  className?: string
  /** No divider, tighter padding. */
  plain?: boolean
}) {
  return (
    <div
      className={cx(
        'flex items-start justify-between gap-3',
        plain ? 'px-3 pt-2.5 pb-1' : 'border-b border-line px-4 py-3',
        className,
      )}
    >
      <div className="min-w-0">
        <p className="text-base font-semibold text-ink">{title}</p>
        {description && <p className="mt-0.5 text-xs text-ink-2">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}

export function CardBody({ children, className, plain }: { children: ReactNode; className?: string; plain?: boolean }) {
  return <div className={cx(plain ? 'px-3 pt-1 pb-3' : 'p-4', className)}>{children}</div>
}

export function CardFooter({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx('flex items-center justify-between gap-3 border-t border-line px-4 py-2.5', className)}>{children}</div>
}

/* ---------- Collapsible ---------- */

export interface CollapsibleProps {
  title: ReactNode
  trail?: ReactNode
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  children: ReactNode
  className?: string
}

export function Collapsible({ title, trail, open, defaultOpen = false, onOpenChange, children, className }: CollapsibleProps) {
  const [uncontrolled, setUncontrolled] = useState(defaultOpen)
  const isOpen = open ?? uncontrolled
  const toggle = () => {
    const next = !isOpen
    onOpenChange?.(next)
    if (open === undefined) setUncontrolled(next)
  }
  return (
    <div className={cx('rounded-lg border border-line bg-surface', className)}>
      <button
        type="button"
        aria-expanded={isOpen}
        onClick={toggle}
        className="flex w-full cursor-pointer items-center gap-2 rounded-lg px-3 py-2.5 text-left outline-none transition-colors duration-100 hover:bg-app/60 focus-visible:ring-2 focus-visible:ring-accent/40"
      >
        <ChevronDown size={14} className={cx('shrink-0 text-ink-3 transition-transform duration-150', isOpen && 'rotate-180')} />
        <span className="flex-1 truncate text-base font-medium text-ink">{title}</span>
        {trail}
      </button>
      <div className={cx('grid transition-[grid-template-rows] duration-200 ease-out', isOpen ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]')}>
        <div className="overflow-hidden">
          <div
            className={cx(
              'px-3 pb-3 text-base text-ink-2 transition-opacity duration-200',
              isOpen && 'border-t border-line pt-3 opacity-100',
              !isOpen && 'opacity-0',
            )}
          >
            {children}
          </div>
        </div>
      </div>
    </div>
  )
}

/* ---------- Accordion ---------- */

interface AccordionContextValue {
  openIds: ReadonlySet<string>
  toggle: (id: string) => void
}
const AccordionContext = createContext<AccordionContextValue | null>(null)

export function Accordion({
  children,
  multiple = false,
  defaultOpen = [],
  className,
}: {
  children: ReactNode
  multiple?: boolean
  defaultOpen?: string[]
  className?: string
}) {
  const [openIds, setOpenIds] = useState<ReadonlySet<string>>(() => new Set(defaultOpen))
  const toggle = (id: string) => {
    setOpenIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        if (!multiple) next.clear()
        next.add(id)
      }
      return next
    })
  }
  return (
    <AccordionContext.Provider value={{ openIds, toggle }}>
      <div className={cx('overflow-hidden rounded-lg border border-line bg-surface', className)}>
        <div className="divide-y divide-line">{children}</div>
      </div>
    </AccordionContext.Provider>
  )
}

export function AccordionItem({
  id,
  title,
  trail,
  children,
  className,
}: {
  id: string
  title: ReactNode
  trail?: ReactNode
  children: ReactNode
  className?: string
}) {
  const ctx = useContext(AccordionContext)
  const [selfOpen, setSelfOpen] = useState(false)
  const isOpen = ctx ? ctx.openIds.has(id) : selfOpen
  const toggle = () => (ctx ? ctx.toggle(id) : setSelfOpen((v) => !v))
  return (
    <div className={className}>
      <button
        type="button"
        aria-expanded={isOpen}
        onClick={toggle}
        className="flex w-full cursor-pointer items-center gap-2 px-3 py-2.5 text-left outline-none transition-colors duration-100 hover:bg-app/60 focus-visible:bg-app/60"
      >
        <ChevronDown size={14} className={cx('shrink-0 text-ink-3 transition-transform duration-150', isOpen && 'rotate-180')} />
        <span className="flex-1 truncate text-base font-medium text-ink">{title}</span>
        {trail}
      </button>
      <div className={cx('grid transition-[grid-template-rows] duration-200 ease-out', isOpen ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]')}>
        <div className="overflow-hidden">
          <div className={cx('px-3 pb-3 text-base text-ink-2 transition-opacity duration-200', isOpen ? 'opacity-100' : 'opacity-0')}>
            {children}
          </div>
        </div>
      </div>
    </div>
  )
}

/* ---------- Callout ---------- */

export type CalloutTone = 'info' | 'success' | 'warning' | 'danger'

const CALLOUT_TONES: Record<CalloutTone, { icon: LucideIcon; box: string; iconCls: string }> = {
  info: { icon: Info, box: 'border-accent/25 bg-accent-soft', iconCls: 'text-accent' },
  success: { icon: CircleCheck, box: 'border-success/25 bg-success-soft', iconCls: 'text-success' },
  warning: { icon: TriangleAlert, box: 'border-warning/25 bg-warning-soft', iconCls: 'text-warning' },
  danger: { icon: CircleAlert, box: 'border-danger/25 bg-danger-soft', iconCls: 'text-danger' },
}

export function Callout({
  tone = 'info',
  title,
  children,
  className,
}: {
  tone?: CalloutTone
  title?: string
  children: ReactNode
  className?: string
}) {
  const t = CALLOUT_TONES[tone]
  const Icon = t.icon
  return (
    <div className={cx('flex gap-2.5 rounded-lg border p-3 text-base leading-relaxed text-ink-2', t.box, className)}>
      <Icon size={15} className={cx('mt-0.5 shrink-0', t.iconCls)} />
      <div className="min-w-0">
        {title && <p className="font-medium text-ink">{title}</p>}
        {children}
      </div>
    </div>
  )
}

/* ---------- Progress / Skeleton / EmptyState ---------- */

export function Progress({ value, className }: { value: number; className?: string }) {
  const pct = Math.min(100, Math.max(0, value))
  return (
    <div role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} className={cx('h-1.5 overflow-hidden rounded-full bg-ink/[0.07]', className)}>
      <div className="h-full rounded-full bg-accent transition-[width] duration-500 ease-out" style={{ width: `${pct}%` }} />
    </div>
  )
}

export function Skeleton({ className }: { className?: string }) {
  return <div aria-hidden className={cx('animate-pulse rounded-md bg-ink/[0.07]', className)} />
}

export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
  className,
}: {
  icon?: LucideIcon
  title: string
  description?: string
  action?: ReactNode
  className?: string
}) {
  return (
    <div className={cx('flex flex-col items-center gap-3 px-6 py-12 text-center', className)}>
      {Icon && (
        <div className="grid size-10 place-items-center rounded-lg border border-line bg-app text-ink-3">
          <Icon size={18} />
        </div>
      )}
      <div>
        <p className="text-sm font-medium text-ink">{title}</p>
        {description && <p className="mx-auto mt-1 max-w-xs text-base text-ink-2">{description}</p>}
      </div>
      {action}
    </div>
  )
}
