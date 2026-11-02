/**
 * Runs API — pipeline executions and their artifacts (plan §5).
 * All paths relative to `/api/v1`.
 *
 * Implemented by the backend engine (docs/backend.engine.md):
 * - run/step statuses follow the agentkit `RunStatus` vocabulary plus `pending`.
 * - `GET /runs` answers `{items, next_cursor: null}` (offset paging, newest first).
 * - critique scores run 0–10.
 */
import { apiGet, apiPost, toQuery, API_ROOT, getSession, refreshSession, toApiError } from './client'
import type { TokenUsage } from './types'
import { getSSETyped, type SSEMessage } from './sse'
import type { PipelineBody } from './studio'

export type { TokenUsage } from './types'

/** Lifecycle of a run / step. `interrupted` = the process died mid-run (plan §5). */
export type RunStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled' | 'interrupted'

/** True once a run can no longer change state (plan §5: a crash becomes `interrupted`). */
export function isTerminalRunStatus(status: RunStatus): boolean {
  return status !== 'pending' && status !== 'running'
}

/** A value for one pipeline input port: plain text or a reference to an existing artifact. */
export type RunInputValue = string | { artifact_id: string }

export interface StartRunBody {
  /** Pin an explicit pipeline version; omitted = latest published. */
  pipeline_version_id?: string
  /** Studio test run (may use drafts, not counted like production runs). */
  is_test?: boolean
  /**
   * Follow-up message in the same chat — the engine seeds the generator with
   * the thread's previous brief and final HTML. The first run of a chat omits
   * it; the backend sets the run's thread id to its own id.
   */
  thread_id?: string
  /**
   * Which pipeline to run (its latest published version is pinned
   * server-side). Omitted = the published pipeline with slug `default`.
   */
  pipeline_id?: string
  input: Record<string, RunInputValue>
}

/** POST /runs → 201 `{id, thread_id, status}`; the run executes in the background. */
export function startRun(body: StartRunBody, signal?: AbortSignal): Promise<{ id: string; thread_id?: string }> {
  return apiPost<{ id: string; thread_id?: string }>('/runs', body, { signal })
}

/** Pipeline reference embedded in run list items. */
export interface RunPipelineRef {
  id: string
  slug: string
  name: string
  version: number | null
}

/** GET /runs list item. */
export interface RunListItem {
  id: string
  status: RunStatus
  pipeline: RunPipelineRef
  iterations: number
  best_score: number | null
  usage: TokenUsage | null
  cost: number | null
  created_at: string
  /** Chat thread: the id of the thread's first run (every run has one). */
  thread_id?: string | null
  /** Used to title chat threads by their first brief. */
  input?: Record<string, RunInputValue>
}

export interface ListRunsParams {
  limit?: number
  offset?: number
  cursor?: string
  /** Serialized as `filter[thread_id]=…`. */
  filter?: { thread_id?: string }
}

export function listRuns(
  params: ListRunsParams = {},
  signal?: AbortSignal,
): Promise<{ items: RunListItem[]; next_cursor?: string | null }> {
  const query: Record<string, unknown> = { ...params }
  if (params.filter?.thread_id) query['filter[thread_id]'] = params.filter.thread_id
  delete (query as { filter?: unknown }).filter
  return apiGet<{ items: RunListItem[]; next_cursor?: string | null }>(`/runs${toQuery(query)}`, { signal })
}

/* ---------- artifacts ---------- */

export type ArtifactKind = 'html' | 'image' | 'json'

/** One persisted output of a node run (`artifacts` table). */
export interface Artifact {
  id: string
  run_id: string
  iteration: number
  node_id: string
  kind: ArtifactKind
  /** Content-addressed blob hash (`blobs` table); resolved via `artifactUrl`. */
  blob_sha: string
  /** Score attached by the Gate/critic, when any. */
  score?: number | null
  created_at?: string
}

/**
 * Content of one artifact, fetched with auth and exposed as an object URL.
 * `kind` is inferred from the served MIME type — this is how artifacts
 * discovered live via `step_completed` events (which carry only ids) get
 * classified for rendering before the next full `getRun` refresh.
 */
export interface ArtifactContent {
  id: string
  /** Object URL of the blob — the caller owns it and must revoke it when done. */
  url: string
  /** MIME type as served, falling back to `application/octet-stream`. */
  mime: string
  /** `image/*` → image, html/xml-ish → html, everything else → json. */
  kind: ArtifactKind
  /** Text of textual artifacts (≤ 2 MB); null for images and larger blobs. */
  text: string | null
}

async function fetchArtifactResponse(id: string, signal?: AbortSignal): Promise<Response> {
  const attempt = (): Promise<Response> => {
    const headers: Record<string, string> = { Accept: '*/*' }
    const session = getSession()
    if (session) headers.Authorization = `Bearer ${session.access_token}`
    return fetch(`${API_ROOT}/artifacts/${id}/content`, { headers, signal })
  }
  let res = await attempt()
  if (res.status === 401) {
    try {
      await refreshSession()
      res = await attempt()
    } catch {
      // refreshSession() already cleared the session; surface the original 401 below
    }
  }
  if (!res.ok) throw await toApiError(res)
  return res
}

const MAX_ARTIFACT_TEXT_BYTES = 2 * 1024 * 1024

export async function fetchArtifact(id: string, signal?: AbortSignal): Promise<ArtifactContent> {
  const res = await fetchArtifactResponse(id, signal)
  const blob = await res.blob()
  const mime = blob.type || 'application/octet-stream'
  const kind: ArtifactKind = mime.startsWith('image/') ? 'image' : /html|xml/.test(mime) ? 'html' : 'json'
  let text: string | null = null
  if (kind !== 'image' && blob.size <= MAX_ARTIFACT_TEXT_BYTES) {
    try {
      text = await blob.text()
    } catch {
      text = null
    }
  }
  return { id, url: URL.createObjectURL(blob), mime, kind, text }
}

/**
 * Object URL for an artifact's content (`GET /artifacts/{id}/content`).
 * Async (fetches + authorizes); callers should revoke the URL when done.
 * Prefer {@link fetchArtifact} when the kind/mime or text is needed too.
 */
export async function artifactUrl(id: string, signal?: AbortSignal): Promise<string> {
  return (await fetchArtifact(id, signal)).url
}

/* ---------- steps & full run ---------- */

/** One scheduled node execution (`run_steps` table). */
export interface RunStep {
  id: string
  run_id: string
  iteration: number
  node_id: string
  status: RunStatus
  /** Versions pinned for this step, e.g. `{ agent: 3, model_set: 1 }`. */
  pinned_versions?: Record<string, number>
  /** Resolved model, e.g. `meta/muse-spark-1.3-contributor`. */
  model?: string | null
  agent_session_id?: string | null
  output?: unknown
  usage?: TokenUsage | null
  error?: string | null
  started_at?: string
  completed_at?: string
  /**
   * Live-only: merged from `step_completed` SSE events — the REST shape of a
   * step does not include them, so ids here may not (yet) appear in `Run.artifacts`.
   */
  artifact_ids?: string[]
}

/** GET /runs/{id} — the run with its steps and artifacts. */
export interface Run {
  id: string
  status: RunStatus
  pipeline: RunPipelineRef
  pipeline_version_id: string
  is_test: boolean
  input: Record<string, RunInputValue>
  iteration: number
  best_artifact_id: string | null
  usage: TokenUsage | null
  cost: number | null
  error: string | null
  created_at: string
  updated_at?: string
  /** Chat thread: the id of the thread's first run. */
  thread_id?: string | null
  /** The pipeline body this run executed (pinned snapshot; present for every viewer). */
  pipeline_body?: PipelineBody
  steps: RunStep[]
  artifacts: Artifact[]
}

export function getRun(id: string, signal?: AbortSignal): Promise<Run> {
  return apiGet<Run>(`/runs/${id}`, { signal })
}

/** POST /runs/{id}/cancel — 204. */
export function cancelRun(id: string, signal?: AbortSignal): Promise<void> {
  return apiPost<void>(`/runs/${id}/cancel`, undefined, { signal })
}

/* ---------- event stream (GET /runs/{id}/events, SSE) ---------- */

/**
 * Engine events per plan §5, discriminated on the SSE `event` name (each
 * payload repeats it under `event`). The stream replays the run's history on
 * connect and closes after `run_completed` / `run_failed`. A cancelled run
 * ends with `run_failed` (refetch the run for its `cancelled` status).
 */
export type RunEvent =
  | { event: 'step_started'; run_id: string; iteration: number; node_id: string }
  | {
      event: 'step_completed'
      run_id: string
      iteration: number
      node_id: string
      status: Exclude<RunStatus, 'pending' | 'running'>
      usage?: TokenUsage
      artifact_ids?: string[]
      error?: string
    }
  | { event: 'iteration_started'; run_id: string; iteration: number }
  | {
      event: 'iteration_completed'
      run_id: string
      iteration: number
      best_score?: number | null
      artifact_ids?: string[]
    }
  | {
      event: 'run_completed'
      run_id: string
      best_artifact_id?: string | null
      usage?: TokenUsage
      cost?: number
    }
  | { event: 'run_failed'; run_id: string; error: string }

/** Maps a raw SSE frame to a {@link RunEvent}, or null for heartbeats/unknown names. */
export function mapRunEvent(message: SSEMessage): RunEvent | null {
  if (!message.data.trim()) return null // `: ping` comments never reach here, but guard anyway
  try {
    const ev = JSON.parse(message.data) as RunEvent & { event?: string }
    if (typeof ev.event !== 'string') return null
    switch (ev.event) {
      case 'step_started':
      case 'step_completed':
      case 'iteration_started':
      case 'iteration_completed':
      case 'run_completed':
      case 'run_failed':
        return ev as RunEvent
      default:
        return null
    }
  } catch {
    return null
  }
}

/**
 * Subscribes to the live event stream of a run. The stream ends when the run
 * completes/fails or the signal aborts.
 */
export function streamRunEvents(
  id: string,
  opts: { signal?: AbortSignal; onEvent: (event: RunEvent) => void },
): Promise<void> {
  return getSSETyped<RunEvent>(`/runs/${id}/events`, { signal: opts.signal, map: mapRunEvent, onEvent: opts.onEvent })
}
