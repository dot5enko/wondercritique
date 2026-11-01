/**
 * Fetch-based SSE reader.
 *
 * `EventSource` can't do POST or send headers, and both streaming endpoints of
 * this app need an Authorization header — agent chat runs are POSTed and
 * `/runs/{id}/events` is GET — so streams are consumed through `fetch` +
 * `ReadableStream` instead.
 *
 * Frames follow the SSE wire format: `event: <name>` / `data: <json>` lines,
 * separated by a blank line, with `: comment` lines used as heartbeats
 * (agentkit sends `: ping`).
 */
import { API_ROOT, getSession, toApiError } from './client'

export interface SSEMessage {
  /** SSE `event:` field, e.g. `text_delta` or `run_completed`. */
  event: string
  /** SSE `data:` payload (multiple data lines joined with `\n`). */
  data: string
}

export interface SSEOptions {
  signal?: AbortSignal
  onEvent: (message: SSEMessage) => void
}

export interface SSEStreamOptions<E> extends Omit<SSEOptions, 'onEvent'> {
  /** Converts a raw frame into a typed event; return null to skip a frame (e.g. heartbeats/done). */
  map: (message: SSEMessage) => E | null
  onEvent: (event: E) => void
}

/** JSON.parse an SSE data payload; empty payloads become `fallback`. */
export function parseSSEJSON<T>(data: string, fallback: T): T {
  if (!data.trim()) return fallback
  return JSON.parse(data) as T
}

async function streamSSE(
  method: 'GET' | 'POST',
  path: string,
  body: unknown,
  opts: { signal?: AbortSignal; onEvent: (message: SSEMessage) => void },
): Promise<void> {
  const headers: Record<string, string> = { Accept: 'text/event-stream' }
  if (method === 'POST') headers['Content-Type'] = 'application/json'
  const session = getSession()
  if (session) headers.Authorization = `Bearer ${session.access_token}`

  let res: Response
  try {
    res = await fetch(`${API_ROOT}${path}`, {
      method,
      headers,
      body: method === 'POST' ? JSON.stringify(body) : undefined,
      signal: opts.signal,
    })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new Error(err instanceof Error ? err.message : 'Network error')
  }

  if (!res.ok || !res.body) {
    // Error responses are regular JSON (`{"error":{...}}`), not an event stream.
    const err = res.ok ? new Error('Streaming unsupported') : await toApiError(res)
    throw err
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  // Current frame being assembled (a frame ends at a blank line).
  let event = 'message'
  let data: string[] = []

  const dispatch = () => {
    if (data.length === 0) return
    opts.onEvent({ event, data: data.join('\n') })
    event = 'message'
    data = []
  }

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    let nl: number
    while ((nl = buffer.indexOf('\n')) !== -1) {
      // CRLF-safe: strip a trailing \r from LF-delimited lines.
      let line = buffer.slice(0, nl)
      buffer = buffer.slice(nl + 1)
      if (line.endsWith('\r')) line = line.slice(0, -1)

      if (line === '') {
        dispatch()
        continue
      }
      if (line.startsWith(':')) continue // comment / keepalive
      if (line.startsWith('event:')) {
        event = line.slice(6).trim()
        continue
      }
      if (line.startsWith('data:')) {
        data.push(line.slice(5).replace(/^ /, ''))
        continue
      }
      // `id:` and `retry:` fields are ignored — nothing here reconnects.
    }
  }
  dispatch() // flush a frame not terminated by a blank line
}

/** Streams `text/event-stream` from a POST endpoint (agent runs, studio try). */
export function postSSE(path: string, body: unknown, opts: SSEOptions): Promise<void> {
  return streamSSE('POST', path, body, opts)
}

/** Streams `text/event-stream` from a GET endpoint (`GET /runs/{id}/events`). */
export function getSSE(path: string, opts: SSEOptions): Promise<void> {
  return streamSSE('GET', path, undefined, opts)
}

/** Like {@link postSSE} but every frame is mapped to a typed event first. */
export function postSSETyped<E>(path: string, body: unknown, opts: SSEStreamOptions<E>): Promise<void> {
  return streamSSE('POST', path, body, {
    signal: opts.signal,
    onEvent: (message) => {
      const mapped = opts.map(message)
      if (mapped !== null) opts.onEvent(mapped)
    },
  })
}

/** Like {@link getSSE} but every frame is mapped to a typed event first. */
export function getSSETyped<E>(path: string, opts: SSEStreamOptions<E>): Promise<void> {
  return streamSSE('GET', path, undefined, {
    signal: opts.signal,
    onEvent: (message) => {
      const mapped = opts.map(message)
      if (mapped !== null) opts.onEvent(mapped)
    },
  })
}
