/**
 * Portal-mounted modal dialog (design-system: shadow-pop panel, hairline
 * border, surface bg, 150ms pop animation). Esc / overlay click / X close;
 * body scroll is locked while any modal is open (stack-safe).
 */
import { useEffect, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { X } from 'lucide-react'
import { cx } from '../ui/cx'
import { IconButton } from '../ui/Button'

export type ModalSize = 'sm' | 'md' | 'lg' | 'xl'

const PANEL_SIZES: Record<ModalSize, string> = {
  sm: 'max-w-sm',
  md: 'max-w-lg',
  lg: 'max-w-2xl',
  xl: 'max-w-5xl',
}

export interface ModalProps {
  open: boolean
  onClose: () => void
  title?: ReactNode
  description?: ReactNode
  children?: ReactNode
  /** Sticky footer row (buttons); rendered above a hairline border. */
  footer?: ReactNode
  size?: ModalSize
  /** Set false to disable Esc/overlay dismissal (X stays). Default true. */
  dismissable?: boolean
}

/* Stack-safe body scroll lock: only the first open modal locks, the last
 * closed one unlocks. Remembers the previous inline value. */
let lockCount = 0
let prevOverflow = ''

function lockScroll(): void {
  if (lockCount === 0) {
    prevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
  }
  lockCount++
}

function unlockScroll(): void {
  lockCount = Math.max(0, lockCount - 1)
  if (lockCount === 0) document.body.style.overflow = prevOverflow
}

/* Esc closes only the topmost open modal (nested modals supported). */
const escStack: Array<() => void> = []

function handleEsc(e: KeyboardEvent): void {
  if (e.key !== 'Escape') return
  const top = escStack[escStack.length - 1]
  if (top) top()
}

let escListenerCount = 0

function useEscStack(open: boolean, close: () => void): void {
  const closeRef = useRef(close)
  useEffect(() => {
    closeRef.current = close
  })
  useEffect(() => {
    if (!open) return
    const entry = () => closeRef.current()
    escStack.push(entry)
    escListenerCount++
    if (escListenerCount === 1) document.addEventListener('keydown', handleEsc)
    return () => {
      const i = escStack.indexOf(entry)
      if (i !== -1) escStack.splice(i, 1)
      escListenerCount--
      if (escListenerCount === 0) document.removeEventListener('keydown', handleEsc)
    }
  }, [open])
}

export function Modal({ open, onClose, title, description, children, footer, size = 'md', dismissable = true }: ModalProps) {
  const panelRef = useRef<HTMLDivElement>(null)
  useEscStack(open, dismissable ? onClose : () => {})

  useEffect(() => {
    if (!open) return
    lockScroll()
    const t = window.setTimeout(() => panelRef.current?.focus(), 0)
    return () => {
      window.clearTimeout(t)
      unlockScroll()
    }
  }, [open])

  if (!open) return null

  return createPortal(
    <div className="fixed inset-0 z-50 overflow-y-auto">
      <div
        aria-hidden
        onMouseDown={(e) => {
          if (dismissable && e.target === e.currentTarget) onClose()
        }}
        className="fixed inset-0 bg-ink/25"
      />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        tabIndex={-1}
        className={cx(
          'animate-pop relative mx-auto my-[8vh] flex w-[calc(100%-2rem)] max-h-[84vh] flex-col rounded-xl border border-line bg-surface shadow-pop outline-none',
          PANEL_SIZES[size],
        )}
      >
        <div className="flex items-start justify-between gap-3 px-4 pb-3 pt-4">
          <div className="min-w-0">
            {title != null && <h2 className="text-sm font-semibold text-ink">{title}</h2>}
            {description != null && <p className="mt-0.5 text-xs text-ink-2">{description}</p>}
          </div>
          <IconButton
            icon={<X />}
            label="Close"
            size="sm"
            className="-mr-1 -mt-1 shrink-0"
            onClick={onClose}
          />
        </div>
        {children != null && <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-4 text-base text-ink-2">{children}</div>}
        {footer != null && (
          <div className="flex shrink-0 items-center justify-end gap-2 border-t border-line px-4 py-2.5">{footer}</div>
        )}
      </div>
    </div>,
    document.body,
  )
}
