/**
 * Typed fetch wrapper for the backend API.
 *
 * Every module under `src/api/` passes paths relative to the API root
 * (`/api/v1`), e.g. `apiGet('/auth/me')`. The root itself is
 * `${VITE_API_BASE_URL ?? ''}/api/v1` — an empty base means same-origin
 * requests, which the Vite dev server proxies to http://localhost:8080.
 *
 * Features: JSON in/out, `Authorization: Bearer` from the stored session,
 * the shared error shape (`{"error":{"code","message","fields"}}`) parsed
 * into {@link ApiError}, and a single-flight access-token refresh with one
 * retry of the original request on 401.
 */
import type { TokenPair, User } from './types'

export type { TokenPair, User } from './types'

const BASE = import.meta.env.VITE_API_BASE_URL ?? ''

/** All paths handed to the api* helpers are relative to this root. */
export const API_ROOT = `${BASE}/api/v1`

/* ---------- session persistence (localStorage) ---------- */

const KEY_ACCESS = 'wc_access'
const KEY_REFRESH = 'wc_refresh'
const KEY_USER = 'wc_user'

export interface StoredSession {
  access_token: string
  refresh_token: string
  user: User
}

function readJSON<T>(key: string): T | null {
  try {
    const raw = localStorage.getItem(key)
    return raw ? (JSON.parse(raw) as T) : null
  } catch {
    return null
  }
}

/** Full stored session, or null when anonymous. */
export function getSession(): StoredSession | null {
  const access_token = localStorage.getItem(KEY_ACCESS)
  const refresh_token = localStorage.getItem(KEY_REFRESH)
  if (!access_token || !refresh_token) return null
  const user = readJSON<User>(KEY_USER)
  if (!user) return null
  return { access_token, refresh_token, user }
}

export function setSession(session: StoredSession): void {
  localStorage.setItem(KEY_ACCESS, session.access_token)
  localStorage.setItem(KEY_REFRESH, session.refresh_token)
  localStorage.setItem(KEY_USER, JSON.stringify(session.user))
}

/** Drops tokens + user from localStorage. */
export function clearSession(): void {
  localStorage.removeItem(KEY_ACCESS)
  localStorage.removeItem(KEY_REFRESH)
  localStorage.removeItem(KEY_USER)
}

/* ---------- session-expired listeners ---------- */

const expiredListeners = new Set<() => void>()

/** Subscribes to "refresh failed, session gone" — the auth provider routes to /login. */
export function onSessionExpired(cb: () => void): () => void {
  expiredListeners.add(cb)
  return () => {
    expiredListeners.delete(cb)
  }
}

function notifySessionExpired(): void {
  for (const cb of expiredListeners) cb()
}

/* ---------- errors ---------- */

export class ApiError extends Error {
  /** HTTP status (0 for network failures / malformed responses). */
  readonly status: number
  /** Machine-readable code from the shared error shape, e.g. `invalid_request`. */
  readonly code: string
  /** Per-field validation messages when the backend sent them. */
  readonly fields?: Record<string, string>
  /** Seconds from a `Retry-After` header (429 rate limits), when present. */
  readonly retryAfter?: number

  constructor(status: number, code: string, message: string, fields?: Record<string, string>, retryAfter?: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.fields = fields
    this.retryAfter = retryAfter
  }
}

function retryAfterOf(res: Response): number | undefined {
  const raw = res.headers.get('Retry-After')
  if (!raw) return undefined
  const n = Number(raw)
  return Number.isFinite(n) ? n : undefined
}

/** Parses a non-2xx Response into an {@link ApiError} (shared with the SSE reader). */
export async function toApiError(res: Response): Promise<ApiError> {
  const retryAfter = retryAfterOf(res)
  let code = 'error'
  let message = res.statusText || `Request failed (${res.status})`
  let fields: Record<string, string> | undefined
  try {
    const body = (await res.json()) as { error?: { code?: string; message?: string; fields?: Record<string, string> } }
    if (body?.error) {
      if (body.error.code) code = body.error.code
      if (body.error.message) message = body.error.message
      if (body.error.fields) fields = body.error.fields
    }
  } catch {
    // non-JSON body — keep the status-text fallback
  }
  return new ApiError(res.status, code, message, fields, retryAfter)
}

/* ---------- request core ---------- */

export interface RequestOptions {
  signal?: AbortSignal
  headers?: Record<string, string>
  /** Auth endpoints must not trigger the refresh-retry loop. */
  noAuth?: boolean
  /** Let the request outlive the page (flushing state on `pagehide`). */
  keepalive?: boolean
}

interface AttemptOptions extends RequestOptions {
  /** Set on the one automatic retry after a successful refresh. */
  retried?: boolean
}

async function send(method: string, path: string, body: unknown, opts: AttemptOptions): Promise<Response> {
  const headers: Record<string, string> = { Accept: 'application/json', ...opts.headers }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const session = opts.noAuth ? null : getSession()
  if (session) headers.Authorization = `Bearer ${session.access_token}`

  let res: Response
  try {
    res = await fetch(`${API_ROOT}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: opts.signal,
      keepalive: opts.keepalive,
    })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new ApiError(0, 'network', err instanceof Error ? err.message : 'Network error')
  }

  if (res.status === 401 && !opts.noAuth && !opts.retried && localStorage.getItem(KEY_REFRESH)) {
    try {
      await refreshSession()
    } catch {
      // refreshSession() already cleared the session and notified listeners
      throw await toApiError(res)
    }
    return send(method, path, body, { ...opts, retried: true })
  }

  if (!res.ok) throw await toApiError(res)
  return res
}

/* ---------- single-flight refresh ---------- */

let refreshPromise: Promise<void> | null = null

/**
 * Exchanges the stored refresh token for a new pair and persists it.
 * Concurrent callers share one in-flight request; on failure the session is
 * cleared and {@link onSessionExpired} listeners fire.
 */
export function refreshSession(): Promise<void> {
  refreshPromise ??= doRefresh().finally(() => {
    refreshPromise = null
  })
  return refreshPromise
}

async function doRefresh(): Promise<void> {
  const refresh_token = localStorage.getItem(KEY_REFRESH)
  if (!refresh_token) {
    clearSession()
    notifySessionExpired()
    throw new ApiError(401, 'unauthorized', 'Not signed in')
  }
  try {
    const res = await fetch(`${API_ROOT}/auth/refresh`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify({ refresh_token }),
    })
    if (!res.ok) throw await toApiError(res)
    const pair = (await res.json()) as TokenPair
    const previous = readJSON<User>(KEY_USER)
    setSession({
      access_token: pair.access_token,
      refresh_token: pair.refresh_token,
      user: pair.user ?? previous ?? { id: '', email: '', name: '', role: 'user' },
    })
  } catch (err) {
    clearSession()
    notifySessionExpired()
    throw err
  }
}

/* ---------- JSON helpers ---------- */

/** Serializes simple query params; undefined/null (and non-primitive) values are skipped. */
export function toQuery<T extends object>(params: T): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || typeof value === 'object') continue
    search.set(key, String(value))
  }
  const s = search.toString()
  return s ? `?${s}` : ''
}

async function parseJSON<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T
  const text = await res.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}

export async function apiGet<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  return parseJSON<T>(await send('GET', path, undefined, opts))
}

export async function apiPost<T>(path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
  return parseJSON<T>(await send('POST', path, body, opts))
}

export async function apiPut<T>(path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
  return parseJSON<T>(await send('PUT', path, body, opts))
}

export async function apiPatch<T>(path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
  return parseJSON<T>(await send('PATCH', path, body, opts))
}

export async function apiDelete<T = void>(path: string, opts: RequestOptions = {}): Promise<T> {
  return parseJSON<T>(await send('DELETE', path, undefined, opts))
}

/**
 * Fetches a binary resource with auth (e.g. `GET /artifacts/{id}/content`) and
 * returns an object URL for it. The caller owns the URL and should
 * `URL.revokeObjectURL` it when done (a `useEffect` cleanup is the usual spot).
 */
export async function apiBlob(path: string, opts: RequestOptions = {}): Promise<string> {
  const res = await send('GET', path, undefined, opts)
  return URL.createObjectURL(await res.blob())
}

/** A file download: the body plus the name from `Content-Disposition` (`null` when absent). */
export interface DownloadedFile {
  blob: Blob
  filename: string | null
}

/** POSTs JSON and returns the (binary) response as a file, e.g. an export. */
export async function apiPostFile(path: string, body?: unknown, opts: RequestOptions = {}): Promise<DownloadedFile> {
  const res = await send('POST', path, body, { ...opts, headers: { Accept: '*/*', ...opts.headers } })
  const disposition = res.headers.get('Content-Disposition') ?? ''
  const m = /filename\*=UTF-8''([^;]+)|filename="?([^";]+)"?/i.exec(disposition)
  const filename = m ? decodeURIComponent(m[1] ?? m[2]) : null
  return { blob: await res.blob(), filename }
}

/** Saves a blob through a temporary `<a download>`. */
export function saveFile(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}
