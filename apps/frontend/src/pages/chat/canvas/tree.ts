/**
 * The canvas as a folder tree: files of `GET /threads/{id}/canvas` grouped by
 * folder (folders are implicit — a folder exists while a file lives below it),
 * plus temporary folders for runs still working on a result whose target
 * folder (`run.canvas_folder`) doesn't exist yet.
 */
import { baseOf, dirOf, type CanvasFile } from '../../../api/canvas'

export interface FolderNode {
  /** `/pricing/dark`; `/` = the root (files directly under `/`). */
  path: string
  /** Last segment ("/" for the root). */
  name: string
  /** Files directly in this folder, by name. */
  files: CanvasFile[]
  /** Subfolders, oldest first. */
  folders: FolderNode[]
  /** Runs in progress / failed whose result goes here (their pending cards). */
  pending: string[]
  /** Exists only for a pending run (no file yet). */
  virtual: boolean
  /** Earliest file below (ISO time; "" for virtual folders) — ordering and "from message N". */
  first: string
  /** Run of the earliest file below ("" for virtual folders). */
  firstRun: string
}

const node = (path: string): FolderNode => ({
  path, name: path === '/' ? '/' : baseOf(path), files: [], folders: [], pending: [], virtual: true, first: '', firstRun: '',
})

/** A pending run of the chat: its id and the folder its result goes to. */
export interface PendingRun {
  id: string
  folder: string
}

/** Builds the tree (root = `/`). */
export function buildTree(files: CanvasFile[], pending: PendingRun[]): FolderNode {
  const root = node('/')
  const byPath = new Map<string, FolderNode>([['/', root]])
  const ensure = (path: string): FolderNode => {
    let n = byPath.get(path)
    if (n) return n
    n = node(path)
    byPath.set(path, n)
    ensure(dirOf(path)).folders.push(n)
    return n
  }
  for (const f of files) {
    const folder = ensure(dirOf(f.path))
    folder.files.push(f)
    // every ancestor is real and knows its earliest file
    for (let p = folder.path; ; p = dirOf(p)) {
      const n = byPath.get(p) as FolderNode
      n.virtual = false
      if (!n.first || f.created_at < n.first) {
        n.first = f.created_at
        n.firstRun = f.run_id
      }
      if (p === '/') break
    }
  }
  for (const r of pending) ensure(r.folder || '/').pending.push(r.id)
  const sort = (n: FolderNode): void => {
    n.files.sort((a, b) => a.path.localeCompare(b.path))
    // oldest first; folders made for pending runs go last
    n.folders.sort((a, b) => (a.first || '~').localeCompare(b.first || '~') || a.path.localeCompare(b.path))
    n.folders.forEach(sort)
  }
  sort(root)
  return root
}

/**
 * The canvas groups: every top-level folder, plus the root itself when files
 * (or pending runs) sit directly under `/`.
 */
export function topGroups(root: FolderNode): FolderNode[] {
  const out = [...root.folders]
  if (root.files.length > 0 || root.pending.length > 0) {
    out.unshift({ ...root, folders: [] })
  }
  return out
}

/** Every folder of the tree by path (root included). */
export function indexFolders(root: FolderNode): Map<string, FolderNode> {
  const m = new Map<string, FolderNode>()
  const walk = (n: FolderNode): void => {
    m.set(n.path, n)
    n.folders.forEach(walk)
  }
  walk(root)
  return m
}

/** Files below a folder (recursive). */
export function filesBelow(n: FolderNode): CanvasFile[] {
  return [...n.files, ...n.folders.flatMap(filesBelow)]
}

/** Display name of a folder. */
export const folderLabel = (path: string): string => (path === '/' ? 'Top level' : `${baseOf(path)}/`)

/** Display name of a component file (no `.html`). */
export const componentLabel = (path: string): string => baseOf(path).replace(/\.html?$/i, '')
