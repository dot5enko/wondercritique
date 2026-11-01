/**
 * Chat run state — pure helpers for the live streaming turn (plan §6).
 *
 * The page owns a `LiveTurn` ref that SSE events fold into via
 * {@link applyEvent}; when the run ends it is finalized into a `ChatEntry`
 * (the same shape as a stored message) so the transcript renderer stays
 * uniform for loaded history and streamed content.
 */
import type { AgentBlock, AgentRunEvent, StoredAgentMessage } from '../../api'

/** One tool call inside the live turn. */
export interface LiveToolCall {
  id: string
  name: string
  input?: unknown
  output?: string
  isError?: boolean
  /** Go `time.Duration` nanoseconds, from `tool_call_finished`. */
  durationNs?: number
  done: boolean
}

/**
 * Ordered content of the live turn — text segments and tool rows
 * interleaved in arrival order (a tool call starts a new text segment).
 */
export type LivePart = { kind: 'text'; text: string } | { kind: 'tool'; call: LiveToolCall }

export interface LiveTurn {
  parts: LivePart[]
  reasoning: string
  /** `run_failed` error text (status `failed`). */
  failed: string | null
  /** `run_failed` with status `cancelled` (Stop button or client abort). */
  cancelled: boolean
}

/** One transcript row: a stored message plus local run-outcome flags. */
export interface ChatEntry {
  msg: StoredAgentMessage
  /** The user input this entry belongs to (used by the failed-entry Retry). */
  input?: string
  /** Danger callout under the message + Retry (mid-run `run_failed`). */
  failed?: string
  /** Muted "stopped" note — partial content preserved. */
  interrupted?: boolean
}

export function emptyLiveTurn(): LiveTurn {
  return { parts: [], reasoning: '', failed: null, cancelled: false }
}

export function hasVisibleContent(live: LiveTurn): boolean {
  if (live.reasoning) return true
  return live.parts.some((p) => (p.kind === 'text' ? p.text !== '' : true))
}

/** Folds one run event into the live turn; returns the same object when nothing changed. */
export function applyEvent(live: LiveTurn, ev: AgentRunEvent): LiveTurn {
  switch (ev.type) {
    case 'text_delta': {
      if (!ev.delta) return live
      const parts = [...live.parts]
      const last = parts[parts.length - 1]
      if (last?.kind === 'text') parts[parts.length - 1] = { kind: 'text', text: last.text + ev.delta }
      else parts.push({ kind: 'text', text: ev.delta })
      return { ...live, parts }
    }
    case 'reasoning_delta': {
      if (!ev.delta) return live
      return { ...live, reasoning: live.reasoning + ev.delta }
    }
    case 'tool_call_started': {
      const c = ev.tool_call
      const call: LiveToolCall = { id: c.id, name: c.name, input: c.input, done: false }
      const idx = live.parts.findIndex((p) => p.kind === 'tool' && p.call.id === c.id)
      if (idx === -1) return { ...live, parts: [...live.parts, { kind: 'tool', call }] }
      const parts = [...live.parts]
      parts[idx] = { kind: 'tool', call }
      return { ...live, parts }
    }
    case 'tool_call_finished': {
      const c = ev.tool_call
      const finished: LiveToolCall = {
        id: c.id,
        name: c.name,
        input: c.input,
        output: c.output,
        isError: c.is_error,
        durationNs: c.duration,
        done: true,
      }
      const idx = live.parts.findIndex((p) => p.kind === 'tool' && p.call.id === c.id)
      if (idx === -1) return { ...live, parts: [...live.parts, { kind: 'tool', call: finished }] }
      const parts = [...live.parts]
      parts[idx] = { kind: 'tool', call: finished }
      return { ...live, parts }
    }
    case 'run_finished': {
      // Safety net for providers that emit no text deltas: surface the final text.
      const text = ev.text ?? ''
      if (!text || live.parts.some((p) => p.kind === 'text' && p.text)) return live
      return { ...live, parts: [...live.parts, { kind: 'text', text }] }
    }
    case 'run_failed':
      if (ev.status === 'cancelled') return { ...live, cancelled: true }
      return { ...live, failed: ev.error }
    default:
      return live
  }
}

let localSeq = 0

function localId(kind: string): string {
  localSeq += 1
  return `local-${kind}-${Date.now().toString(36)}-${localSeq}`
}

/** Local stand-in for a persisted user message (optimistic append). */
export function userEntry(input: string): ChatEntry {
  return {
    msg: {
      id: localId('u'),
      session_id: '',
      run_id: '',
      seq: 0,
      message: { role: 'user', blocks: [{ type: 'text', text: input }] },
      created_at: new Date().toISOString(),
    },
    input,
  }
}

/** Converts the finished/interrupted live turn into a transcript entry. */
export function entryFromLive(
  live: LiveTurn,
  opts: { interrupted?: boolean; failed?: string; input?: string },
): ChatEntry {
  const blocks: AgentBlock[] = []
  if (live.reasoning) blocks.push({ type: 'reasoning', text: live.reasoning })
  for (const p of live.parts) {
    if (p.kind === 'text') {
      if (p.text) blocks.push({ type: 'text', text: p.text })
    } else {
      blocks.push({
        type: 'tool_call',
        tool_call_id: p.call.id,
        tool_name: p.call.name,
        input: p.call.input,
        output: p.call.output,
        is_error: p.call.isError,
      })
    }
  }
  return {
    msg: {
      id: localId('a'),
      session_id: '',
      run_id: '',
      seq: 0,
      message: { role: 'assistant', blocks },
      created_at: new Date().toISOString(),
    },
    failed: opts.failed,
    interrupted: opts.interrupted || live.cancelled || undefined,
    input: opts.input,
  }
}
