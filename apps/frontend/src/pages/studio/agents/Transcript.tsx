/**
 * Try-it run transcript (plan §4.4): renders the streamed AgentRunEvents as
 * assistant text/reasoning bubbles, collapsible tool-call rows and step
 * dividers. `applyEvent` folds one event into the entry list — text and
 * reasoning deltas append to the open bubble of their step.
 */
import { ChevronRight, CircleAlert, CircleCheck } from 'lucide-react'
import { cx, Spinner } from '../../../ui'
import type { AgentRunEvent, AgentToolCallEvent } from '../../../api/agents'

export type TranscriptEntry =
  | { kind: 'text'; id: number; step: number; text: string }
  | { kind: 'reasoning'; id: number; step: number; text: string }
  | { kind: 'tool'; id: number; call: AgentToolCallEvent; running: boolean }
  | { kind: 'step'; id: number; step: number; stop_reason: string }

let entrySeq = 0

/** Folds one run event into the transcript entries. */
export function applyEvent(prev: TranscriptEntry[], event: AgentRunEvent): TranscriptEntry[] {
  switch (event.type) {
    case 'text_delta': {
      const last = prev[prev.length - 1]
      if (last && last.kind === 'text' && last.step === event.step) {
        return [...prev.slice(0, -1), { ...last, text: last.text + event.delta }]
      }
      return [...prev, { kind: 'text', id: ++entrySeq, step: event.step, text: event.delta }]
    }
    case 'reasoning_delta': {
      const last = prev[prev.length - 1]
      if (last && last.kind === 'reasoning' && last.step === event.step) {
        return [...prev.slice(0, -1), { ...last, text: last.text + event.delta }]
      }
      return [...prev, { kind: 'reasoning', id: ++entrySeq, step: event.step, text: event.delta }]
    }
    case 'tool_call_started':
      return [...prev, { kind: 'tool', id: ++entrySeq, call: event.tool_call, running: true }]
    case 'tool_call_finished': {
      for (let i = prev.length - 1; i >= 0; i--) {
        const entry = prev[i]
        if (entry.kind === 'tool' && entry.call.id === event.tool_call.id) {
          const next = prev.slice()
          next[i] = { ...entry, call: { ...entry.call, ...event.tool_call }, running: false }
          return next
        }
      }
      // Started frame was missed (e.g. stream joined late) — show the finished call.
      return [...prev, { kind: 'tool', id: ++entrySeq, call: event.tool_call, running: false }]
    }
    case 'step_finished':
      return [...prev, { kind: 'step', id: ++entrySeq, step: event.step, stop_reason: event.stop_reason }]
    default:
      // run_started / run_finished / run_failed are surfaced by the status line.
      return prev
  }
}

function fmtDuration(ns: number | undefined): string {
  if (ns == null || !Number.isFinite(ns) || ns <= 0) return ''
  const ms = ns / 1e6
  if (ms < 1000) return `${Math.max(1, Math.round(ms))}ms`
  return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)}s`
}

function stringifyInput(input: unknown): string {
  if (input == null) return '—'
  try {
    return JSON.stringify(input, null, 2)
  } catch {
    return String(input)
  }
}

const SUMMARY_CLASS =
  'flex cursor-pointer list-none select-none items-center gap-1.5 outline-none [&::-webkit-details-marker]:hidden'

export function Transcript({ entries }: { entries: TranscriptEntry[] }) {
  return (
    <div className="space-y-2">
      {entries.map((entry) => (
        <TranscriptEntryView key={entry.id} entry={entry} />
      ))}
    </div>
  )
}

function TranscriptEntryView({ entry }: { entry: TranscriptEntry }) {
  switch (entry.kind) {
    case 'text':
      return (
        <div className="whitespace-pre-wrap break-words rounded-md border border-line bg-surface px-2.5 py-2 text-base leading-relaxed text-ink">
          {entry.text || '…'}
        </div>
      )
    case 'reasoning':
      return (
        <details className="group min-w-0">
          <summary className={cx(SUMMARY_CLASS, 'w-fit text-2xs text-ink-3 transition-colors duration-100 hover:text-ink-2')}>
            <ChevronRight size={11} className="shrink-0 transition-transform duration-150 group-open:rotate-90" />
            Thinking
          </summary>
          <div className="mt-1 whitespace-pre-wrap break-words border-l border-line pl-2.5 text-2xs leading-relaxed text-ink-3">
            {entry.text}
          </div>
        </details>
      )
    case 'tool': {
      const { call } = entry
      return (
        <details className="group rounded-md border border-line bg-surface">
          <summary className={cx(SUMMARY_CLASS, 'px-2 py-1.5 text-xs')}>
            {entry.running ? (
              <Spinner size={12} className="shrink-0 text-accent" />
            ) : call.is_error ? (
              <CircleAlert size={12} className="shrink-0 text-danger" />
            ) : (
              <CircleCheck size={12} className="shrink-0 text-success" />
            )}
            <span className="min-w-0 truncate font-mono text-ink">{call.name}</span>
            <span className="shrink-0 text-2xs text-ink-3">
              {entry.running ? 'running…' : call.is_error ? 'failed' : (fmtDuration(call.duration) || 'done')}
            </span>
            <ChevronRight
              size={11}
              className="ml-auto shrink-0 text-ink-3 transition-transform duration-150 group-open:rotate-90"
            />
          </summary>
          <div className="space-y-1.5 border-t border-line px-2 py-2">
            <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-sunken p-1.5 font-mono text-2xs leading-relaxed text-ink-2">
              {stringifyInput(call.input)}
            </pre>
            {call.output != null && (
              <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-sunken p-1.5 font-mono text-2xs leading-relaxed text-ink-2">
                {call.output}
              </pre>
            )}
          </div>
        </details>
      )
    }
    case 'step':
      return (
        <div role="separator" className="flex items-center gap-2 py-0.5 text-2xs text-ink-3">
          <span className="h-px flex-1 bg-line" />
          <span>
            step {entry.step}
            {entry.stop_reason ? ` · ${entry.stop_reason}` : ''}
          </span>
          <span className="h-px flex-1 bg-line" />
        </div>
      )
  }
}
