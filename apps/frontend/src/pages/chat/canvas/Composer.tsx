/**
 * Floating message composer of the canvas: the selected components/groups as
 * removable chips (they travel with the message as context), an auto-growing
 * textarea (16px so iOS doesn't zoom on focus) and Send. Enter sends,
 * Shift+Enter breaks the line.
 */
import { useLayoutEffect, type RefObject } from 'react'
import { ArrowUp, X } from 'lucide-react'
import { Button, cx } from '../../../ui'

export interface ContextChip {
  key: string
  label: string
}

export function Composer({
  value,
  onChange,
  onSend,
  sending,
  error,
  chips,
  onRemoveChip,
  onClearChips,
  selectMode,
  onDoneSelecting,
  inputRef,
  placeholder,
}: {
  value: string
  onChange: (v: string) => void
  onSend: () => void
  sending: boolean
  error: string | null
  chips: ContextChip[]
  onRemoveChip: (key: string) => void
  onClearChips: () => void
  selectMode: boolean
  onDoneSelecting: () => void
  inputRef: RefObject<HTMLTextAreaElement | null>
  placeholder: string
}) {
  useLayoutEffect(() => {
    const el = inputRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 160)}px`
  }, [value, inputRef])

  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-0 z-30 flex justify-center px-3 pb-[max(0.75rem,env(safe-area-inset-bottom))]">
      <div className="pointer-events-auto flex w-full max-w-[680px] flex-col gap-1 rounded-3xl border border-line-strong bg-surface/95 p-1.5 shadow-pop backdrop-blur transition-colors focus-within:border-accent/60">
        {selectMode && (
          <div className="flex items-center gap-2 rounded-full bg-accent-soft py-1 pr-1 pl-3 text-xs text-accent-ink">
            <span className="min-w-0 flex-1">Tap components or groups to add them to the message.</span>
            <Button size="sm" variant="primary" className="rounded-full" onClick={onDoneSelecting}>
              Done
            </Button>
          </div>
        )}
        {chips.length > 0 && (
          <div className="flex flex-wrap items-center gap-1.5 px-2 pt-1">
            {chips.map((c) => (
              <span key={c.key} className="inline-flex h-6 items-center gap-1 rounded-full bg-accent-soft pr-1 pl-2.5 text-xs font-medium text-accent-ink">
                {c.label}
                <button
                  type="button"
                  aria-label={`Remove ${c.label}`}
                  onClick={() => onRemoveChip(c.key)}
                  className="grid size-4 cursor-pointer place-items-center rounded-full outline-none hover:bg-accent/15 focus-visible:ring-2 focus-visible:ring-accent/40"
                >
                  <X size={11} />
                </button>
              </span>
            ))}
            {chips.length > 1 && (
              <button type="button" onClick={onClearChips} className="cursor-pointer px-1 text-xs text-ink-3 hover:text-ink">
                Clear
              </button>
            )}
          </div>
        )}
        {error && <p className="px-3 text-xs text-danger">{error}</p>}
        <div className="flex items-end gap-1.5">
          <textarea
            ref={inputRef}
            rows={1}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault()
                onSend()
              }
            }}
            placeholder={placeholder}
            className={cx(
              'max-h-40 min-h-9 min-w-0 flex-1 resize-none bg-transparent px-3 py-1.5 text-[16px] leading-6 text-ink outline-none placeholder:text-ink-3',
            )}
          />
          <Button
            variant="primary"
            size="lg"
            icon={<ArrowUp />}
            className="shrink-0 rounded-full"
            loading={sending}
            disabled={!value.trim()}
            onClick={onSend}
            aria-label="Send"
          />
        </div>
      </div>
    </div>
  )
}
