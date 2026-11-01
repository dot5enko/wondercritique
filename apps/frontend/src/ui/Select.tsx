import {
  Children,
  cloneElement,
  isValidElement,
  useEffect,
  useId,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactElement,
  type ReactNode,
  type RefObject,
} from 'react'
import { Check, ChevronDown } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { cx } from './cx'

/* ---------- useDismiss: outside pointerdown + Escape ---------- */

export function useDismiss(ref: RefObject<HTMLElement | null>, onDismiss: () => void, active = true) {
  useEffect(() => {
    if (!active) return
    const onPointerDown = (e: PointerEvent) => {
      if (ref.current && e.target instanceof Node && !ref.current.contains(e.target)) onDismiss()
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onDismiss()
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [ref, onDismiss, active])
}

/* ---------- Select: custom listbox with keyboard navigation ---------- */

export interface SelectOption {
  value: string
  label: string
  hint?: string
  icon?: LucideIcon
}

export interface SelectProps {
  options: SelectOption[]
  value: string | null
  onChange: (value: string) => void
  placeholder?: string
  size?: 'sm' | 'md' | 'lg'
  variant?: 'outline' | 'ghost'
  disabled?: boolean
  className?: string
  ariaLabel?: string
}

const TRIGGER_SIZES = {
  sm: 'h-6 gap-1 px-1.5 text-xs',
  md: 'h-[30px] gap-1.5 px-2 text-base',
  lg: 'h-9 gap-2 px-2.5 text-sm',
}
const ROW_SIZES = { sm: 'h-6 text-xs', md: 'h-7 text-base', lg: 'h-8 text-sm' }

export function Select({
  options,
  value,
  onChange,
  placeholder = 'Select…',
  size = 'md',
  variant = 'outline',
  disabled = false,
  className,
  ariaLabel,
}: SelectProps) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const listRef = useRef<HTMLUListElement>(null)
  const listId = useId()
  const selected = options.find((o) => o.value === value) ?? null

  useDismiss(rootRef, () => setOpen(false), open)

  useEffect(() => {
    if (!open) return
    setActive(Math.max(0, options.findIndex((o) => o.value === value)))
    listRef.current?.focus()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  useEffect(() => {
    if (open) listRef.current?.children[active]?.scrollIntoView({ block: 'nearest' })
  }, [active, open])

  const commit = (idx: number) => {
    const o = options[idx]
    if (!o) return
    onChange(o.value)
    setOpen(false)
    triggerRef.current?.focus()
  }

  const onKeyDown = (e: ReactKeyboardEvent) => {
    if (e.key === 'Escape') {
      setOpen(false)
      triggerRef.current?.focus()
      return
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const delta = e.key === 'ArrowDown' ? 1 : -1
      setActive(Math.min(options.length - 1, Math.max(0, active + delta)))
    } else if (e.key === 'Home') {
      e.preventDefault()
      setActive(0)
    } else if (e.key === 'End') {
      e.preventDefault()
      setActive(options.length - 1)
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      commit(active)
    }
  }

  const SelectedIcon = selected?.icon
  return (
    <div ref={rootRef} className={cx('relative', className)}>
      <button
        ref={triggerRef}
        type="button"
        role="combobox"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-label={ariaLabel}
        disabled={disabled}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => {
          if (!open && (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ')) {
            e.preventDefault()
            setOpen(true)
          }
        }}
        className={cx(
          'inline-flex max-w-full cursor-pointer select-none items-center justify-between rounded-md font-medium outline-none transition duration-100 active:scale-[0.98]',
          TRIGGER_SIZES[size],
          variant === 'outline'
            ? 'border border-line bg-surface text-ink hover:border-line-strong focus-visible:border-accent focus-visible:ring-2 focus-visible:ring-accent/15'
            : 'text-ink-2 hover:bg-ink/5 hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/40',
          open && variant === 'outline' && 'border-accent ring-2 ring-accent/15',
          disabled && 'pointer-events-none opacity-50',
        )}
      >
        <span className="flex min-w-0 items-center gap-1.5">
          {SelectedIcon && <SelectedIcon size={14} className="shrink-0 text-ink-3" />}
          <span className={cx('truncate', !selected && 'font-normal text-ink-3')}>
            {selected ? selected.label : placeholder}
          </span>
        </span>
        <ChevronDown size={13} className={cx('shrink-0 text-ink-3 transition-transform duration-100', open && 'rotate-180')} />
      </button>

      {open && (
        <ul
          ref={listRef}
          id={listId}
          role="listbox"
          tabIndex={-1}
          onKeyDown={onKeyDown}
          className="animate-pop absolute top-full z-40 mt-1 max-h-64 w-max min-w-full origin-top overflow-y-auto rounded-lg border border-line bg-surface p-1 shadow-pop focus:outline-none"
        >
          {options.map((o, idx) => {
            const Icon = o.icon
            const isSelected = o.value === value
            return (
              <li
                key={o.value}
                role="option"
                aria-selected={isSelected}
                onMouseEnter={() => setActive(idx)}
                onClick={() => commit(idx)}
                className={cx(
                  'flex cursor-default select-none items-center gap-2 rounded-md px-2',
                  ROW_SIZES[size],
                  idx === active && 'bg-app',
                )}
              >
                {Icon && <Icon size={14} className="shrink-0 text-ink-3" />}
                <span className="min-w-0 flex-1 truncate">{o.label}</span>
                {o.hint && <span className="text-2xs text-ink-3">{o.hint}</span>}
                {isSelected && <Check size={13} strokeWidth={2.5} className="shrink-0 text-accent" />}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

/* ---------- Menu: action dropdown around any trigger ---------- */

export interface MenuProps {
  trigger: ReactElement<any>
  align?: 'start' | 'end'
  children: ReactNode
  className?: string
}

export interface MenuItemProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  icon?: LucideIcon
  shortcut?: string
  danger?: boolean
  /** Injected by Menu: closes the popover after activation. */
  onActivate?: () => void
}

export function MenuItem({
  icon: Icon,
  shortcut,
  danger = false,
  disabled,
  onActivate,
  className,
  onClick,
  children,
  type = 'button',
  ...rest
}: MenuItemProps) {
  return (
    <button
      type={type}
      role="menuitem"
      disabled={disabled}
      onClick={(e) => {
        onClick?.(e)
        onActivate?.()
      }}
      className={cx(
        'flex h-7 w-full cursor-pointer select-none items-center gap-2 rounded-md px-2 text-left text-base outline-none transition-colors duration-100',
        danger ? 'text-danger hover:bg-danger-soft focus-visible:bg-danger-soft' : 'text-ink hover:bg-app focus-visible:bg-app',
        disabled && 'pointer-events-none opacity-40',
        className,
      )}
      {...rest}
    >
      {Icon && <Icon size={14} className={cx('shrink-0', danger ? 'text-danger/80' : 'text-ink-3')} />}
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {shortcut && <span className="text-xs tracking-wide text-ink-3">{shortcut}</span>}
    </button>
  )
}

export function MenuLabel({ children }: { children: ReactNode }) {
  return <div className="px-2 pb-0.5 pt-1.5 text-2xs font-medium text-ink-3">{children}</div>
}

export function MenuSeparator() {
  return <div role="separator" className="mx-1 my-1 h-px bg-line" />
}

function MenuImpl({ trigger, align = 'start', children, className }: MenuProps) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const popRef = useRef<HTMLDivElement>(null)
  useDismiss(rootRef, () => setOpen(false), open)

  useEffect(() => {
    if (open) popRef.current?.focus()
  }, [open])

  const close = () => {
    setOpen(false)
    rootRef.current?.querySelector<HTMLButtonElement>('button')?.focus()
  }

  const moveFocus = (dir: 1 | -1) => {
    const items = popRef.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')
    if (!items || items.length === 0) return
    const list = Array.from(items)
    const idx = list.indexOf(document.activeElement as HTMLButtonElement)
    const next = idx === -1 ? 0 : (idx + dir + list.length) % list.length
    list[next]?.focus()
  }

  return (
    <div ref={rootRef} className={cx('relative inline-flex', className)}>
      {cloneElement(trigger, {
        onClick: () => setOpen((v) => !v),
        'aria-haspopup': 'menu' as const,
        'aria-expanded': open,
      })}
      {open && (
        <div
          ref={popRef}
          role="menu"
          tabIndex={-1}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') {
              e.preventDefault()
              moveFocus(1)
            } else if (e.key === 'ArrowUp') {
              e.preventDefault()
              moveFocus(-1)
            } else if (e.key === 'Escape') {
              e.preventDefault()
              close()
            }
          }}
          className={cx(
            'animate-pop absolute top-full z-40 mt-1 flex w-max min-w-44 origin-top flex-col rounded-lg border border-line bg-surface p-1 shadow-pop focus:outline-none',
            align === 'end' ? 'right-0' : 'left-0',
          )}
        >
          {Children.map(children, (child) =>
            isValidElement(child) && (child as ReactElement<MenuItemProps>).type === MenuItem
              ? cloneElement(child as ReactElement<MenuItemProps>, { onActivate: close })
              : child,
          )}
        </div>
      )}
    </div>
  )
}

export const Menu = Object.assign(MenuImpl, { Item: MenuItem, Label: MenuLabel, Separator: MenuSeparator })

