/**
 * "Try it" modal (plan §4.4): one-off run of the agent's current draft on a
 * sample input, streaming the same run events as chat into a transcript.
 * Unsaved draft edits are flushed before the run so the backend sees the
 * draft the user is looking at; 409 (busy) / 429 (rate limited) are surfaced.
 */
import { useEffect, useRef, useState } from 'react'
import { Play, Square } from 'lucide-react'
import {
  Button,
  Callout,
  Field,
  Spinner,
  StatusDot,
  Textarea,
} from '../../../ui'
import { Modal } from '../../../components'
import { ApiError } from '../../../api/client'
import { tryAgent, type StudioCatalog, type StudioDef } from '../../../api/studio'
import type { TokenUsage } from '../../../api/types'
import { defaultSampleInput } from './defaults'
import { Transcript, applyEvent, type TranscriptEntry } from './Transcript'

type RunStatus = 'idle' | 'running' | 'completed' | 'failed' | 'stopped'

export interface TryItPanelProps {
  open: boolean
  onClose: () => void
  def: StudioDef<'agent'>
  catalog: StudioCatalog
  dirty: boolean
  /** Saves the draft immediately (used before a run / preview). */
  flushDraft: () => Promise<void>
}

function runErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 409) return `${err.message} — another run is active for this agent; wait for it to finish.`
    if (err.status === 429) return `Rate limited${err.retryAfter ? ` — retry in ${err.retryAfter}s` : ''}.`
    return err.message
  }
  return err instanceof Error ? err.message : 'Run failed.'
}

function fmtUsage(usage: TokenUsage): string {
  const parts = [`in ${usage.input_tokens.toLocaleString()}`, `out ${usage.output_tokens.toLocaleString()}`]
  if (usage.reasoning_tokens > 0) parts.push(`reasoning ${usage.reasoning_tokens.toLocaleString()}`)
  return parts.join(' · ')
}

function StatusLine({ status, usage }: { status: RunStatus; usage: TokenUsage | null }) {
  if (status === 'idle') return <span className="text-2xs text-ink-3">Not run yet</span>
  if (status === 'running')
    return (
      <span className="inline-flex items-center gap-1.5 text-2xs text-ink-2">
        <Spinner size={12} /> Running…
      </span>
    )
  if (status === 'stopped')
    return (
      <span className="inline-flex items-center gap-1.5 text-2xs text-ink-2">
        <StatusDot tone="neutral" /> Stopped
      </span>
    )
  if (status === 'failed')
    return (
      <span className="inline-flex items-center gap-1.5 text-2xs text-danger">
        <StatusDot tone="danger" /> Failed
      </span>
    )
  return (
    <span className="inline-flex min-w-0 flex-wrap items-center gap-1.5 text-2xs text-ink-2">
      <StatusDot tone="success" /> Completed{usage ? ` · ${fmtUsage(usage)}` : ''}
    </span>
  )
}

export function TryItPanel({ open, onClose, def, catalog, dirty, flushDraft }: TryItPanelProps) {
  const [sampleText, setSampleText] = useState(() => JSON.stringify(defaultSampleInput(catalog), null, 2))
  const [jsonError, setJsonError] = useState<string | null>(null)
  const [entries, setEntries] = useState<TranscriptEntry[]>([])
  const [status, setStatus] = useState<RunStatus>('idle')
  const [usage, setUsage] = useState<TokenUsage | null>(null)
  const [runError, setRunError] = useState<string | null>(null)
  const ctrlRef = useRef<AbortController | null>(null)

  const running = status === 'running'

  const stop = () => ctrlRef.current?.abort()

  const run = async (): Promise<void> => {
    if (running) return
    let input: unknown
    try {
      input = JSON.parse(sampleText)
    } catch (err) {
      setJsonError(err instanceof Error ? `Invalid JSON — ${err.message}` : 'Invalid JSON.')
      return
    }
    if (input === null || typeof input !== 'object' || Array.isArray(input)) {
      setJsonError('Sample must be a JSON object with one key per input port.')
      return
    }
    setJsonError(null)
    setRunError(null)
    setEntries([])
    setUsage(null)
    setStatus('running')

    const ctrl = new AbortController()
    ctrlRef.current = ctrl
    try {
      if (dirty) {
        try {
          await flushDraft()
        } catch (err) {
          setStatus('idle')
          setRunError(`Couldn't save the draft before the run — ${runErrorMessage(err)}`)
          return
        }
      }
      await tryAgent(def.id, { input: input as Record<string, unknown> }, {
        signal: ctrl.signal,
        onEvent: (event) => {
          if (event.type === 'run_finished') {
            setStatus('completed')
            setUsage(event.usage)
          } else if (event.type === 'run_failed') {
            setStatus(event.status === 'cancelled' ? 'stopped' : 'failed')
            setRunError(event.error)
            if (event.usage) setUsage(event.usage)
          } else if (event.type === 'step_finished') {
            setUsage(event.usage)
          }
          setEntries((prev) => applyEvent(prev, event))
        },
      })
      setStatus((s) => (s === 'running' ? 'completed' : s))
    } catch (err) {
      const aborted = ctrl.signal.aborted || (err instanceof DOMException && err.name === 'AbortError')
      if (aborted) {
        setStatus((s) => (s === 'running' ? 'stopped' : s))
      } else {
        setStatus('failed')
        setRunError(runErrorMessage(err))
      }
    } finally {
      if (ctrlRef.current === ctrl) ctrlRef.current = null
    }
  }

  /* Auto-scroll: follow the tail unless the user scrolled up. */
  const scrollRef = useRef<HTMLDivElement>(null)
  const stickToBottom = useRef(true)
  const onScroll = () => {
    const el = scrollRef.current
    if (!el) return
    stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }
  useEffect(() => {
    const el = scrollRef.current
    if (el && stickToBottom.current) el.scrollTop = el.scrollHeight
  }, [entries])

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="xl"
      title="Try it"
      description="Runs the current draft on the sample input — unsaved changes are saved first."
    >
      <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)]">
        <div className="space-y-2">
          <Field label="Sample input" hint="JSON object — one key per input port.">
            <Textarea
              mono
              rows={14}
              spellCheck={false}
              value={sampleText}
              onChange={(e) => setSampleText(e.target.value)}
            />
          </Field>
          {jsonError && <p className="text-xs text-danger">{jsonError}</p>}

          <div className="flex flex-wrap items-center gap-2.5">
            {running ? (
              <Button size="sm" variant="danger" icon={<Square />} onClick={stop}>
                Stop
              </Button>
            ) : (
              <Button size="sm" variant="primary" icon={<Play />} onClick={() => void run()}>
                Run
              </Button>
            )}
            <StatusLine status={status} usage={usage} />
          </div>

          {runError && <Callout tone="danger" title="Run failed">{runError}</Callout>}
        </div>

        <div
          ref={scrollRef}
          onScroll={onScroll}
          className="h-[60vh] min-h-64 overflow-y-auto rounded-md bg-sunken p-2"
        >
          {entries.length === 0 ? (
            <p className="grid place-items-center py-10 text-center text-xs text-ink-3">
              {status === 'running' ? 'Waiting for events…' : 'Run a test to see the transcript.'}
            </p>
          ) : (
            <Transcript entries={entries} />
          )}
        </div>
      </div>
    </Modal>
  )
}
