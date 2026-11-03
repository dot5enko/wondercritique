/**
 * Chats — the app's home (`/chat`): nothing but the list of design chats and
 * "New chat". Opening one (or a new one) goes to the full-screen canvas
 * (`/chat/:threadId`, `/chat/new`, see `canvas/`). A chat = a thread of
 * pipeline runs, grouped client-side by `useThreads()`.
 */
import { useNavigate } from 'react-router'
import { LogOut, Plus, RotateCw, Settings } from 'lucide-react'
import { Button, IconButton, Skeleton, cx } from '../../ui'
import { Callout } from '../../ui/Display'
import { isBackendMissing, relativeTime } from '../../components'
import { useAuth } from '../../features/auth'
import { runStatusTone } from '../runs/shared'
import { useThreads } from './threads'

export default function ChatListPage() {
  const navigate = useNavigate()
  const { user, logout } = useAuth()
  const { query, threads } = useThreads()
  const loading = query.loading && query.data == null

  return (
    <div className="min-h-dvh bg-app">
      <div className="mx-auto flex w-full max-w-2xl flex-col gap-4 px-4 pt-[max(1.5rem,env(safe-area-inset-top))] pb-10">
        <header className="flex items-center gap-2">
          <h1 className="min-w-0 flex-1 truncate text-lg font-semibold tracking-tight text-ink">Wonder Critique</h1>
          {user?.role === 'admin' && (
            <IconButton icon={<Settings />} label="Settings" onClick={() => navigate('/studio/pipelines')} />
          )}
          <IconButton icon={<LogOut />} label="Log out" onClick={() => void logout()} />
          <Button variant="primary" size="lg" icon={<Plus />} onClick={() => navigate('/chat/new')}>
            New chat
          </Button>
        </header>

        {loading ? (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
          </div>
        ) : query.error != null && threads.length === 0 ? (
          isBackendMissing(query.error) ? (
            <Callout tone="warning" title="The runs backend can't be reached">
              Chats live in the runs API, which isn't responding. Check that the server is running and reload.
            </Callout>
          ) : (
            <div className="flex items-center gap-2 text-sm text-ink-2">
              Couldn't load chats.
              <Button size="sm" variant="secondary" icon={<RotateCw />} onClick={query.reload}>
                Retry
              </Button>
            </div>
          )
        ) : threads.length === 0 ? (
          <button
            type="button"
            onClick={() => navigate('/chat/new')}
            className="cursor-pointer rounded-2xl border border-dashed border-line-strong bg-surface px-5 py-10 text-center text-sm text-ink-2 outline-none hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/40"
          >
            No chats yet — start one and describe what you want designed.
          </button>
        ) : (
          <ul className="flex flex-col overflow-hidden rounded-2xl border border-line bg-surface">
            {threads.map((t, i) => {
              const { tone } = runStatusTone(t.status)
              return (
                <li key={t.id} className={cx(i > 0 && 'border-t border-line')}>
                  <button
                    type="button"
                    onClick={() => navigate(`/chat/${encodeURIComponent(t.id)}`)}
                    className="flex w-full cursor-pointer items-center gap-3 px-4 py-3.5 text-left outline-none transition-colors duration-100 hover:bg-app focus-visible:bg-app"
                  >
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
                      <span className="block truncate text-sm font-medium text-ink">{t.title}</span>
                      <span className="block text-xs text-ink-3">
                        {relativeTime(t.at)}
                        {t.runs > 1 && ` · ${t.runs} messages`}
                      </span>
                    </span>
                  </button>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )
}
