/**
 * Chat — talk to any published agent (plan.critique-engine.md §6: the generic
 * `/api/v1/agents/{slug}/…` httpapi surface replaces the demo critic).
 *
 * Two-pane layout like the shell: session rail (w-64, sunken) on the left,
 * thread + composer on the right. The agent comes from `?agent=<slug>`
 * (default `critic`); admins pick from published Studio defs, everyone else
 * types a slug. Runs stream over SSE: text/reasoning deltas, tool rows and
 * the run outcome are folded into the transcript live.
 */
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { Bot, MessageSquare, Plus, RotateCw, X } from 'lucide-react'
import {
  ApiError,
  cancelAgentRun,
  createAgentSession,
  deleteAgentSession,
  getAgentSession,
  listAgentSessions,
  listDefs,
  runAgent,
  type AgentRunEvent,
  type AgentSession,
} from '../../api'
import { Button, Callout, EmptyState, IconButton, Skeleton } from '../../ui'
import { ConfirmDialog, ErrorState, absoluteTime, relativeTime } from '../../components'
import { useAsync } from '../../hooks'
import { useAuth } from '../../features/auth'
import { SessionRail } from './rail'
import { Composer } from './composer'
import { TranscriptList } from './thread'
import {
  applyEvent,
  emptyLiveTurn,
  entryFromLive,
  hasVisibleContent,
  userEntry,
  type ChatEntry,
  type LiveTurn,
} from './run'

type HistoryState = 'idle' | 'loading' | 'ready' | 'error'

/** Guard object created per run — post-run side effects apply only while it is still current. */
interface RunCtx {
  slug: string
  sessionId: string
}

function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError'
}

export default function ChatPage() {
  const { user } = useAuth()
  const isAdmin = user?.role === 'admin'
  const [searchParams, setSearchParams] = useSearchParams()
  const slug = (searchParams.get('agent') ?? 'critic').trim() || 'critic'

  /* ---------- agent defs (admin picker; falls back to a slug input) ---------- */
  const defs = useAsync(async (signal) => {
    if (!isAdmin) return null
    try {
      const res = await listDefs('agent', { limit: 100 }, signal)
      // Chat resolves published agents only — hide drafts/archived defs.
      const published = res.items.filter((d) => !d.archived && d.latest_version != null)
      return published.length > 0 ? published : null
    } catch {
      return null // studio endpoints missing / not an admin → slug input
    }
  }, [isAdmin])
  const agentName = useMemo(() => defs.data?.find((d) => d.slug === slug)?.name ?? null, [defs.data, slug])

  /* ---------- sessions ---------- */
  const sessions = useAsync((signal) => listAgentSessions(slug, { limit: 50 }, signal), [slug])
  const sortedSessions = useMemo(() => {
    const list = [...(sessions.data?.sessions ?? [])]
    list.sort((a, b) => (a.updated_at < b.updated_at ? 1 : a.updated_at > b.updated_at ? -1 : 0))
    return list
  }, [sessions.data])

  const mutateSessions = useCallback(
    (fn: (list: AgentSession[]) => AgentSession[]) => {
      sessions.setData((prev) => (prev ? { ...prev, sessions: fn(prev.sessions) } : prev))
    },
    [sessions.setData],
  )

  /* ---------- selection + history ---------- */
  // The active session lives in the URL (`?id=`) so a refresh reopens the same chat.
  const activeId = searchParams.get('id')
  const setActiveId = useCallback(
    (id: string | null) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          if (id) next.set('id', id)
          else next.delete('id')
          return next
        },
        { replace: true },
      )
    },
    [setSearchParams],
  )
  const [entries, setEntries] = useState<ChatEntry[]>([])
  const [historyState, setHistoryState] = useState<HistoryState>('idle')
  const [historyError, setHistoryError] = useState<unknown>(null)
  const [historyEpoch, setHistoryEpoch] = useState(0)
  const reloadHistory = useCallback(() => setHistoryEpoch((e) => e + 1), [])

  /* ---------- auto-scroll (sticky bottom unless the user scrolled up) ---------- */
  const scrollRef = useRef<HTMLDivElement>(null)
  const pinnedRef = useRef(true)
  const handleScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    pinnedRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48
  }, [])

  /* ---------- live run state ---------- */
  const liveRef = useRef<LiveTurn | null>(null)
  const [live, setLive] = useState<LiveTurn | null>(null)
  const ctrlRef = useRef<AbortController | null>(null)
  const ctxRef = useRef<RunCtx | null>(null)
  const streamingRef = useRef(false)
  const [streaming, setStreaming] = useState(false)
  const lastInputRef = useRef<string | null>(null)

  /* ---------- composer + notices ---------- */
  const [text, setText] = useState('')
  const [busyNotice, setBusyNotice] = useState(false)
  const [startError, setStartError] = useState<string | null>(null)

  /* ---------- rail actions ---------- */
  const [creating, setCreating] = useState(false)
  const [railError, setRailError] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<AgentSession | null>(null)
  const [deleting, setDeleting] = useState(false)

  const activeSession = useMemo(() => sortedSessions.find((s) => s.id === activeId) ?? null, [sortedSessions, activeId])

  const resetRun = useCallback(() => {
    ctrlRef.current?.abort()
    ctrlRef.current = null
    ctxRef.current = null
    liveRef.current = null
    setLive(null)
    streamingRef.current = false
    setStreaming(false)
    setBusyNotice(false)
    setStartError(null)
  }, [])

  // Switching agent drops the session/run state — switching sessions is handled below.
  // Guarded by prevSlugRef so it doesn't clear the deep-linked `?id=` on first mount.
  const prevSlugRef = useRef(slug)
  useEffect(() => {
    if (prevSlugRef.current === slug) return
    prevSlugRef.current = slug
    resetRun()
    setActiveId(null)
    setText('')
    sessions.setData(null) // don't show the previous agent's sessions while loading
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slug, resetRun, setActiveId])

  // Selecting another session aborts the in-flight run (a client disconnect
  // cancels the run server-side) and clears live/notices for the new context.
  useEffect(() => {
    resetRun()
  }, [activeId, resetRun])

  // Load the selected session's history. Replaces the local transcript wholesale.
  useEffect(() => {
    pinnedRef.current = true // opening a session lands on the latest messages
    if (!activeId) {
      setEntries([])
      setHistoryState('idle')
      setHistoryError(null)
      return
    }
    const ctrl = new AbortController()
    let alive = true
    setHistoryState('loading')
    setHistoryError(null)
    setEntries([])
    getAgentSession(slug, activeId, ctrl.signal).then(
      (res) => {
        if (!alive) return
        setEntries(res.messages.map((msg) => ({ msg })))
        setHistoryState('ready')
      },
      (err: unknown) => {
        if (!alive || ctrl.signal.aborted) return
        setHistoryError(err)
        setHistoryState('error')
      },
    )
    return () => {
      alive = false
      ctrl.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slug, activeId, historyEpoch])

  // Abort any in-flight run on unmount (a client disconnect cancels the run server-side).
  useEffect(
    () => () => {
      ctrlRef.current?.abort()
      ctxRef.current = null
    },
    [],
  )

  useLayoutEffect(() => {
    const el = scrollRef.current
    if (el && pinnedRef.current) el.scrollTop = el.scrollHeight
  }, [entries, live, historyState, activeId])

  /* ---------- session rail actions ---------- */
  const createSession = useCallback(async () => {
    setRailError(null)
    setCreating(true)
    try {
      const s = await createAgentSession(slug, { title: '' })
      mutateSessions((list) => [s, ...list.filter((x) => x.id !== s.id)])
      setActiveId(s.id)
    } catch (err) {
      setRailError(err instanceof Error ? err.message : 'Could not create the session.')
    } finally {
      setCreating(false)
    }
  }, [slug, mutateSessions])

  const confirmDelete = useCallback(async () => {
    const target = deleteTarget
    if (!target) return
    setDeleting(true)
    setRailError(null)
    try {
      await deleteAgentSession(slug, target.id)
      mutateSessions((list) => list.filter((s) => s.id !== target.id))
      if (activeId === target.id) setActiveId(null)
      setDeleteTarget(null)
    } catch (err) {
      setRailError(err instanceof Error ? err.message : 'Could not delete the session.')
      setDeleteTarget(null)
    } finally {
      setDeleting(false)
    }
  }, [slug, deleteTarget, activeId, mutateSessions])

  const changeSlug = useCallback(
    (next: string) => {
      if (!next || next === slug) return
      setSearchParams({ agent: next }, { replace: true })
    },
    [slug, setSearchParams],
  )

  /* ---------- runs ---------- */
  const touchSession = useCallback(
    (sessionId: string) => {
      mutateSessions((list) =>
        list.map((s) => (s.id === sessionId ? { ...s, updated_at: new Date().toISOString() } : s)),
      )
    },
    [mutateSessions],
  )

  const finalizeTurn = useCallback((interrupted: boolean, failed: string | undefined, input: string | undefined) => {
    const finished = liveRef.current
    liveRef.current = null
    setLive(null)
    if (!finished) return
    const failedMsg = failed ?? finished.failed ?? undefined
    if (!hasVisibleContent(finished) && !failedMsg) return
    // The failure callout already explains the truncation — no extra "stopped" note.
    setEntries((prev) => [...prev, entryFromLive(finished, { interrupted: interrupted && !failedMsg, failed: failedMsg, input })])
  }, [])

  const renameSession = useCallback(
    (sessionId: string, input: string) => {
      const title = input.length > 60 ? `${input.slice(0, 60)}…` : input
      mutateSessions((list) => list.map((s) => (s.id === sessionId ? { ...s, title } : s)))
    },
    [mutateSessions],
  )

  const runTurn = useCallback(
    (input: string, opts: { appendUser: boolean }) => {
      const sessionId = activeId
      if (!sessionId || ctrlRef.current) return
      setBusyNotice(false)
      setStartError(null)
      const ctx: RunCtx = { slug, sessionId }
      ctxRef.current = ctx

      // Optimistic user bubble. A busy (409) or failed start rolls it back —
      // the run never started, so nothing was persisted server-side.
      let appended: ChatEntry | null = null
      if (opts.appendUser) {
        const entry = userEntry(input)
        appended = entry
        setEntries((prev) => [...prev, entry])
      }
      // First user message names the session locally (httpapi has no title-update
      // endpoint, so the rename lives in the rail list only).
      if (activeSession && !activeSession.title && !entries.some((e) => e.msg.message.role === 'user')) {
        renameSession(sessionId, input)
      }

      const live0 = emptyLiveTurn()
      liveRef.current = live0
      setLive(live0)
      lastInputRef.current = input

      const ctrl = new AbortController()
      ctrlRef.current = ctrl
      streamingRef.current = true
      setStreaming(true)
      let gotEvent = false
      let sawTerminal = false

      runAgent(slug, sessionId, { input }, {
        signal: ctrl.signal,
        onEvent: (ev: AgentRunEvent) => {
          gotEvent = true
          if (ev.type === 'run_finished' || ev.type === 'run_failed') sawTerminal = true
          const cur = liveRef.current
          if (!cur) return
          const next = applyEvent(cur, ev)
          liveRef.current = next
          setLive(next)
        },
      }).then(
        () => {
          if (ctxRef.current !== ctx) return
          // Stream closed without a terminal event (dropped/crashed) → interrupted.
          finalizeTurn(!sawTerminal, undefined, input)
        },
        (err: unknown) => {
          if (ctxRef.current !== ctx) return
          if (isAbortError(err)) {
            // Stop button / session switch — keep whatever streamed so far.
            finalizeTurn(true, undefined, input)
            return
          }
          if (!gotEvent) {
            if (appended) {
              const dropped = appended
              setEntries((prev) => prev.filter((e) => e.msg.id !== dropped.msg.id))
            }
            liveRef.current = null
            setLive(null)
            setText((prev) => prev || input)
            if (err instanceof ApiError && err.status === 409) setBusyNotice(true)
            else setStartError(err instanceof Error ? err.message : 'Could not start the run.')
          } else {
            finalizeTurn(false, err instanceof Error ? err.message : 'The run failed unexpectedly.', input)
          }
        },
      ).finally(() => {
        if (ctxRef.current !== ctx) return
        ctxRef.current = null
        ctrlRef.current = null
        streamingRef.current = false
        setStreaming(false)
        touchSession(sessionId)
      })
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [slug, activeId, activeSession, entries, finalizeTurn, touchSession],
  )

  const send = useCallback(() => {
    const input = text.trim()
    if (!input || !activeId || streaming) return
    setText('')
    runTurn(input, { appendUser: true })
  }, [text, activeId, streaming, runTurn])

  const stop = useCallback(() => {
    const ctrl = ctrlRef.current
    const sessionId = activeId
    if (!ctrl || !sessionId) return
    // Best effort: the abort below also cancels the run (client disconnect).
    void cancelAgentRun(slug, sessionId).catch(() => {})
    ctrl.abort()
  }, [slug, activeId])

  /** Re-run a failed turn. `entryInput` identifies the failed entry's input; undefined = the live turn (current run). */
  const retryFrom = useCallback(
    (entryInput: string | undefined) => {
      const input = entryInput ?? lastInputRef.current
      if (!input || !activeId || streaming) return
      // The user message is already persisted — don't append it twice.
      runTurn(input, { appendUser: false })
    },
    [activeId, streaming, runTurn],
  )

  /** Re-send after a failed start (nothing was persisted). */
  const retryStart = useCallback(() => {
    const input = lastInputRef.current
    if (!input || !activeId || streaming) return
    setText((prev) => (prev.trim() === input ? '' : prev))
    runTurn(input, { appendUser: true })
  }, [activeId, streaming, runTurn])

  const cancelRunning = useCallback(() => {
    if (!activeId) return
    void cancelAgentRun(slug, activeId)
      .then(() => setBusyNotice(false))
      .catch(() => {})
  }, [slug, activeId])

  /* ---------- render ---------- */
  return (
    <div className="flex h-full min-h-0">
      <SessionRail
        slug={slug}
        defs={defs.data ?? null}
        defsLoading={defs.loading && isAdmin}
        sessions={sortedSessions}
        sessionsLoading={sessions.loading && sessions.data === null}
        sessionsError={sessions.error}
        activeId={activeId}
        creating={creating}
        actionError={railError}
        onSlugChange={changeSlug}
        onNew={() => void createSession()}
        onSelect={setActiveId}
        onDeleteRequest={setDeleteTarget}
        onRetrySessions={sessions.reload}
      />

      <section className="flex min-w-0 flex-1 flex-col bg-surface">
        <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line bg-surface px-4">
          <Bot size={15} className="shrink-0 text-ink-3" />
          <h2 className="text-sm font-semibold text-ink">{agentName ?? 'Chat'}</h2>
          <span className="inline-flex items-center rounded border border-line bg-app px-1.5 py-px font-mono text-2xs text-ink-2">
            {slug}
          </span>
          <span className="min-w-0 flex-1" />
          {activeSession && (
            <span
              className="shrink-0 text-2xs text-ink-3"
              title={absoluteTime(activeSession.updated_at)}
            >
              {relativeTime(activeSession.updated_at)}
            </span>
          )}
        </header>

        <div ref={scrollRef} onScroll={handleScroll} className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto max-w-3xl px-4 py-4">
            {!activeSession ? (
              <EmptyState
                icon={MessageSquare}
                title="Start a new chat"
                description="Pick a session on the left or create a new one."
                action={
                  <Button variant="primary" size="sm" icon={<Plus />} loading={creating} onClick={() => void createSession()}>
                    New chat
                  </Button>
                }
                className="mt-16"
              />
            ) : historyState === 'loading' ? (
              <HistorySkeleton />
            ) : historyError ? (
              <ErrorState error={historyError} onRetry={reloadHistory} />
            ) : entries.length === 0 && !live ? (
              <EmptyState
                icon={Bot}
                title="Send the first message"
                description={`Say something to ${agentName ?? slug} to start the conversation.`}
                className="mt-16"
              />
            ) : (
              <TranscriptList entries={entries} live={live} onRetry={retryFrom} />
            )}
          </div>
        </div>

        {(busyNotice || startError) && (
          <div className="shrink-0 space-y-2 px-3 pt-2">
            {busyNotice && (
              <Callout tone="warning" className="pr-2">
                <div className="flex items-center gap-2">
                  <span className="min-w-0 flex-1">Agent is busy — wait or cancel the running turn.</span>
                  <Button size="sm" variant="secondary" className="shrink-0" onClick={cancelRunning}>
                    Cancel running turn
                  </Button>
                  <IconButton icon={<X />} label="Dismiss" size="sm" className="shrink-0" onClick={() => setBusyNotice(false)} />
                </div>
              </Callout>
            )}
            {startError && (
              <Callout tone="danger" title="Could not start the run" className="pr-2">
                <div className="mt-1 flex items-center gap-2">
                  <span className="min-w-0 break-words">{startError}</span>
                  <Button size="sm" variant="secondary" icon={<RotateCw />} className="shrink-0" onClick={retryStart}>
                    Retry
                  </Button>
                  <IconButton icon={<X />} label="Dismiss" size="sm" className="shrink-0" onClick={() => setStartError(null)} />
                </div>
              </Callout>
            )}
          </div>
        )}

        {activeSession && (
          <Composer
            value={text}
            onChange={setText}
            onSend={send}
            onStop={stop}
            streaming={streaming}
            disabled={historyState === 'loading'}
            placeholder={`Message ${agentName ?? slug}…`}
          />
        )}
      </section>

      <ConfirmDialog
        open={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        title="Delete session?"
        body={`“${deleteTarget?.title || 'Untitled'}” and its messages will be removed. This cannot be undone.`}
        confirmLabel="Delete"
        danger
        busy={deleting}
        onConfirm={() => void confirmDelete()}
      />
    </div>
  )
}

function HistorySkeleton() {
  return (
    <div className="space-y-3" aria-busy="true">
      <div className="flex justify-end">
        <Skeleton className="h-10 w-1/2 rounded-lg" />
      </div>
      <Skeleton className="h-16 w-2/3 rounded-lg" />
      <div className="flex justify-end">
        <Skeleton className="h-8 w-1/3 rounded-lg" />
      </div>
      <Skeleton className="h-12 w-3/5 rounded-lg" />
    </div>
  )
}
