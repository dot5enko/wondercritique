/**
 * Chat API for published agents — the agentkit `httpapi` surface, mounted per
 * agent slug at `/api/v1/agents/{slug}/…` (docs/backend.md "Agent HTTP
 * endpoints", plan §6: any published Studio agent replaces the demo critic).
 *
 * Wire types mirror the agentkit structs exactly:
 * `store.Session`, `store.StoredMessage`, `llm.Message`/`llm.Block`/`llm.Usage`,
 * `agentkit.Event` (the SSE payloads), plus the final `done` frame.
 */
import { apiDelete, apiGet, apiPost, toQuery } from './client'
import type { TokenUsage } from './types'
import { parseSSEJSON, postSSETyped, type SSEMessage } from './sse'

export type { TokenUsage } from './types'

/* ---------- agentkit wire types ---------- */

export type AgentRole = 'user' | 'assistant' | 'system' | 'tool'

/** One content block of a message (`llm.Block`). Fields are populated per type. */
export interface AgentBlock {
  type: 'text' | 'image' | 'tool_call' | 'tool_result' | 'reasoning'
  /** Text content; reasoning summary. */
  text?: string
  /** `commentary` when assistant text precedes a tool call. */
  phase?: string
  tool_call_id?: string
  tool_name?: string
  /** tool_call arguments (raw JSON). */
  input?: unknown
  /** tool_result output — tools return strings only. */
  output?: string
  is_error?: boolean
  /** `data:` URL or https URL. */
  image_url?: string
  /** Opaque provider payload replayed verbatim (reasoning). */
  opaque?: unknown
  provider?: string
}

/** `llm.Message`. */
export interface AgentMessage {
  role: AgentRole
  blocks: AgentBlock[]
}

/** `store.StoredMessage` — a persisted message inside a session. */
export interface StoredAgentMessage {
  id: string
  session_id: string
  run_id: string
  /** 1-based, strictly increasing within a session. */
  seq: number
  message: AgentMessage
  created_at: string
}

/** `store.Session`. */
export interface AgentSession {
  id: string
  agent: string
  title: string
  metadata?: Record<string, unknown>
  created_at: string
  updated_at: string
}

/** `store.RunStatus`. */
export type AgentRunStatus = 'running' | 'completed' | 'failed' | 'cancelled' | 'interrupted'

/* ---------- run events (agentkit.Event over SSE) ---------- */

/** Tool call detail in `tool_call_started` / `tool_call_finished` events. */
export interface AgentToolCallEvent {
  id: string
  name: string
  /** Raw JSON arguments. */
  input?: unknown
  /** Present on `tool_call_finished`. */
  output?: string
  is_error?: boolean
  /** Nanoseconds (Go `time.Duration`). */
  duration?: number
}

/**
 * Discriminated union of the agentkit run events, as streamed by
 * `POST …/runs` (SSE). Each frame's `event:` name equals its `type` field, so
 * the union discriminates on `type`. The terminal `event: done` frame carries
 * `{}` and is not part of this union.
 */
export type AgentRunEvent =
  | { type: 'run_started'; session_id: string; run_id: string }
  | { type: 'text_delta'; session_id: string; run_id: string; step: number; delta: string }
  | { type: 'reasoning_delta'; session_id: string; run_id: string; step: number; delta: string }
  | { type: 'tool_call_started'; session_id: string; run_id: string; step: number; tool_call: AgentToolCallEvent }
  | { type: 'tool_call_finished'; session_id: string; run_id: string; step: number; tool_call: AgentToolCallEvent }
  | {
      type: 'step_finished'
      session_id: string
      run_id: string
      step: number
      message: AgentMessage
      usage: TokenUsage
      stop_reason: string
    }
  | { type: 'run_finished'; session_id: string; run_id: string; text: string; usage: TokenUsage; status: 'completed' }
  | { type: 'run_failed'; session_id: string; run_id: string; error: string; usage?: TokenUsage; status: 'failed' | 'cancelled' }

/**
 * Maps a raw SSE frame to an {@link AgentRunEvent}, or null for the terminal
 * `done` frame / unknown event names.
 */
export function mapAgentRunEvent(message: SSEMessage): AgentRunEvent | null {
  if (message.event === 'done') return null
  const ev = parseSSEJSON<AgentRunEvent & Record<string, unknown>>(message.data, {} as AgentRunEvent)
  if (!ev || typeof ev.type !== 'string') return null
  return ev
}

/* ---------- sessions ---------- */

export interface ListAgentSessionsParams {
  limit?: number
  offset?: number
}

/** POST /agents/{slug}/sessions — creates a session owned by the current user. */
export function createAgentSession(
  slug: string,
  body: { title?: string; metadata?: Record<string, unknown> } = {},
  signal?: AbortSignal,
): Promise<AgentSession> {
  return apiPost<AgentSession>(`/agents/${slug}/sessions`, body, { signal })
}

/** GET /agents/{slug}/sessions — the caller's sessions, newest first is not guaranteed. */
export function listAgentSessions(
  slug: string,
  params: ListAgentSessionsParams = {},
  signal?: AbortSignal,
): Promise<{ sessions: AgentSession[] }> {
  return apiGet<{ sessions: AgentSession[] }>(`/agents/${slug}/sessions${toQuery(params)}`, { signal })
}

/** GET /agents/{slug}/sessions/{id} — full message history. */
export function getAgentSession(slug: string, id: string, signal?: AbortSignal): Promise<{ messages: StoredAgentMessage[] }> {
  return apiGet<{ messages: StoredAgentMessage[] }>(`/agents/${slug}/sessions/${id}`, { signal })
}

/** DELETE /agents/{slug}/sessions/{id} — 204. */
export function deleteAgentSession(slug: string, id: string, signal?: AbortSignal): Promise<void> {
  return apiDelete<void>(`/agents/${slug}/sessions/${id}`, { signal })
}

/* ---------- runs ---------- */

/** `?stream=false` result (`runResponse` in httpapi.go). */
export interface AgentRunResult {
  run_id: string
  status: AgentRunStatus
  text: string
  usage: TokenUsage
  steps: number
  messages?: AgentMessage[]
}

export interface StartAgentRun {
  signal?: AbortSignal
  /** Omit to stream SSE (default); `true` posts `?stream=false` for a JSON result. */
  stream?: boolean
}

/**
 * Runs the agent in a session.
 *
 * Default: SSE stream of {@link AgentRunEvent}s to `onEvent` (the terminal
 * `done` frame ends the stream and is not delivered). With `stream: false`
 * the promise resolves to the final {@link AgentRunResult} instead.
 * A busy session rejects with a 409 {@link ApiError} before streaming.
 */
export async function runAgent(
  slug: string,
  sessionId: string,
  body: { input: string; messages?: AgentMessage[] },
  opts: StartAgentRun & { onEvent: (event: AgentRunEvent) => void },
): Promise<void>
export async function runAgent(
  slug: string,
  sessionId: string,
  body: { input: string; messages?: AgentMessage[] },
  opts: StartAgentRun & { stream: false },
): Promise<AgentRunResult>
export async function runAgent(
  slug: string,
  sessionId: string,
  body: { input: string; messages?: AgentMessage[] },
  opts: StartAgentRun,
): Promise<void | AgentRunResult> {
  if (opts.stream === false) {
    return apiPost<AgentRunResult>(`/agents/${slug}/sessions/${sessionId}/runs?stream=false`, body, { signal: opts.signal })
  }
  return postSSETyped<AgentRunEvent>(`/agents/${slug}/sessions/${sessionId}/runs`, body, {
    signal: opts.signal,
    map: mapAgentRunEvent,
    onEvent: 'onEvent' in opts ? (opts as { onEvent: (e: AgentRunEvent) => void }).onEvent : () => {},
  })
}

/** POST /agents/{slug}/sessions/{id}/cancel — 204; cancels the active run. */
export function cancelAgentRun(slug: string, sessionId: string, signal?: AbortSignal): Promise<void> {
  return apiPost<void>(`/agents/${slug}/sessions/${sessionId}/cancel`, undefined, { signal })
}
