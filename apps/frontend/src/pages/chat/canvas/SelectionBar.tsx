/**
 * Fixed quick-actions toolbar for the current selection (right edge, under the
 * zoom controls): one icon button per action, sections separated by
 * hairlines, a count badge on top. Same `MenuAction`s as the context menu.
 */
import { Ellipsis } from 'lucide-react'
import { cx } from '../../../ui'
import type { MenuAction } from './ContextMenu'

export function SelectionBar({ count, groups, onMore }: {
  count: number
  groups: MenuAction[][]
  /** "More actions" — opens the full context menu next to the button. */
  onMore: (anchor: DOMRect) => void
}) {
  const sections = groups.filter((g) => g.length > 0)
  return (
    <div
      role="toolbar"
      aria-orientation="vertical"
      aria-label="Selection actions"
      className="pointer-events-auto flex max-h-[calc(100dvh-240px)] w-11 animate-pop flex-col items-center gap-0.5 overflow-y-auto overscroll-contain rounded-xl border border-line bg-surface/95 p-1 shadow-pop backdrop-blur [scrollbar-width:none] pointer-coarse:w-[52px]"
    >
      <span className="grid h-6 shrink-0 min-w-6 place-items-center rounded-full bg-accent px-1.5 text-2xs font-semibold text-white tabular-nums" title={`${count} selected`}>
        {count}
      </span>
      {sections.map((items, i) => (
        <div key={i} className="flex w-full flex-col items-center gap-0.5">
          <div role="separator" className="my-0.5 h-px w-6 bg-line" />
          {items.map(({ key, label, icon: Icon, disabled, onSelect }) => (
            <button
              key={key}
              type="button"
              title={label}
              aria-label={label}
              disabled={disabled}
              onClick={(e) => {
                e.currentTarget.blur()
                onSelect()
              }}
              className={cx(
                'grid size-9 shrink-0 cursor-pointer place-items-center rounded-lg text-ink-2 outline-none transition-colors duration-100 hover:bg-app hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/40 pointer-coarse:size-11',
                disabled && 'pointer-events-none opacity-35',
              )}
            >
              {Icon && <Icon size={17} />}
            </button>
          ))}
        </div>
      ))}
      <div role="separator" className="my-0.5 h-px w-6 shrink-0 bg-line" />
      <button
        type="button"
        title="More actions"
        aria-label="More actions"
        aria-haspopup="menu"
        onClick={(e) => onMore(e.currentTarget.getBoundingClientRect())}
        className="grid size-9 shrink-0 cursor-pointer place-items-center rounded-lg text-ink-2 outline-none transition-colors duration-100 hover:bg-app hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/40 pointer-coarse:size-11"
      >
        <Ellipsis size={17} />
      </button>
    </div>
  )
}
