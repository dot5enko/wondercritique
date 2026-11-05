/**
 * Long-press / right-click menu of the canvas. Positioned at the press point
 * (clamped into the viewport), touch-sized rows on coarse pointers, closed by
 * an outside press or Escape. Items are plain actions — the canvas page
 * decides what they do (most send a chat message about the selection).
 */
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import { cx } from '../../../ui'

export interface MenuAction {
  key: string
  label: string
  icon?: LucideIcon
  hint?: string
  disabled?: boolean
  /** A toggle that is on (highlighted). */
  active?: boolean
  onSelect: () => void
}

export function ContextMenu({
  x,
  y,
  title,
  groups,
  onClose,
}: {
  x: number
  y: number
  title?: ReactNode
  /** Sections separated by hairlines; empty sections are skipped. */
  groups: MenuAction[][]
  onClose: () => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)

  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const m = 8
    const w = el.offsetWidth
    const h = el.offsetHeight
    const left = Math.max(m, Math.min(x, window.innerWidth - w - m))
    // prefer below the finger, flip above when there's no room
    const top = y + h + m > window.innerHeight ? Math.max(m, y - h) : y
    setPos({ left, top })
  }, [x, y])

  useEffect(() => {
    const onDown = (e: PointerEvent): void => {
      if (ref.current && e.target instanceof Node && !ref.current.contains(e.target)) onClose()
    }
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose()
    }
    // next tick: the press that opened the menu must not close it
    const t = window.setTimeout(() => {
      window.addEventListener('pointerdown', onDown, true)
      window.addEventListener('keydown', onKey)
    }, 0)
    ref.current?.focus()
    return () => {
      window.clearTimeout(t)
      window.removeEventListener('pointerdown', onDown, true)
      window.removeEventListener('keydown', onKey)
    }
  }, [onClose])

  const sections = groups.filter((g) => g.length > 0)
  return (
    <div
      ref={ref}
      role="menu"
      tabIndex={-1}
      onContextMenu={(e) => e.preventDefault()}
      className="fixed z-50 w-64 animate-pop rounded-xl border border-line bg-surface p-1 shadow-pop outline-none select-none"
      style={pos ?? { left: x, top: y, visibility: 'hidden' }}
    >
      {title && <div className="truncate px-2.5 pt-1.5 pb-1 text-2xs font-medium text-ink-3">{title}</div>}
      {sections.map((items, i) => (
        <div key={i}>
          {i > 0 && <div role="separator" className="mx-1 my-1 h-px bg-line" />}
          {items.map(({ key, label, icon: Icon, hint, disabled, active, onSelect }) => (
            <button
              key={key}
              type="button"
              role={active == null ? 'menuitem' : 'menuitemcheckbox'}
              aria-checked={active == null ? undefined : active}
              disabled={disabled}
              onClick={() => {
                onClose()
                onSelect()
              }}
              className={cx(
                'flex h-8 w-full cursor-pointer items-center gap-2.5 rounded-lg px-2.5 text-left text-base text-ink outline-none transition-colors duration-100 hover:bg-app focus-visible:bg-app pointer-coarse:h-11 pointer-coarse:text-sm',
                disabled && 'pointer-events-none opacity-40',
                active && 'text-accent',
              )}
            >
              {Icon && <Icon size={15} className={cx('shrink-0', active ? 'text-accent' : 'text-ink-3')} />}
              <span className="min-w-0 flex-1 truncate">{label}</span>
              {hint && <span className="text-2xs text-ink-3">{hint}</span>}
            </button>
          ))}
        </div>
      ))}
    </div>
  )
}
