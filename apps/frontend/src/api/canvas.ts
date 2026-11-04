/**
 * Canvas API — the shared file tree of a chat thread (docs/backend.canvas.md).
 * All paths relative to `/api/v1`.
 *
 * A thread's canvas is a tree of HTML component files (folders = groups) over
 * immutable artifacts; the agents read the same files. Files are never changed
 * in place — every result is a new file. Layout is view-only state stored on
 * the server; user operations (delete / restore / move) become notes the next
 * run of the thread is told about.
 */
import { apiGet, apiPost, apiPut } from './client'

/** One component file of the canvas. */
export interface CanvasFile {
  /** `/pricing/dark/tiers.html` */
  path: string
  /** Immutable html artifact holding the content (`GET /artifacts/{id}/content`). */
  artifact_id: string
  /** Run (= message) that produced it. */
  run_id: string
  /** The file it is a new version of ("" = a new design). */
  source_path: string
  score: number | null
  size: number
  created_at: string
}

export interface CanvasPoint {
  x: number
  y: number
}

/**
 * View layout, keyed by paths: `pos` = `g:<folder>` (top-level groups:
 * absolute canvas position; nested groups: offset) | `c:<file>` (offset),
 * `scale` = file → uniform scale, `dir` = folder → row / col flow.
 */
export interface CanvasLayout {
  pos: Record<string, CanvasPoint>
  scale: Record<string, number>
  dir: Record<string, 'row' | 'col'>
}

/** A user operation the next run will be told about. */
export interface CanvasNote {
  id: string
  text: string
  at: string
}

/** GET /threads/{id}/canvas */
export interface Canvas {
  thread_id: string
  files: CanvasFile[]
  layout: CanvasLayout
  pending_notes: CanvasNote[]
}

const base = (thread: string): string => `/threads/${encodeURIComponent(thread)}/canvas`

export function getCanvas(thread: string, signal?: AbortSignal): Promise<Canvas> {
  return apiGet<Canvas>(base(thread), { signal })
}

/** PUT …/layout — last write wins; keys of unknown paths are dropped. */
export function putCanvasLayout(thread: string, layout: CanvasLayout, opts: { signal?: AbortSignal; keepalive?: boolean } = {}): Promise<{ layout: CanvasLayout }> {
  return apiPut<{ layout: CanvasLayout }>(`${base(thread)}/layout`, { layout }, opts)
}

/** POST …/delete — files and folders (recursive), one undoable operation. */
export function deleteCanvasPaths(thread: string, paths: string[]): Promise<{ op_id: string; deleted: string[] }> {
  return apiPost<{ op_id: string; deleted: string[] }>(`${base(thread)}/delete`, { paths })
}

/** POST …/restore — undo a delete (409 when a path is taken again). */
export function restoreCanvasOp(thread: string, opId: string): Promise<{ restored: string[] }> {
  return apiPost<{ restored: string[] }>(`${base(thread)}/restore`, { op_id: opId })
}

/** POST …/move — file → file or folder → folder (rename = move; the target is slugged server-side). */
export function moveCanvasPath(thread: string, from: string, to: string): Promise<{ moved: { from: string; to: string }[] }> {
  return apiPost<{ moved: { from: string; to: string }[] }>(`${base(thread)}/move`, { from, to })
}

/* ---------- path helpers ---------- */

/** Parent folder of a path (`/` for top-level entries). */
export function dirOf(path: string): string {
  const i = path.lastIndexOf('/')
  return i <= 0 ? '/' : path.slice(0, i)
}

/** Last segment of a path (`/` stays `/`). */
export function baseOf(path: string): string {
  return path === '/' ? '/' : path.slice(path.lastIndexOf('/') + 1)
}

/** `p` equals `folder` or lies below it. */
export function isWithin(p: string, folder: string): boolean {
  return folder === '/' || p === folder || p.startsWith(`${folder}/`)
}

export function joinPath(folder: string, name: string): string {
  return folder === '/' ? `/${name}` : `${folder}/${name}`
}
