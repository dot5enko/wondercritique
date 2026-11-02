/**
 * Design chat — every chat is a pipeline run (product flow: the user writes
 * what they want designed → the pipeline generates HTML, renders it, a critic
 * reviews it and the gate decides whether to regenerate). The thread shows
 * each run live (RunCard); "Details" opens the full run view in a drawer.
 *
 * A chat = a thread of runs: the first message starts run #1 (its id becomes
 * the thread id); follow-up messages start a new run with `thread_id` so the
 * engine can feed previous context (ASSUMPTION in api/runs.ts).
 *
 * The runs/studio backend does not exist yet — until it does, API failures
 * surface as a clear "backend can't be reached" state instead of crashes.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { ArrowUp, MessageSquareDashed, Square, X } from 'lucide-react'
import { Button, IconButton, Input, Select, Textarea } from '../../ui'
import { Badge, Callout, EmptyState, Skeleton } from '../../ui/Display'
import { Field } from '../../ui/Form'
import { ErrorState, isBackendMissing, relativeTime } from '../../components'
import { useAsync } from '../../hooks'
import {
  cancelRun,
  isTerminalRunStatus,
  listRuns,
  startRun,
  type RunInputValue,
  type RunStatus,
} from '../../api/runs'
import { getDef, getVersion, listDefs, type PipelineBody } from '../../api/studio'
import { useThreads } from './threads'
import { RunCard } from './RunCard'
import { parseInputFields } from '../runs/NewRunModal'
import { RunDetailView } from '../runs/detail'

/** One timeline entry of the active thread. */
type Entry = { kind: 'user'; text: string; at: string } | { kind: 'run'; runId: string }

const EXAMPLES = [
  'A pricing section with three tiers, dark theme, rounded cards',
  'A hero for a hiking app: big headline, photo background, CTA button',
  'A user profile card with avatar, stats and a follow button',
]

const LAST_PIPELINE_KEY = 'wc_chat_pipeline'

export default function ChatPage() {
  const [params, setSearchParams] = useSearchParams()
  const threadId = params.get('thread')

  /* ---------- thread list (shared with the Shell sidebar) ---------- */

  const { query: threadsQ, threads } = useThreads()
  const backendMissing = threadsQ.error != null && isBackendMissing(threadsQ.error)

  /* ---------- active thread timeline ---------- */

  const [entries, setEntries] = useState<Entry[]>([])
  const [threadError, setThreadError] = useState<unknown>(null)
  const [threadLoading, setThreadLoading] = useState(false)

  const loadThread = useCallback(
    async (id: string, signal?: AbortSignal): Promise<void> => {
      setThreadLoading(true)
      setThreadError(null)
      try {
        const res = await listRuns({ filter: { thread_id: id }, limit: 100 }, signal)
        const runs = [...res.items].sort((a, b) => a.created_at.localeCompare(b.created_at))
        const next: Entry[] = []
        for (const r of runs) {
          const brief = r.input?.['brief']
          next.push({ kind: 'user', text: typeof brief === 'string' && brief ? brief : '(message)', at: r.created_at })
          next.push({ kind: 'run', runId: r.id })
        }
        if (!signal?.aborted) setEntries(next)
      } catch (err: unknown) {
        if (!signal?.aborted) setThreadError(err)
      } finally {
        if (!signal?.aborted) setThreadLoading(false)
      }
    },
    [],
  )

  useEffect(() => {
    setEntries([])
    setRunStatuses({})
    if (!threadId) return
    const ctrl = new AbortController()
    void loadThread(threadId, ctrl.signal)
    return () => ctrl.abort()
  }, [threadId, loadThread])

  /* ---------- live statuses (reported up by mounted RunCards) ---------- */

  const [runStatuses, setRunStatuses] = useState<Record<string, RunStatus>>({})
  const reportStatus = useCallback((id: string, status: RunStatus) => {
    setRunStatuses((prev) => (prev[id] === status ? prev : { ...prev, [id]: status }))
  }, [])

  const lastRunId = useMemo(() => {
    for (let i = entries.length - 1; i >= 0; i--) {
      const e = entries[i]
      if (e.kind === 'run') return e.runId
    }
    return null
  }, [entries])
  const busy = lastRunId != null && !isTerminalRunStatus(runStatuses[lastRunId] ?? 'running')

  // Rail mirrors status changes (e.g. a run finishing) without polling.
  const anyTerminal = Object.values(runStatuses).some((s) => isTerminalRunStatus(s))
  useEffect(() => {
    if (anyTerminal) threadsQ.reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [anyTerminal])

  /* ---------- pipeline picker (new chats) ---------- */

  const pipelinesQ = useAsync((signal) => listDefs('pipeline', { limit: 100 }, signal), [])
  const pipelines = useMemo(
    () => (pipelinesQ.data?.items ?? []).filter((d) => d.latest_version != null),
    [pipelinesQ.data],
  )
  const pipelinesUnavailable = pipelinesQ.error != null // 403 for non-admins, or missing backend

  const [pipelineId, setPipelineId] = useState<string | null>(() => {
    try {
      return localStorage.getItem(LAST_PIPELINE_KEY)
    } catch {
      return null
    }
  })
  const effectivePipelineId = useMemo(() => {
    if (pipelines.length === 0) return null
    if (pipelineId && pipelines.some((p) => p.id === pipelineId)) return pipelineId
    return pipelines.find((p) => p.slug === 'default')?.id ?? pipelines[0]?.id ?? null
  }, [pipelines, pipelineId])
  useEffect(() => {
    if (effectivePipelineId) {
      try {
        localStorage.setItem(LAST_PIPELINE_KEY, effectivePipelineId)
      } catch {
        // private mode etc. — the picker still works
      }
    }
  }, [effectivePipelineId])

  // Input fields of the pipeline the NEXT message will run: the selected one
  // for a new chat, else the thread's pipeline (follow-ups keep it).
  const threadPipelineId = useMemo(() => {
    const first = threadsQ.data?.items.find((r) => (r.thread_id || r.id) === threadId)
    return first?.pipeline.id ?? null
  }, [threadsQ.data, threadId])
  const fieldsPipelineId = threadId ? threadPipelineId : effectivePipelineId
  const fieldsQ = useAsync(
    async (signal): Promise<PipelineBody | null> => {
      if (!fieldsPipelineId) return null
      const def = (pipelinesQ.data?.items ?? []).find((d) => d.id === fieldsPipelineId)
      if (def?.latest_version != null) {
        const ver = await getVersion('pipeline', fieldsPipelineId, def.latest_version, signal).catch(() => null)
        if (ver) return ver.body
      }
      const d = await getDef('pipeline', fieldsPipelineId, signal).catch(() => null)
      return d?.draft ?? null
    },
    [fieldsPipelineId, pipelinesQ.data],
  )
  const inputFields = useMemo(() => parseInputFields(fieldsQ.data), [fieldsQ.data])
  const primaryField = inputFields.find((f) => (f.type ?? 'text').toLowerCase() !== 'image') ?? null
  const extraFields = inputFields.filter((f) => f !== primaryField)
  const requiredExtras = extraFields.filter((f) => f.required)

  /* ---------- composer ---------- */

  const [text, setText] = useState('')
  const [extras, setExtras] = useState<Record<string, string>>({})
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState<unknown>(null)

  const send = async (): Promise<void> => {
    const brief = text.trim()
    if (!brief || sending || busy) return
    setSending(true)
    setSendError(null)
    try {
      const input: Record<string, RunInputValue> = {}
      if (primaryField) input[primaryField.name] = brief
      else input['brief'] = brief
      for (const f of extraFields) {
        const v = (extras[f.name] ?? '').trim()
        if (!v) continue
        input[f.name] = (f.type ?? 'text').toLowerCase() === 'image' ? { artifact_id: v } : v
      }
      const res = await startRun({
        input,
        ...(threadId ? { thread_id: threadId } : {}),
        ...(!threadId && effectivePipelineId ? { pipeline_id: effectivePipelineId } : {}),
      })
      setText('')
      setExtras({})
      if (!threadId) setSearchParams({ thread: res.id })
      else void loadThread(threadId)
      threadsQ.reload()
    } catch (err: unknown) {
      setSendError(err)
    } finally {
      setSending(false)
    }
  }

  const stop = async (): Promise<void> => {
    if (!lastRunId) return
    try {
      await cancelRun(lastRunId)
    } catch {
      // the RunCard stream/refresh will surface the outcome
    }
  }

  /* ---------- misc ---------- */

  const bottomRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' })
  }, [entries.length, threadId])

  // Switching chats (incl. "New chat" in the sidebar) clears a stale send error.
  useEffect(() => setSendError(null), [threadId])
  const activeThread = threads.find((t) => t.id === threadId) ?? null

  const drawerRunId = params.get('run')

  /* ---------- render ---------- */

  return (
    <div className="flex h-full min-h-0">
      {backendMissing ? (
        <div className="flex min-w-0 flex-1 items-start justify-center p-6">
          <Callout tone="warning" title="The runs backend can't be reached" className="max-w-lg">
            <p className="mt-1 text-sm">
              Chats run pipelines through the runs API, which isn't responding. Check that the
              server is running and reload — your agents and pipelines are unaffected.
            </p>
            {threadsQ.error instanceof Error && (
              <p className="mt-1 font-mono text-2xs text-ink-3">{threadsQ.error.message}</p>
            )}
          </Callout>
        </div>
      ) : (
        <>
          <div className="flex min-w-0 flex-1 flex-col">
            {/* header — only for an open thread ("New chat" lives in the sidebar) */}
            {activeThread && (
              <div className="flex items-center gap-2 border-b border-line bg-surface px-4 py-2.5">
                <p className="min-w-0 flex-1 truncate text-sm font-semibold text-ink">{activeThread.title}</p>
                {activeThread.runs > 1 && <Badge tone="neutral">{activeThread.runs} runs</Badge>}
              </div>
            )}

            {/* timeline */}
            <div className="min-h-0 flex-1 overflow-y-auto">
              <div className="mx-auto flex max-w-3xl flex-col gap-4 px-4 py-5">
                {!threadId && (
                  <div className="rounded-xl border border-line bg-surface p-5">
                    <EmptyState
                      icon={MessageSquareDashed}
                      title="Describe what you want designed"
                      description="Each chat runs the design pipeline: an agent generates the HTML, it gets rendered, a critic reviews it, and it regenerates until the score passes."
                      className="py-2"
                    />
                    <div className="mt-2 flex flex-col gap-1.5">
                      {EXAMPLES.map((ex) => (
                        <button
                          key={ex}
                          type="button"
                          onClick={() => setText(ex)}
                          className="cursor-pointer rounded-lg border border-line bg-app px-3 py-2 text-left text-sm text-ink-2 outline-none transition duration-100 hover:border-line-strong hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/30"
                        >
                          {ex}
                        </button>
                      ))}
                    </div>
                  </div>
                )}

                {threadId && threadLoading && (
                  <div className="space-y-3">
                    <Skeleton className="h-16" />
                    <Skeleton className="h-48" />
                  </div>
                )}
                {threadId && !threadLoading && threadError != null && (
                  threadError != null && isBackendMissing(threadError) ? (
                    <Callout tone="warning" title="The runs backend can't be reached">
                      This chat's history lives in the runs API, which isn't responding right now.
                    </Callout>
                  ) : (
                    <ErrorState error={threadError} onRetry={() => threadId && void loadThread(threadId)} />
                  )
                )}

                {entries.map((e, i) =>
                  e.kind === 'user' ? (
                    <div key={`${i}-user`} className="flex justify-end">
                      <div className="max-w-[85%] rounded-2xl rounded-br-sm bg-accent-soft px-3.5 py-2.5 text-sm whitespace-pre-wrap text-ink">
                        {e.text}
                        <span className="mt-0.5 block text-right text-2xs text-accent-ink/70">{relativeTime(e.at)}</span>
                      </div>
                    </div>
                  ) : (
                    <RunCard
                      key={`${i}-${e.runId}`}
                      runId={e.runId}
                      onStatus={reportStatus}
                      onOpenDetails={() => setSearchParams((prev) => {
                        const next = new URLSearchParams(prev)
                        next.set('run', e.runId)
                        return next
                      })}
                      onRetry={
                        threadId
                          ? () => {
                              // the user message right before this run card
                              const prev = entries[i - 1]
                              void resend(prev && prev.kind === 'user' ? prev.text : '')
                            }
                          : undefined
                      }
                    />
                  ),
                )}

                <div ref={bottomRef} />
              </div>
            </div>

            {/* composer */}
            <div className="border-t border-line bg-surface">
              <div className="mx-auto flex max-w-3xl flex-col gap-2 px-4 py-3">
                {sendError != null && (
                  isBackendMissing(sendError) ? (
                    <Callout tone="warning" title="The runs backend can't be reached">
                      Runs can't start while the runs API is unreachable.
                    </Callout>
                  ) : (
                    <Callout tone="danger" title="Couldn't start the run">
                      {sendError instanceof Error ? sendError.message : 'Something went wrong.'}
                    </Callout>
                  )
                )}

                {!threadId && !pipelinesUnavailable && pipelines.length > 0 && (
                  <div className="flex items-center gap-2">
                    <span className="text-2xs whitespace-nowrap text-ink-3">Pipeline</span>
                    <Select
                      options={pipelines.map((p) => ({ value: p.id, label: p.name, hint: `v${p.latest_version}` }))}
                      value={effectivePipelineId}
                      onChange={setPipelineId}
                      ariaLabel="Pipeline"
                      className="max-w-64"
                    />
                  </div>
                )}
                {!threadId && pipelinesUnavailable && pipelines.length === 0 && (
                  <p className="text-2xs text-ink-3">
                    Pipeline picker unavailable (admin-only) — the default published pipeline will run.
                  </p>
                )}

                {requiredExtras.length > 0 && (
                  <div className="grid grid-cols-2 gap-2">
                    {requiredExtras.map((f) => (
                      <Field key={f.name} label={f.label || f.name} hint={f.description}>
                        <Input
                          value={extras[f.name] ?? f.default ?? ''}
                          onChange={(e) => setExtras((prev) => ({ ...prev, [f.name]: e.target.value }))}
                        />
                      </Field>
                    ))}
                  </div>
                )}

                <div className="flex items-end gap-2">
                  <Textarea
                    rows={2}
                    value={text}
                    onChange={(e) => setText(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' && !e.shiftKey) {
                        e.preventDefault()
                        void send()
                      }
                    }}
                    placeholder={busy ? 'Waiting for the pipeline to finish…' : 'Describe what you want designed…'}
                    disabled={busy || sending}
                    className="min-h-0 flex-1 resize-none"
                  />
                  {busy ? (
                    <Button variant="secondary" icon={<Square />} onClick={() => void stop()} disabled={!lastRunId}>
                      Stop
                    </Button>
                  ) : (
                    <Button variant="primary" icon={<ArrowUp />} loading={sending} disabled={!text.trim()} onClick={() => void send()}>
                      Send
                    </Button>
                  )}
                </div>
              </div>
            </div>
          </div>

          {/* run-details drawer */}
          {drawerRunId && (
            <Drawer onClose={() => setSearchParams((prev) => {
              const next = new URLSearchParams(prev)
              next.delete('run')
              return next
            })}>
              <RunDetailView runId={drawerRunId} />
            </Drawer>
          )}
        </>
      )}
    </div>
  )

  /** Re-sends a message as a follow-up run (used by the run card's retry). */
  async function resend(brief: string): Promise<void> {
    if (!brief || !threadId) return
    setSending(true)
    setSendError(null)
    try {
      await startRun({ input: { [primaryField?.name ?? 'brief']: brief }, thread_id: threadId })
      void loadThread(threadId)
      threadsQ.reload()
    } catch (err: unknown) {
      setSendError(err)
    } finally {
      setSending(false)
    }
  }
}



/** Right-side drawer with the full run detail view. */
function Drawer({ children, onClose }: { children: React.ReactNode; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return (
    <>
      <button
        type="button"
        aria-label="Close details"
        onClick={onClose}
        className="fixed inset-0 z-40 cursor-default bg-black/20"
      />
      <aside className="fixed inset-y-0 right-0 z-50 flex w-[min(960px,92vw)] flex-col border-l border-line bg-page shadow-xl">
        <div className="flex items-center justify-end border-b border-line bg-surface px-2 py-1.5">
          <IconButton icon={<X />} label="Close details" size="sm" onClick={onClose} />
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
      </aside>
    </>
  )
}
