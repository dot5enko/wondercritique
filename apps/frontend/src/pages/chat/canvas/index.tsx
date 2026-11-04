/**
 * Design canvas — one chat, full screen (`/chat/:threadId`, `/chat/new`).
 *
 * The canvas is a view of the chat's shared file tree (GET /threads/{id}/canvas):
 * every folder is a group block (FolderGroup, subfolders nested inside), every
 * HTML file a component; the agents read the same files under /canvas. Runs
 * still working on a result show as pending cards in their target folder
 * (`run.canvas_folder`). Tapping a component or a group selects it (Shift/⌘
 * or "Select more" mode for several); the selection travels with the next
 * message as `context_paths`, so the agent gets those files' HTML and writes
 * new versions next to them (files are never changed in place). The long-press
 * menu and the floating selection toolbar offer canned requests ("Generate
 * variants", …) that simply send a message about the selection, plus layout
 * actions (move / scale handles, arrange in a row / column — stored on the
 * server, useCanvasLayout), rename / move and delete (undoable; the server
 * tells the next run about it).
 */
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router'
import {
  ArrowLeft,
  Columns3,
  Copy,
  FolderInput,
  FolderPen,
  ListChecks,
  ListTree,
  Maximize,
  Maximize2,
  Minus,
  Moon,
  PenLine,
  Plus,
  RotateCcw,
  Rows3,
  Smartphone,
  Sparkles,
  Trash2,
  Wand2,
  X,
} from 'lucide-react'
import { Button, cx, IconButton } from '../../../ui'
import { Callout } from '../../../ui/Display'
import { Modal } from '../../../components/Modal'
import { ErrorState, isBackendMissing } from '../../../components'
import { useAsync } from '../../../hooks'
import {
  isTerminalRunStatus,
  listRuns,
  startRun,
  type Run,
  type RunListItem,
  type RunStatus,
} from '../../../api/runs'
import {
  baseOf,
  deleteCanvasPaths,
  dirOf,
  getCanvas,
  joinPath,
  moveCanvasPath,
  restoreCanvasOp,
  type CanvasFile,
} from '../../../api/canvas'
import { getVersion, listDefs } from '../../../api/studio'
import { parseInputFields } from '../../runs/NewRunModal'
import { RunDetailView } from '../../runs/detail'
import { useThreads } from '../threads'
import { useViewport, type HandleInfo, type MenuInfo, type TapInfo } from './useViewport'
import { FolderGroup, type GroupEnv } from './FolderGroup'
import { buildTree, filesBelow, folderLabel, indexFolders, topGroups, type FolderNode } from './tree'
import { ContextMenu, type MenuAction } from './ContextMenu'
import { SelectionBar } from './SelectionBar'
import { Composer, type ContextChip } from './Composer'
import { loadComponentHtml, useComponentHtml } from './ComponentFrame'
import { hasArrangement, MAX_SCALE, MIN_SCALE, useCanvasLayout, type GroupDir, type Point } from './layout'

const EXAMPLES = [
  'A pricing section with three tiers, dark theme, rounded cards',
  'Five call-to-action button styles for a fitness app',
  'A user profile card with avatar, stats and a follow button',
]

/** Canned requests of the long-press menu (sent as messages about the selection). */
const QUICK_ACTIONS: { key: string; label: string; icon: typeof Sparkles; prompt: (many: boolean) => string }[] = [
  {
    key: 'variants',
    label: 'Generate variants',
    icon: Sparkles,
    prompt: (many) => many
      ? 'Generate 3 new variants based on the selected components'
      : 'Generate 3 distinct variants of the selected component',
  },
  {
    key: 'improve',
    label: 'Improve the design',
    icon: Wand2,
    prompt: (many) => `Improve the selected component${many ? 's' : ''}: polish spacing, hierarchy and typography, and fix any visual issues`,
  },
  {
    key: 'mobile',
    label: 'Adapt for mobile',
    icon: Smartphone,
    prompt: (many) => `Adapt the selected component${many ? 's' : ''} for a 390px-wide mobile screen`,
  },
  {
    key: 'dark',
    label: 'Dark version',
    icon: Moon,
    prompt: (many) => `Make a dark-theme version of the selected component${many ? 's' : ''}`,
  },
]

/** Screen space kept free around fitted content (top bar, composer). */
const FIT_INSET = { top: 72, right: 24, bottom: 150, left: 24 }

/** Gaps of "Arrange" (= the flex gaps): components in a row / column, groups. */
const ARRANGE_GAP = { row: 64, col: 80, group: 160 }

/** The chat title collapses to the back arrow this long after the page opens. */
const TITLE_COLLAPSE_MS = 3500
/**
 * Zoom − / % / + / fit buttons. Hidden: pinch / ctrl+wheel / keys cover it on
 * touch and trackpads; kept for a future desktop toggle.
 */
const SHOW_ZOOM_CONTROLS = false

/** Files one message may be about (the engine's cap on `context_paths`). */
const MAX_CONTEXT_FILES = 12

const selectorOf = (key: string): string => `[data-sel="${CSS.escape(key)}"]`
const clamp = (v: number, lo: number, hi: number): number => Math.min(hi, Math.max(lo, v))

/** Text of a run's message: the pipeline's primary field, else `brief`, else the first text value. */
function briefOf(input: Record<string, unknown> | undefined, field: string): string {
  const v = input?.[field] ?? input?.['brief']
  if (typeof v === 'string' && v.trim()) return v.trim()
  for (const x of Object.values(input ?? {})) if (typeof x === 'string' && x.trim() && !x.startsWith('wcblob://')) return x.trim()
  return '(message)'
}

const errorText = (err: unknown, fallback: string): string =>
  isBackendMissing(err) ? "The canvas backend can't be reached." : err instanceof Error && err.message ? err.message : fallback

/**
 * Input field the message text goes to: the default pipeline's first non-image
 * field (admins can read pipeline defs), else `brief` — which is also what the
 * engine falls back to.
 */
function usePrimaryField(): string {
  const q = useAsync(async (signal): Promise<string> => {
    const defs = await listDefs('pipeline', { limit: 100 }, signal)
    const published = defs.items.filter((d) => d.latest_version != null)
    const def = published.find((p) => p.slug === 'default') ?? published[0]
    if (!def || def.latest_version == null) return 'brief'
    const ver = await getVersion('pipeline', def.id, def.latest_version, signal)
    const f = parseInputFields(ver.body).find((x) => (x.type ?? 'text').toLowerCase() !== 'image')
    return f?.name ?? 'brief'
  }, [])
  return q.data ?? 'brief'
}

export default function CanvasPage() {
  const { threadId: param } = useParams()
  const threadId = param && param !== 'new' ? param : null
  const navigate = useNavigate()
  const { query: threadsQ, threads } = useThreads()
  const field = usePrimaryField()
  const title = threads.find((t) => t.id === threadId)?.title ?? (threadId ? '' : 'New chat')

  /* ---------- the thread's runs (= messages) ---------- */

  const [runs, setRuns] = useState<RunListItem[]>([])
  const [runsError, setRunsError] = useState<unknown>(null)
  const [runsLoading, setRunsLoading] = useState(false)
  const runsRef = useRef(runs)
  runsRef.current = runs

  const loadRuns = useCallback(async (id: string, signal?: AbortSignal): Promise<void> => {
    setRunsLoading(true)
    setRunsError(null)
    try {
      const res = await listRuns({ filter: { thread_id: id }, limit: 200 }, signal)
      if (!signal?.aborted) setRuns([...res.items].sort((a, b) => a.created_at.localeCompare(b.created_at)))
    } catch (err: unknown) {
      if (!signal?.aborted) setRunsError(err)
    } finally {
      if (!signal?.aborted) setRunsLoading(false)
    }
  }, [])

  /* ---------- the canvas: the thread's file tree (+ the stored layout) ---------- */

  const [files, setFiles] = useState<CanvasFile[]>([])
  const [canvasError, setCanvasError] = useState<unknown>(null)
  const { layout, setPos, setScale, setDir, reset: resetLayout, adopt, flush } = useCanvasLayout(threadId)

  const loadCanvas = useCallback(async (id: string, signal?: AbortSignal): Promise<void> => {
    try {
      const c = await getCanvas(id, signal)
      if (signal?.aborted) return
      setFiles(c.files)
      setCanvasError(null)
      adopt(c.layout)
    } catch (err: unknown) {
      if (!signal?.aborted) setCanvasError(err)
    }
  }, [adopt])

  const [statuses, setStatuses] = useState<Record<string, RunStatus>>({})
  const [selection, setSelection] = useState<string[]>([])
  const [selectMode, setSelectMode] = useState(false)

  useEffect(() => {
    setStatuses({})
    setSelection([])
    setSelectMode(false)
    setFiles([])
    setCanvasError(null)
    if (!threadId) {
      setRuns([])
      setRunsError(null)
      return
    }
    const ctrl = new AbortController()
    void loadRuns(threadId, ctrl.signal)
    void loadCanvas(threadId, ctrl.signal)
    return () => ctrl.abort()
  }, [threadId, loadRuns, loadCanvas])

  /** Live status of a message's run (pending cards report it). A run finishing → its files land in the tree. */
  const onStatus = useCallback((runId: string, status: RunStatus) => {
    setStatuses((prev) => (prev[runId] === status ? prev : { ...prev, [runId]: status }))
    const listed = runsRef.current.find((r) => r.id === runId)
    if (threadId && isTerminalRunStatus(status) && listed && !isTerminalRunStatus(listed.status)) {
      void loadCanvas(threadId)
      void loadRuns(threadId)
    }
  }, [threadId, loadCanvas, loadRuns])

  // The chat list mirrors finished runs without polling.
  const finished = Object.values(statuses).filter(isTerminalRunStatus).length
  useEffect(() => {
    if (finished > 0) threadsQ.reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [finished])

  const runIndex = useMemo(() => new Map(runs.map((r, i) => [r.id, i + 1])), [runs])
  const messageOf = useCallback((runId: string) => {
    const r = runs.find((x) => x.id === runId)
    return r ? { index: runIndex.get(runId) ?? 0, brief: briefOf(r.input, field) } : null
  }, [runs, runIndex, field])

  /** Messages without a result on the canvas yet: running, failed or stopped (their pending cards). */
  const pendingRuns = useMemo(
    () => runs.filter((r) => (statuses[r.id] ?? r.status) !== 'completed'),
    [runs, statuses],
  )
  const tree = useMemo(
    () => buildTree(files, pendingRuns.map((r) => ({ id: r.id, folder: r.canvas_folder || '/' }))),
    [files, pendingRuns],
  )
  const groups = useMemo(() => topGroups(tree), [tree])
  const folders = useMemo(() => indexFolders(tree), [tree])
  const fileByPath = useMemo(() => new Map(files.map((f) => [f.path, f])), [files])
  const topKeys = useMemo(() => new Set(groups.map((g) => `g:${g.path}`)), [groups])

  /* ---------- selection ---------- */

  const selected = useMemo(() => new Set(selection), [selection])

  /** Paths a selection key stands for (the root group = only the files directly under `/`). */
  const pathsOf = useCallback((key: string): string[] => {
    const p = key.slice(2)
    if (key === 'g:/') return tree.files.map((f) => f.path)
    return key.startsWith('c:') ? (fileByPath.has(p) ? [p] : []) : folders.has(p) ? [p] : []
  }, [tree, fileByPath, folders])

  /** Selection keys → the files they cover (a group = every file below it), deduplicated. */
  const expand = useCallback((keys: string[]): CanvasFile[] => {
    const out = new Map<string, CanvasFile>()
    for (const k of keys) {
      const p = k.slice(2)
      const list = k === 'g:/' ? tree.files : k.startsWith('g:') ? (folders.get(p) ? filesBelow(folders.get(p) as FolderNode) : []) : [fileByPath.get(p)]
      for (const f of list) if (f) out.set(f.path, f)
    }
    return [...out.values()]
  }, [tree, folders, fileByPath])

  /** The message a key's content comes from (Run details). */
  const runOfKey = useCallback((key: string): string | null => {
    const p = key.slice(2)
    if (key.startsWith('c:')) return fileByPath.get(p)?.run_id ?? null
    return folders.get(p)?.firstRun || null
  }, [fileByPath, folders])

  const chips: ContextChip[] = selection.map((k) => ({
    key: k,
    label: k.startsWith('g:') ? folderLabel(k.slice(2)) : baseOf(k.slice(2)),
  }))

  /* ---------- viewport ---------- */

  const viewportRef = useRef<HTMLDivElement>(null)
  const worldRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  /** Keep fitting the newest group while the user hasn't moved the view. */
  const autoFit = useRef(true)
  const [menu, setMenu] = useState<MenuInfo | null>(null)

  const onTap = ({ target, additive }: TapInfo): void => {
    if (!target) {
      if (!selectMode && !additive) setSelection([])
      return
    }
    if (selectMode || additive) {
      setSelection((prev) => (prev.includes(target) ? prev.filter((k) => k !== target) : [...prev, target]))
    } else {
      setSelection((prev) => (prev.length === 1 && prev[0] === target ? [] : [target]))
    }
  }
  const onMenu = (info: MenuInfo): void => {
    if (info.target && !selected.has(info.target)) {
      setSelection((prev) => (selectMode ? [...prev, info.target as string] : [info.target as string]))
    }
    setMenu(info)
  }

  /* ---------- handles: move / resize (DOM moved imperatively, committed on release) ---------- */

  const drag = useRef<{ el: HTMLElement; box: HTMLElement | null; doc: HTMLElement | null; from: Point; base: Point; s0: number } | null>(null)
  const onHandle = (info: HandleInfo): void => {
    const { kind, target, phase, dx, dy } = info
    if (phase === 'start') {
      const el = worldRef.current?.querySelector<HTMLElement>(selectorOf(target))
      if (!el) return
      const box = kind === 'resize' ? el.querySelector<HTMLElement>('[data-frame-box]') : null
      if (kind === 'resize' && !box) return
      let off = layout.pos[target] ?? { x: 0, y: 0 }
      if (topKeys.has(target) && !free) {
        // first group move: every group gets its current place as a canvas position
        const all = groupPlaces()
        setPos(all)
        off = all[target] ?? off
      }
      const doc = box?.querySelector<HTMLElement>('[data-frame-doc]') ?? null
      if (kind === 'resize' && !doc) return
      drag.current = {
        el,
        box,
        doc,
        from: box ? { x: box.offsetWidth, y: box.offsetHeight } : off,
        // the document's natural size (the frame is drawn at base × scale)
        base: doc ? { x: Number(doc.dataset.w), y: Number(doc.dataset.h) } : { x: 0, y: 0 },
        s0: layout.scale[target.slice(2)] ?? 1,
      }
      el.dataset.dragging = ''
      if (!selected.has(target)) setSelection((prev) => (selectMode ? [...prev, target] : [target]))
      return
    }
    const d = drag.current
    if (!d) return
    const { el, box, doc, from, base, s0 } = d
    if (box && doc) {
      // uniform scale: the corner follows whichever axis was dragged further
      const grow = Math.max((from.x + dx) / from.x, (from.y + dy) / from.y)
      const s = phase === 'cancel' ? s0 : clamp(s0 * grow, MIN_SCALE, MAX_SCALE)
      box.style.width = `${base.x * s}px`
      box.style.height = `${base.y * s}px`
      doc.style.transform = s !== 1 ? `scale(${s})` : ''
      if (phase === 'end') setScale(target.slice(2), s)
    } else {
      const p = phase === 'cancel' ? from : { x: from.x + dx, y: from.y + dy }
      el.style.translate = `${p.x}px ${p.y}px`
      el.dataset.dx = String(p.x)
      el.dataset.dy = String(p.y)
      if (phase === 'end') setPos({ [target]: p })
    }
    if (phase === 'end' || phase === 'cancel') {
      delete el.dataset.dragging
      drag.current = null
    }
  }

  /**
   * Free layout: once a top-level group was moved / arranged every one has a
   * canvas position and is absolutely placed (heights changing — results
   * arriving, the zoom-dependent header — never shift the others).
   */
  const free = groups.some((g) => layout.pos[`g:${g.path}`] != null)
  /** Current canvas position of every group (to switch to the free layout). */
  const groupPlaces = (): Record<string, Point> => {
    const out: Record<string, Point> = {}
    for (const g of Array.from(contentRef.current?.querySelectorAll<HTMLElement>('[data-group]') ?? [])) {
      const r = rectOf(g)
      if (r) out[`g:${g.dataset.group}`] = { x: r.x, y: r.y }
    }
    return out
  }
  // New groups in a free layout go below everything (before paint, no flash at 0,0).
  useLayoutEffect(() => {
    if (!free) return
    const els = Array.from(contentRef.current?.querySelectorAll<HTMLElement>('[data-group]') ?? [])
    const missing = els.filter((g) => layout.pos[`g:${g.dataset.group}`] == null)
    if (missing.length === 0) return
    const placed = els.flatMap((g) => (missing.includes(g) ? [] : [rectOf(g)])).filter((r) => r != null)
    let y = Math.max(0, ...placed.map((r) => r.y + r.h)) + ARRANGE_GAP.group
    const x = placed.length > 0 ? Math.min(...placed.map((r) => r.x)) : 0
    const next: Record<string, Point> = {}
    for (const g of missing) {
      next[`g:${g.dataset.group}`] = { x, y }
      y += g.offsetHeight + ARRANGE_GAP.group
    }
    setPos(next)
  })

  const zoomLabel = useRef<HTMLButtonElement>(null)
  const vp = useViewport(viewportRef, worldRef, {
    onTap,
    onMenu,
    onHandle,
    onView: ({ k }) => {
      if (zoomLabel.current) zoomLabel.current.textContent = `${Math.round(k * 100)}%`
    },
    onInteract: () => {
      autoFit.current = false
    },
  })
  const { fitRect, rectOf, zoomBy, setView, view } = vp

  const fitElement = useCallback((el: HTMLElement | null, animate = true): void => {
    const r = el ? rectOf(el) : null
    if (r) fitRect({ x: r.x - 40, y: r.y - 40, w: r.w + 80, h: r.h + 80 }, FIT_INSET, 1, animate)
  }, [fitRect, rectOf])
  /** Union of every group (moved groups included — they don't size the content box). */
  const fitAll = useCallback(() => {
    const groups = contentRef.current?.querySelectorAll<HTMLElement>('[data-group]') ?? []
    let u: { x: number; y: number; r: number; b: number } | null = null
    for (const g of Array.from(groups)) {
      const r = rectOf(g)
      if (!r) continue
      u = u
        ? { x: Math.min(u.x, r.x), y: Math.min(u.y, r.y), r: Math.max(u.r, r.x + r.w), b: Math.max(u.b, r.y + r.h) }
        : { x: r.x, y: r.y, r: r.x + r.w, b: r.y + r.h }
    }
    if (u) fitRect({ x: u.x - 40, y: u.y - 40, w: u.r - u.x + 80, h: u.b - u.y + 80 }, FIT_INSET, 1, true)
  }, [fitRect, rectOf])
  /** The group with the newest work: a running message's card, else the newest file. */
  const latestSel = useMemo((): string | null => {
    const running = [...pendingRuns].reverse().find((r) => !isTerminalRunStatus(statuses[r.id] ?? r.status))
    if (running) return `[data-pending="${CSS.escape(running.id)}"]`
    const newest = files.reduce<CanvasFile | null>((a, f) => (a == null || f.created_at > a.created_at ? f : a), null)
    return newest ? selectorOf(`c:${newest.path}`) : null
  }, [pendingRuns, statuses, files])
  const fitLatest = useCallback((animate = true) => {
    const root = contentRef.current
    const el = latestSel ? root?.querySelector<HTMLElement>(latestSel)?.closest<HTMLElement>('[data-group]') : null
    const all = root?.querySelectorAll<HTMLElement>('[data-group]')
    fitElement(el ?? (all && all.length > 0 ? all[all.length - 1] : null), animate)
  }, [fitElement, latestSel])

  // Content grows as frames measure themselves: keep the latest group in view
  // until the user takes over.
  useEffect(() => {
    const el = contentRef.current
    if (!el) return
    let first = true
    let raf = 0
    // frames report sizes in bursts: refit at most once per frame
    const ro = new ResizeObserver(() => {
      if (raf || !autoFit.current) return
      raf = requestAnimationFrame(() => {
        raf = 0
        if (!autoFit.current) return
        fitLatest(!first)
        first = false
      })
    })
    ro.observe(el)
    // groups too: in the free layout they don't size the content box
    for (const g of Array.from(el.querySelectorAll('[data-group]'))) ro.observe(g)
    return () => {
      ro.disconnect()
      cancelAnimationFrame(raf)
    }
  }, [fitLatest, groups])
  useEffect(() => {
    autoFit.current = true
  }, [threadId])

  // The canvas owns the whole screen: no rubber-band / pull-to-refresh.
  useEffect(() => {
    const root = document.documentElement
    const prev = root.style.overscrollBehavior
    root.style.overscrollBehavior = 'none'
    return () => {
      root.style.overscrollBehavior = prev
    }
  }, [])

  /* ---------- misc state ---------- */

  const [detailsRunId, setDetailsRunId] = useState<string | null>(null)
  const [previewPath, setPreviewPath] = useState<string | null>(null)
  const [notice, setNotice] = useState<{ text: string; undo?: () => void } | null>(null)
  useEffect(() => {
    if (!notice) return
    const t = window.setTimeout(() => setNotice(null), notice.undo ? 6000 : 2400)
    return () => window.clearTimeout(t)
  }, [notice])

  /* ---------- sending ---------- */

  const [text, setText] = useState('')
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState<string | null>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)

  /** Sends a message about `paths` (files / folders of the canvas). */
  const send = async (message: string, paths: string[]): Promise<void> => {
    const brief = message.trim()
    if (!brief || sending) return
    const contextPaths = threadId ? [...new Set(paths)] : []
    const count = expand(contextPaths.map((p) => (fileByPath.has(p) ? `c:${p}` : `g:${p}`))).length
    if (count > MAX_CONTEXT_FILES) {
      setSendError(`A message can be about at most ${MAX_CONTEXT_FILES} components (${count} selected).`)
      return
    }
    setSending(true)
    setSendError(null)
    try {
      const res = await startRun({
        input: { [field]: brief },
        ...(threadId ? { thread_id: threadId } : {}),
        ...(contextPaths.length > 0 ? { context_paths: contextPaths } : {}),
      })
      setText('')
      setSelection([])
      setSelectMode(false)
      autoFit.current = true
      if (!threadId) navigate(`/chat/${encodeURIComponent(res.thread_id || res.id)}`, { replace: true })
      else void loadRuns(threadId)
      threadsQ.reload()
    } catch (err: unknown) {
      setSendError(isBackendMissing(err) ? "The runs backend can't be reached." : err instanceof Error ? err.message : 'Couldn\'t send the message.')
    } finally {
      setSending(false)
    }
  }
  const sendWithSelection = (message: string): void => void send(message, selection.flatMap(pathsOf))
  const retryRef = useRef<(run: Run) => void>(() => {})
  retryRef.current = (run) => void send(briefOf(run.input, field), (run.context_paths ?? []).filter((p) => fileByPath.has(p)))
  // stable, so the memoized groups don't re-render on every keystroke
  const retry = useCallback((run: Run) => retryRef.current(run), [])

  /* ---------- layout actions ---------- */

  /**
   * Offsets that line the given same-level elements up in a row / column,
   * in their current visual order, starting at their top-left-most corner.
   */
  const lineUp = (keys: string[], dir: GroupDir, gap: number, base: Record<string, Point> = layout.pos): Record<string, Point> => {
    const world = worldRef.current
    const items = keys.flatMap((k) => {
      const el = world?.querySelector<HTMLElement>(selectorOf(k))
      const r = el ? rectOf(el) : null
      return r ? [{ k, r, off: base[k] ?? { x: 0, y: 0 } }] : []
    })
    if (items.length < 2) return {}
    items.sort((a, b) => (dir === 'col' ? a.r.y - b.r.y || a.r.x - b.r.x : a.r.x - b.r.x || a.r.y - b.r.y))
    const x0 = Math.min(...items.map((i) => i.r.x))
    const y0 = Math.min(...items.map((i) => i.r.y))
    let cur = dir === 'col' ? y0 : x0
    const out: Record<string, Point> = {}
    for (const { k, r, off } of items) {
      const tx = dir === 'col' ? x0 : cur
      const ty = dir === 'col' ? cur : y0
      out[k] = { x: off.x + tx - r.x, y: off.y + ty - r.y }
      cur += (dir === 'col' ? r.h : r.w) + gap
    }
    return out
  }

  /** The flow items of a folder: its files, then its subfolders. */
  const itemsOf = (folder: string): string[] => {
    const n = folders.get(folder)
    if (!n) return []
    const subs = folder === '/' ? [] : n.folders.map((f) => `g:${f.path}`)
    return [...n.files.map((f) => `c:${f.path}`), ...subs]
  }
  /** Selected items (components, nested groups) by the folder they flow in. */
  const itemsByFolder = (keys: string[]): Map<string, string[]> => {
    const m = new Map<string, string[]>()
    for (const k of keys) {
      if (topKeys.has(k)) continue
      const parent = dirOf(k.slice(2))
      m.set(parent, [...(m.get(parent) ?? []), k])
    }
    return m
  }

  const groupKeys = selection.filter((k) => k.startsWith('g:'))
  const topSelected = selection.filter((k) => topKeys.has(k))
  /** Arranging means something: several groups, a group of 2+ items, or 2+ items of one folder. */
  const canArrange = topSelected.length > 1
    || (groupKeys.length === 1 && itemsOf(groupKeys[0].slice(2)).length > 1)
    || [...itemsByFolder(selection)].some(([folder, ks]) => !selected.has(`g:${folder}`) && ks.length > 1)

  /**
   * One group → its items flow as a row / column. Several canvas groups → the
   * groups line up. Several items of one folder → they line up inside it (all
   * of them → the folder's flow changes instead).
   */
  const arrange = (dir: GroupDir): void => {
    autoFit.current = false
    const next: Record<string, Point | null> = {}
    const flow = (folder: string): void => {
      setDir(folder, dir)
      for (const k of itemsOf(folder)) next[k] = null
    }
    if (groupKeys.length === 1) {
      if (itemsOf(groupKeys[0].slice(2)).length > 1) flow(groupKeys[0].slice(2))
    } else if (topSelected.length > 1) {
      const base = free ? layout.pos : groupPlaces()
      Object.assign(next, free ? {} : base, lineUp(topSelected, dir, ARRANGE_GAP.group, base))
    }
    for (const [folder, keys] of itemsByFolder(selection)) {
      if (selected.has(`g:${folder}`) || keys.length < 2) continue
      if (keys.length === itemsOf(folder).length) flow(folder)
      else Object.assign(next, lineUp(keys, dir, ARRANGE_GAP[dir]))
    }
    setPos(next)
  }

  /**
   * Selection back to the automatic layout: a component's offset and scale; a
   * group's flow and its items (its own place stays — "Reset layout" on the
   * empty canvas restacks everything).
   */
  const resetSelectionLayout = (): void => {
    const next: Record<string, Point | null> = {}
    for (const k of selection) {
      if (k.startsWith('c:')) {
        next[k] = null
        setScale(k.slice(2), null)
      } else {
        setDir(k.slice(2), null)
        for (const item of itemsOf(k.slice(2))) {
          next[item] = null
          if (item.startsWith('c:')) setScale(item.slice(2), null)
        }
      }
    }
    setPos(next)
  }
  const layoutEmpty = !hasArrangement(layout)

  /* ---------- file operations (server side; the next message is told about them) ---------- */

  const clearSelection = (): void => {
    setSelection([])
    setSelectMode(false)
  }
  /** Runs a canvas operation with the layout saved first and the tree reloaded after. */
  const operate = async (fn: (thread: string) => Promise<void>, failure: string): Promise<void> => {
    if (!threadId) return
    try {
      await flush()
      await fn(threadId)
    } catch (err: unknown) {
      setNotice({ text: errorText(err, failure) })
    } finally {
      await loadCanvas(threadId)
    }
  }

  /** Deletes the selection from the canvas (and from every future context); undoable for a moment. */
  const deleteSelection = (): void => {
    const keys = [...selection]
    const paths = keys.flatMap(pathsOf)
    if (paths.length === 0) return
    clearSelection()
    void operate(async (thread) => {
      const { op_id } = await deleteCanvasPaths(thread, paths)
      setNotice({
        text: keys.length === 1 ? (keys[0].startsWith('g:') ? 'Group deleted' : 'Component deleted') : `${keys.length} deleted`,
        undo: () => void operate(async (t) => { await restoreCanvasOp(t, op_id) }, "Couldn't restore"),
      })
    }, "Couldn't delete")
  }

  /** Renames the selected folder (a move next to itself). */
  const renameFolder = (): void => {
    const key = selection[0]
    if (selection.length !== 1 || !key?.startsWith('g:') || key === 'g:/') return
    const folder = key.slice(2)
    const name = window.prompt('Rename folder', baseOf(folder))?.trim()
    if (!name || name === baseOf(folder)) return
    clearSelection()
    void operate(async (thread) => { await moveCanvasPath(thread, folder, joinPath(dirOf(folder), name)) }, "Couldn't rename the folder")
  }

  /** Moves the selected components into another folder (created when new). */
  const moveToFolder = (): void => {
    const paths = selection.filter((k) => k.startsWith('c:')).map((k) => k.slice(2))
    if (paths.length === 0) return
    const target = window.prompt('Move to folder', dirOf(paths[0]))?.trim()
    if (target == null || target === '') return
    const folder = target.startsWith('/') ? target : `/${target}`
    clearSelection()
    void operate(async (thread) => {
      for (const p of paths) if (dirOf(p) !== folder) await moveCanvasPath(thread, p, joinPath(folder, baseOf(p)))
    }, "Couldn't move")
  }

  // The title shrinks to just the back arrow after a moment (hover shows it again).
  const [titleShort, setTitleShort] = useState(false)
  useEffect(() => {
    setTitleShort(false)
    const t = window.setTimeout(() => setTitleShort(true), TITLE_COLLAPSE_MS)
    return () => window.clearTimeout(t)
  }, [threadId])

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      const typing = e.target instanceof HTMLElement && (e.target.tagName === 'TEXTAREA' || e.target.tagName === 'INPUT')
      if (e.key === 'Escape' && !menu && !previewPath && !detailsRunId) {
        setSelection([])
        setSelectMode(false)
      }
      if (typing) return
      if ((e.key === '=' || e.key === '+') && !e.metaKey && !e.ctrlKey) zoomBy(1.25, undefined, undefined, true)
      else if (e.key === '-' && !e.metaKey && !e.ctrlKey) zoomBy(0.8, undefined, undefined, true)
      else if (e.code === 'Digit1' && e.shiftKey) fitAll()
      else return
      autoFit.current = false
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [menu, previewPath, detailsRunId, zoomBy, fitAll])

  /* ---------- context menu ---------- */

  const ids = expand(selection)
  const single = selection.length === 1 && selection[0].startsWith('c:') ? fileByPath.get(selection[0].slice(2)) ?? null : null
  const arrangeActions: MenuAction[] = canArrange
    ? [
        { key: 'col', label: 'Arrange vertically', icon: Rows3, onSelect: () => arrange('col') },
        { key: 'row', label: 'Arrange horizontally', icon: Columns3, onSelect: () => arrange('row') },
      ]
    : []
  const resetAction: MenuAction = { key: 'reset', label: 'Reset position & size', icon: RotateCcw, onSelect: resetSelectionLayout }
  const deleteAction: MenuAction = { key: 'delete', label: 'Delete from canvas', icon: Trash2, onSelect: deleteSelection }
  const fileActions: MenuAction[] = [
    ...(selection.length === 1 && selection[0].startsWith('g:') && selection[0] !== 'g:/'
      ? [{ key: 'rename', label: 'Rename folder…', icon: FolderPen, onSelect: renameFolder }]
      : []),
    ...(selection.length > 0 && selection.every((k) => k.startsWith('c:'))
      ? [{ key: 'move-to', label: 'Move to folder…', icon: FolderInput, onSelect: moveToFolder }]
      : []),
  ]
  const quickActions: MenuAction[] = QUICK_ACTIONS.map((a) => ({
    key: a.key,
    label: a.label,
    icon: a.icon,
    disabled: ids.length === 0 || sending,
    onSelect: () => sendWithSelection(a.prompt(ids.length > 1)),
  }))
  const clearAction: MenuAction = { key: 'clear', label: 'Clear selection', icon: X, onSelect: clearSelection }

  const menuGroups = (): MenuAction[][] => {
    const runId = selection.length === 1 ? runOfKey(selection[0]) : menu?.target ? runOfKey(menu.target) : null
    if (selection.length === 0) {
      return [[
        { key: 'fit', label: 'Fit everything', icon: Maximize, onSelect: () => { autoFit.current = false; fitAll() } },
        { key: 'latest', label: 'Go to latest', icon: Maximize2, disabled: groups.length === 0, onSelect: () => fitLatest() },
        { key: 'write', label: 'New message', icon: PenLine, onSelect: () => inputRef.current?.focus() },
      ], layoutEmpty ? [] : [
        { key: 'reset-all', label: 'Reset layout', icon: RotateCcw, onSelect: resetLayout },
      ]]
    }
    return [
      quickActions,
      [
        { key: 'change', label: 'Describe a change…', icon: PenLine, onSelect: () => inputRef.current?.focus() },
        selectMode
          ? { key: 'done', label: 'Done selecting', icon: ListChecks, onSelect: () => setSelectMode(false) }
          : { key: 'more', label: 'Select more', icon: ListChecks, onSelect: () => setSelectMode(true) },
      ],
      [...arrangeActions, resetAction, ...fileActions, deleteAction],
      [
        ...(single
          ? [
              { key: 'preview', label: 'Open preview', icon: Maximize2, onSelect: () => setPreviewPath(single.path) },
              {
                key: 'copy',
                label: 'Copy HTML',
                icon: Copy,
                onSelect: () => {
                  void loadComponentHtml(single.artifact_id)
                    .then((html) => navigator.clipboard.writeText(html))
                    .then(() => setNotice({ text: 'HTML copied' }), () => setNotice({ text: "Couldn't copy" }))
                },
              },
            ]
          : []),
        ...(runId ? [{ key: 'details', label: 'Run details', icon: ListTree, onSelect: () => setDetailsRunId(runId) }] : []),
        clearAction,
      ],
    ]
  }

  /** The fixed toolbar: layout first, then the canned requests, then single-item actions. */
  const barRunId = selection.length === 1 ? runOfKey(selection[0]) : null
  const barGroups: MenuAction[][] = [
    arrangeActions,
    quickActions,
    [
      ...(single ? [{ key: 'preview', label: 'Open preview', icon: Maximize2, onSelect: () => setPreviewPath(single.path) }] : []),
      ...(barRunId ? [{ key: 'details', label: 'Run details', icon: ListTree, onSelect: () => setDetailsRunId(barRunId) }] : []),
      resetAction,
      ...fileActions,
      deleteAction,
      clearAction,
    ],
  ]

  const menuTitle = selection.length === 0
    ? 'Canvas'
    : selection.length === 1
      ? chips[0]?.label
      : `${selection.length} selected · ${ids.length} components`

  /* ---------- render ---------- */

  const env = useMemo<GroupEnv>(() => ({
    selection: selected, layout, messageOf, onDetails: setDetailsRunId, onRetry: retry, onStatus,
  }), [selected, layout, messageOf, retry, onStatus])

  const empty = !threadId && runs.length === 0

  return (
    <div className="fixed inset-0 overflow-hidden overscroll-none bg-app select-none [-webkit-touch-callout:none]">
      {/* viewport — every gesture lands here */}
      <div
        ref={viewportRef}
        className="absolute inset-0 touch-none bg-[radial-gradient(circle,var(--color-line-strong)_1px,transparent_1.2px)]"
      >
        <div ref={worldRef} className="absolute top-0 left-0 origin-top-left">
          <div ref={contentRef} className={free ? 'relative' : 'flex flex-col items-start gap-40'}>
            {groups.map((g) => (
              <FolderGroup key={g.path} node={g} top free={free} env={env} />
            ))}
          </div>
        </div>
      </div>

      {/* top bar */}
      <div className="pointer-events-none fixed inset-x-0 top-0 z-20 flex items-start justify-between gap-3 px-3 pt-[max(0.75rem,env(safe-area-inset-top))]">
        <div
          className={cx(
            'group/title pointer-events-auto flex min-w-0 items-center rounded-xl border border-line bg-surface/95 p-1 shadow-pop backdrop-blur transition-[padding] duration-300 ease-out',
            titleShort ? 'pointer-fine:hover:pr-3' : 'pr-3',
          )}
        >
          <IconButton icon={<ArrowLeft />} label="All chats" onClick={() => navigate('/chat')} />
          <p
            className={cx(
              'min-w-0 truncate text-sm font-semibold text-ink transition-[max-width,opacity,margin] duration-300 ease-out',
              titleShort
                ? 'ml-0 max-w-0 opacity-0 pointer-fine:group-hover/title:ml-1 pointer-fine:group-hover/title:max-w-[60vw] pointer-fine:group-hover/title:opacity-100'
                : 'ml-1 max-w-[60vw] opacity-100',
            )}
          >
            {title}
          </p>
        </div>
        {SHOW_ZOOM_CONTROLS && (
        <div className="pointer-events-auto flex shrink-0 items-center gap-0.5 rounded-xl border border-line bg-surface/95 p-1 shadow-pop backdrop-blur">
          <IconButton icon={<Minus />} label="Zoom out" onClick={() => { autoFit.current = false; zoomBy(0.8, undefined, undefined, true) }} />
          <button
            ref={zoomLabel}
            type="button"
            onClick={() => {
              autoFit.current = false
              const vpEl = viewportRef.current
              if (!vpEl) return
              // back to 100% around the screen center
              const f = 1 / view.current.k
              const cx = vpEl.clientWidth / 2
              const cy = vpEl.clientHeight / 2
              setView({ k: 1, x: cx - (cx - view.current.x) * f, y: cy - (cy - view.current.y) * f }, true)
            }}
            title="Reset to 100%"
            className="h-8 w-12 cursor-pointer rounded-md text-xs font-medium text-ink-2 tabular-nums outline-none hover:bg-app focus-visible:ring-2 focus-visible:ring-accent/40"
          >
            100%
          </button>
          <IconButton icon={<Plus />} label="Zoom in" onClick={() => { autoFit.current = false; zoomBy(1.25, undefined, undefined, true) }} />
          <IconButton icon={<Maximize />} label="Fit everything" onClick={() => { autoFit.current = false; fitAll() }} />
        </div>
        )}
      </div>

      {selection.length > 0 && (
        <SelectionBar
          count={selection.length}
          groups={barGroups}
          onMore={(r) => setMenu({ target: null, clientX: r.left > 280 ? r.left - 264 : r.right + 8, clientY: r.top })}
        />
      )}

      {/* empty / error states (screen space) */}
      {empty && (
        <div className="pointer-events-none fixed inset-0 z-10 flex items-center justify-center px-4 pb-32">
          <div className="pointer-events-auto w-full max-w-md rounded-2xl border border-line bg-surface p-5 shadow-pop">
            <p className="text-sm font-semibold text-ink">Describe what you want designed</p>
            <p className="mt-1 text-xs leading-relaxed text-ink-2">
              The agents' designs land on the canvas as component files, grouped in folders.
              Tap a component to select it, long-press for actions, pinch to zoom.
            </p>
            <div className="mt-3 flex flex-col gap-1.5">
              {EXAMPLES.map((ex) => (
                <button
                  key={ex}
                  type="button"
                  onClick={() => {
                    setText(ex)
                    inputRef.current?.focus()
                  }}
                  className="cursor-pointer rounded-lg border border-line bg-app px-3 py-2.5 text-left text-sm text-ink-2 outline-none transition duration-100 hover:border-line-strong hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/30"
                >
                  {ex}
                </button>
              ))}
            </div>
          </div>
        </div>
      )}
      {threadId && canvasError != null && runsError == null && (
        <div className="fixed inset-x-0 top-20 z-10 mx-auto w-full max-w-md px-4">
          <ErrorState error={canvasError} onRetry={() => void loadCanvas(threadId)} />
        </div>
      )}
      {threadId && runsError != null && !runsLoading && (
        <div className="fixed inset-x-0 top-20 z-10 mx-auto w-full max-w-md px-4">
          {isBackendMissing(runsError) ? (
            <Callout tone="warning" title="The runs backend can't be reached">
              This chat lives in the runs API, which isn't responding right now.
            </Callout>
          ) : (
            <ErrorState error={runsError} onRetry={() => void loadRuns(threadId)} />
          )}
        </div>
      )}
      {notice && (
        <div className="fixed inset-x-0 top-20 z-40 flex justify-center">
          <span className="flex animate-pop items-center gap-3 rounded-full bg-ink py-1.5 pr-1.5 pl-3 text-xs text-white shadow-pop">
            {notice.text}
            {notice.undo ? (
              <button
                type="button"
                onClick={() => {
                  notice.undo?.()
                  setNotice(null)
                }}
                className="cursor-pointer rounded-full bg-white/15 px-2.5 py-0.5 font-medium outline-none hover:bg-white/25 focus-visible:ring-2 focus-visible:ring-white/50"
              >
                Undo
              </button>
            ) : (
              <span className="w-1.5" />
            )}
          </span>
        </div>
      )}

      <Composer
        value={text}
        onChange={setText}
        onSend={() => sendWithSelection(text)}
        sending={sending}
        error={sendError}
        chips={chips}
        onRemoveChip={(k) => setSelection((prev) => prev.filter((x) => x !== k))}
        onClearChips={() => setSelection([])}
        selectMode={selectMode}
        onDoneSelecting={() => setSelectMode(false)}
        inputRef={inputRef}
        placeholder={selection.length > 0 ? 'What should change in the selection?' : groups.length > 0 ? 'Ask for changes or something new…' : 'Describe what you want designed…'}
      />

      {menu && (
        <ContextMenu
          x={menu.clientX}
          y={menu.clientY}
          title={menuTitle}
          groups={menuGroups()}
          onClose={() => setMenu(null)}
        />
      )}

      <PreviewModal
        id={previewPath ? fileByPath.get(previewPath)?.artifact_id ?? null : null}
        label={previewPath ? baseOf(previewPath) : ''}
        onClose={() => setPreviewPath(null)}
      />

      {detailsRunId && (
        <Drawer onClose={() => setDetailsRunId(null)}>
          <RunDetailView runId={detailsRunId} />
        </Drawer>
      )}
    </div>
  )
}

/** Full-size, interactive preview of one component. */
function PreviewModal({ id, label, onClose }: { id: string | null; label: string; onClose: () => void }) {
  const { html } = useComponentHtml(id ?? '')
  return (
    <Modal
      open={id != null}
      onClose={onClose}
      size="xl"
      title={label}
      footer={
        <Button variant="secondary" onClick={onClose}>
          Close
        </Button>
      }
    >
      {id && html != null ? (
        <iframe title={label} srcDoc={html} sandbox="allow-scripts" className="h-[70vh] w-full rounded-lg border border-line bg-white" />
      ) : (
        <div className="h-[70vh] w-full animate-pulse rounded-lg bg-app" />
      )}
    </Modal>
  )
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
      <button type="button" aria-label="Close details" onClick={onClose} className="fixed inset-0 z-40 cursor-default bg-black/20" />
      <aside className="fixed inset-y-0 right-0 z-50 flex w-[min(960px,100vw)] flex-col border-l border-line bg-page shadow-xl select-text">
        <div className="flex items-center justify-end border-b border-line bg-surface px-2 py-1.5">
          <IconButton icon={<X />} label="Close details" size="sm" onClick={onClose} />
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
      </aside>
    </>
  )
}
