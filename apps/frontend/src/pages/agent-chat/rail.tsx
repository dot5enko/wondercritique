/**
 * Chat session rail (w-64, sunken) — agent picker at the top (admins with
 * studio defs: Select over published agents; otherwise a mono slug input
 * applied on Enter), "New chat" and the session list.
 */
import { useEffect, useMemo, useState } from 'react'
import { Plus, RotateCw, Trash2 } from 'lucide-react'
import { Button, IconButton, Input, Select, Skeleton } from '../../ui'
import { cx } from '../../ui'
import { relativeTime } from '../../components'
import type { AgentSession, StudioDef } from '../../api'

export interface SessionRailProps {
  slug: string
  /** Published agent defs for the picker; null when unavailable (non-admin / studio API missing). */
  defs: StudioDef<'agent'>[] | null
  defsLoading: boolean
  sessions: AgentSession[]
  sessionsLoading: boolean
  sessionsError: unknown
  activeId: string | null
  creating: boolean
  /** Create/delete failure message, surfaced at the rail bottom. */
  actionError: string | null
  onSlugChange: (slug: string) => void
  onNew: () => void
  onSelect: (id: string) => void
  onDeleteRequest: (session: AgentSession) => void
  onRetrySessions: () => void
}

export function SessionRail({
  slug,
  defs,
  defsLoading,
  sessions,
  sessionsLoading,
  sessionsError,
  activeId,
  creating,
  actionError,
  onSlugChange,
  onNew,
  onSelect,
  onDeleteRequest,
  onRetrySessions,
}: SessionRailProps) {
  const picker = defs !== null && defs.length > 0
  return (
    <aside className="flex h-full w-64 shrink-0 flex-col border-r border-line bg-sunken">
      <div className="flex h-11 shrink-0 items-center border-b border-line px-2">
        {defsLoading ? (
          <Skeleton className="h-6 flex-1" />
        ) : picker ? (
          <AgentSelect slug={slug} defs={defs} onChange={onSlugChange} />
        ) : (
          <SlugInput slug={slug} onApply={onSlugChange} />
        )}
      </div>

      <div className="shrink-0 p-2 pb-1">
        <Button variant="primary" size="sm" block icon={<Plus />} loading={creating} onClick={onNew}>
          New chat
        </Button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
        <div className="px-2 pb-1 pt-2 text-2xs font-medium text-ink-3">Sessions</div>
        {sessionsLoading ? (
          <div className="space-y-1 pt-1">
            {[0, 1, 2, 3, 4].map((i) => (
              <Skeleton key={i} className="h-10" />
            ))}
          </div>
        ) : sessionsError ? (
          <div className="px-2 py-2">
            <p className="text-xs text-danger">
              {sessionsError instanceof Error ? sessionsError.message : 'Couldn’t load sessions.'}
            </p>
            <Button size="sm" variant="secondary" icon={<RotateCw />} className="mt-2" onClick={onRetrySessions}>
              Retry
            </Button>
          </div>
        ) : sessions.length === 0 ? (
          <p className="px-2 py-2 text-xs text-ink-3">No sessions yet.</p>
        ) : (
          <div className="space-y-0.5 pt-1">
            {sessions.map((s) => (
              <SessionRow
                key={s.id}
                session={s}
                active={s.id === activeId}
                onSelect={() => onSelect(s.id)}
                onDelete={() => onDeleteRequest(s)}
              />
            ))}
          </div>
        )}
      </div>

      {actionError && (
        <div className="shrink-0 border-t border-line p-2">
          <p className="text-2xs leading-relaxed text-danger">{actionError}</p>
        </div>
      )}
    </aside>
  )
}

function AgentSelect({
  slug,
  defs,
  onChange,
}: {
  slug: string
  defs: StudioDef<'agent'>[]
  onChange: (slug: string) => void
}) {
  const options = useMemo(() => {
    const list: Array<{ value: string; label: string; hint?: string }> = defs.map((d) => ({
      value: d.slug,
      label: d.name || d.slug,
      hint: d.slug,
    }))
    if (!list.some((o) => o.value === slug)) list.push({ value: slug, label: slug })
    return list
  }, [defs, slug])
  return (
    <Select
      size="sm"
      variant="ghost"
      ariaLabel="Chat agent"
      value={slug}
      options={options}
      onChange={onChange}
      className="min-w-0 flex-1 [&_button]:w-full"
    />
  )
}

/** Mono slug field for non-admins (and when the studio defs are unavailable). Applied on Enter. */
function SlugInput({ slug, onApply }: { slug: string; onApply: (slug: string) => void }) {
  const [draft, setDraft] = useState(slug)
  useEffect(() => setDraft(slug), [slug])
  return (
    <Input
      size="sm"
      aria-label="Agent slug"
      placeholder="agent-slug"
      spellCheck={false}
      value={draft}
      onChange={(e) => setDraft(e.target.value)}
      onKeyDown={(e) => {
        if (e.key !== 'Enter') return
        e.preventDefault()
        const next = e.currentTarget.value.trim()
        if (next && next !== slug) onApply(next)
      }}
      className="font-mono text-xs"
    />
  )
}

function SessionRow({
  session,
  active,
  onSelect,
  onDelete,
}: {
  session: AgentSession
  active: boolean
  onSelect: () => void
  onDelete: () => void
}) {
  return (
    <div className="group relative">
      <button
        type="button"
        onClick={onSelect}
        aria-current={active || undefined}
        className={cx(
          'flex w-full cursor-pointer flex-col items-start gap-px rounded-md px-2 py-1.5 pr-8 text-left outline-none transition-colors duration-100 focus-visible:ring-2 focus-visible:ring-accent/40',
          active ? 'bg-accent-soft' : 'hover:bg-ink/5',
        )}
      >
        <span className={cx('w-full truncate text-base font-medium', active ? 'text-accent-ink' : 'text-ink')}>
          {session.title || 'Untitled'}
        </span>
        <span className={cx('text-2xs', active ? 'text-accent-ink/70' : 'text-ink-3')}>{relativeTime(session.updated_at)}</span>
      </button>
      <div className="absolute right-1 top-1/2 -translate-y-1/2 opacity-0 transition-opacity duration-100 group-focus-within:opacity-100 group-hover:opacity-100">
        <IconButton icon={<Trash2 />} label="Delete session" size="sm" onClick={onDelete} className="text-ink-3 hover:text-danger" />
      </div>
    </div>
  )
}
