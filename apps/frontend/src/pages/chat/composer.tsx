/**
 * Chat composer — mirrors the showcase composer: bordered rounded box, plain
 * textarea auto-growing to ~6 rows, Enter sends / Shift+Enter newline,
 * Send ↔ Stop while a run is streaming.
 */
import { useLayoutEffect, useRef, type KeyboardEvent } from 'react'
import { Send, Square } from 'lucide-react'
import { Button, Kbd } from '../../ui'

/** ~6 rows at the 13px base (21px line-height) + vertical padding. */
const MAX_HEIGHT = 136

export interface ComposerProps {
  value: string
  onChange: (value: string) => void
  onSend: () => void
  onStop: () => void
  streaming: boolean
  /** Extra disable (e.g. history still loading) — input can't be sent into an unknown session. */
  disabled?: boolean
  placeholder?: string
}

export function Composer({ value, onChange, onSend, onStop, streaming, disabled = false, placeholder }: ComposerProps) {
  const ref = useRef<HTMLTextAreaElement>(null)

  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, MAX_HEIGHT)}px`
  }, [value])

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== 'Enter' || e.shiftKey || e.nativeEvent.isComposing) return
    e.preventDefault()
    if (!streaming && !disabled && value.trim()) onSend()
  }

  return (
    <div className="shrink-0 border-t border-line bg-surface p-3">
      <div className="rounded-lg border border-line bg-surface transition duration-100 focus-within:border-accent focus-within:ring-2 focus-within:ring-accent/15">
        <textarea
          ref={ref}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder={placeholder}
          rows={1}
          disabled={streaming || disabled}
          className="block max-h-[136px] w-full resize-none overflow-y-auto rounded-t-lg bg-transparent px-3 pt-2.5 text-base text-ink outline-none placeholder:text-ink-3 disabled:opacity-60"
        />
        <div className="flex items-center justify-between gap-2 px-2 pb-2">
          <span className="hidden items-center gap-1 text-2xs text-ink-3 sm:flex">
            <Kbd>↵</Kbd> send · <Kbd>⇧↵</Kbd> newline
          </span>
          {streaming ? (
            <Button variant="primary" size="sm" icon={<Square />} onClick={onStop}>
              Stop
            </Button>
          ) : (
            <Button variant="primary" size="sm" iconRight={<Send />} onClick={onSend} disabled={!value.trim() || disabled}>
              Send
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}
