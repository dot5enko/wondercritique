/**
 * Chats — the app's home (`/chat`): the style-guide pills (create one from
 * sample images; tap one → a new chat with it, `/chat/new?styleguide=<id>`),
 * a search box and one list whose first row is "New chat", followed by the
 * chats (`GET /threads`, loaded in chunks with a "Load more" pill). Opening one
 * (or a new one) goes to the full-screen canvas (`/chat/:threadId`,
 * `/chat/new`, see `canvas/`).
 */
import { useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { LogOut, Palette, Plus, RotateCw, Search, Settings, X } from 'lucide-react'
import { Button, IconButton, Input, Skeleton, Spinner, cx } from '../../ui'
import { Callout } from '../../ui/Display'
import { isBackendMissing, relativeTime } from '../../components'
import { useAuth } from '../../features/auth'
import { useDebouncedCallback } from '../../hooks'
import { runStatusTone } from '../runs/shared'
import { usePagedThreads } from './threads'
import { CreateStyleguideModal, StyleguideModal, StyleguidePills, useStyleguides } from './styleguides'

const SEARCH_DEBOUNCE_MS = 250

const escapeRegExp = (s: string): string => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/** `text` with every case-insensitive occurrence of `query` marked yellow. */
function Highlight({ text, query }: { text: string; query: string }): ReactNode {
  const q = query.trim()
  if (!q) return text
  const parts = text.split(new RegExp(`(${escapeRegExp(q)})`, 'giu'))
  return parts.map((part, i) =>
    i % 2 === 1 ? (
      <mark key={i} className="rounded-[3px] bg-yellow-200 px-px text-ink">
        {part}
      </mark>
    ) : (
      part
    ),
  )
}

export default function ChatListPage() {
  const navigate = useNavigate()
  const { user, logout } = useAuth()
  const [text, setText] = useState('')
  const [search, setSearch] = useState('')
  const applySearch = useDebouncedCallback((v: string) => setSearch(v.trim()), SEARCH_DEBOUNCE_MS)
  const { threads, loading, loadingMore, error, hasMore, loadMore, reload } = usePagedThreads(search)

  const onText = (v: string): void => {
    setText(v)
    applySearch(v)
  }
  const clear = (): void => {
    setText('')
    setSearch('')
    applySearch('') // supersedes a pending debounced search
  }
  const newChat = (): void => void navigate('/chat/new')

  const sg = useStyleguides()
  const [creating, setCreating] = useState(false)
  const [openGuide, setOpenGuide] = useState<string | null>(null)
  const chatWithGuide = (id: string): void => void navigate(`/chat/new?styleguide=${encodeURIComponent(id)}`)

  const row = 'flex w-full cursor-pointer items-center gap-3 px-4 py-3.5 text-left outline-none transition-colors duration-100 hover:bg-app focus-visible:bg-app'

  return (
    <div className="min-h-dvh bg-app">
      <div className="mx-auto flex w-full max-w-2xl flex-col gap-4 px-4 pt-[max(1.5rem,env(safe-area-inset-top))] pb-10">
        <header className="flex items-center gap-2">
          <h1 className="min-w-0 flex-1 truncate text-lg font-semibold tracking-tight text-ink">Wonder Critique</h1>
          {user?.role === 'admin' && (
            <IconButton icon={<Settings />} label="Settings" onClick={() => navigate('/studio/pipelines')} />
          )}
          <IconButton icon={<LogOut />} label="Log out" onClick={() => void logout()} />
        </header>

        <StyleguidePills guides={sg.guides} onCreate={() => setCreating(true)} onUse={chatWithGuide} onOpen={setOpenGuide} />

        <Input
          size="lg"
          type="search"
          value={text}
          onChange={(e) => onText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape' && text) {
              e.preventDefault()
              clear()
            }
          }}
          placeholder="Search chats"
          aria-label="Search chats"
          icon={<Search />}
          trail={
            text ? (
              <button
                type="button"
                onClick={clear}
                aria-label="Clear search"
                className="flex cursor-pointer items-center rounded p-0.5 text-ink-3 outline-none hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/40 [&_svg]:size-3.5"
              >
                <X />
              </button>
            ) : undefined
          }
          className="[&::-webkit-search-cancel-button]:hidden"
        />

        <ul className="flex flex-col overflow-hidden rounded-2xl border border-line bg-surface">
          <li>
            <button type="button" onClick={newChat} className={cx(row, 'font-medium text-accent-ink')}>
              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-accent-soft text-accent [&_svg]:size-3.5">
                <Plus />
              </span>
              <span className="min-w-0 flex-1 text-sm">New chat</span>
            </button>
          </li>

          {loading && threads.length === 0 ? (
            [0, 1, 2].map((i) => (
              <li key={i} className="border-t border-line px-4 py-3.5">
                <Skeleton className="h-9" />
              </li>
            ))
          ) : error != null && threads.length === 0 ? (
            <li className="border-t border-line px-4 py-4">
              {isBackendMissing(error) ? (
                <Callout tone="warning" title="The runs backend can't be reached">
                  Chats live in the runs API, which isn't responding. Check that the server is running and reload.
                </Callout>
              ) : (
                <div className="flex items-center gap-2 text-sm text-ink-2">
                  Couldn't load chats.
                  <Button size="sm" variant="secondary" icon={<RotateCw />} onClick={reload}>
                    Retry
                  </Button>
                </div>
              )}
            </li>
          ) : threads.length === 0 ? (
            <li className="border-t border-line px-4 py-6 text-center text-sm text-ink-3">
              {search ? `No chats match “${search}”.` : 'No chats yet — start one and describe what you want designed.'}
            </li>
          ) : (
            threads.map((t) => {
              const { tone } = runStatusTone(t.status)
              return (
                <li key={t.id} className="border-t border-line">
                  <button type="button" onClick={() => navigate(`/chat/${encodeURIComponent(t.id)}`)} className={row}>
                    <span
                      className={cx(
                        'size-2 shrink-0 rounded-full',
                        tone === 'accent' && 'animate-pulse bg-accent',
                        tone === 'success' && 'bg-success',
                        tone === 'danger' && 'bg-danger',
                        tone === 'warning' && 'bg-warning',
                        tone === 'neutral' && 'bg-ink-3',
                      )}
                    />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-medium text-ink">
                        <Highlight text={t.title} query={search} />
                      </span>
                      <span className="flex min-w-0 items-center gap-1 text-xs text-ink-3">
                        <span className="shrink-0">
                          {relativeTime(t.at)}
                          {t.runs > 1 && ` · ${t.runs} messages`}
                        </span>
                        {t.styleguideId && sg.byId.has(t.styleguideId) && (
                          <span className="flex min-w-0 items-center gap-1">
                            ·
                            <Palette size={11} className="shrink-0 text-accent" />
                            <span className="truncate">{sg.byId.get(t.styleguideId)?.name}</span>
                          </span>
                        )}
                      </span>
                    </span>
                  </button>
                </li>
              )
            })
          )}
        </ul>

        {hasMore && threads.length > 0 && (
          <div className="flex justify-center">
            <button
              type="button"
              onClick={loadMore}
              disabled={loadingMore || loading}
              className="flex cursor-pointer items-center gap-2 rounded-full border border-line bg-surface px-4 py-1.5 text-sm text-ink-2 shadow-xs outline-none transition duration-100 hover:border-line-strong hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/40 active:scale-[0.98] disabled:cursor-default disabled:opacity-70"
            >
              {loadingMore && <Spinner />}
              {error != null && !loadingMore ? "Couldn't load — retry" : 'Load more'}
            </button>
          </div>
        )}
      </div>

      <CreateStyleguideModal
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={(created) => {
          setCreating(false)
          sg.upsert(created)
          sg.reload()
        }}
      />
      <StyleguideModal
        guide={openGuide ? (sg.byId.get(openGuide) ?? null) : null}
        onClose={() => setOpenGuide(null)}
        onChanged={(changed) => {
          sg.upsert(changed)
          sg.reload()
        }}
        onDeleted={(id) => {
          setOpenGuide(null)
          sg.remove(id)
        }}
        onUse={chatWithGuide}
      />
    </div>
  )
}
