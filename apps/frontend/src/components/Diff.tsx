/**
 * Dependency-free diff for two values (JSON pretty-printed or raw text).
 * Line-level LCS (O(n²) DP, capped at ~2000 lines per side with a
 * "diff too large" fallback), trimmed to 3 context lines around changes.
 *
 * `diffJson(a, b)` / `diffText(a, b)` return hunks (or `null` when too large);
 * `<DiffView hunks={...} />` renders them in a unified, tinted view.
 */
import { cx } from '../ui/cx'

export type DiffLineType = 'same' | 'add' | 'del'

export interface DiffLine {
  type: DiffLineType
  text: string
  /** 1-based line number in the old text (del/same), else null. */
  a: number | null
  /** 1-based line number in the new text (add/same), else null. */
  b: number | null
}

export interface DiffHunk {
  /** 1-based old-text start line. */
  aStart: number
  /** 1-based new-text start line. */
  bStart: number
  lines: DiffLine[]
}

/** Both sides must stay under this many lines, else diff*() returns null. */
const MAX_LINES = 2000
const CONTEXT = 3

function stringify(v: unknown): string {
  if (v === undefined) return ''
  if (typeof v === 'string') return v
  try {
    return JSON.stringify(v, null, 2) ?? ''
  } catch {
    return String(v)
  }
}

/** Classic LCS diff over two line arrays; marks 1-based line numbers. */
function diffLines(a: string[], b: string[]): DiffLine[] {
  const n = a.length
  const m = b.length
  const width = m + 1
  const dp = new Int32Array((n + 1) * width)
  const at = (i: number, j: number) => i * width + j
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[at(i, j)] = a[i] === b[j] ? dp[at(i + 1, j + 1)] + 1 : Math.max(dp[at(i + 1, j)], dp[at(i, j + 1)])
    }
  }
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  let lnA = 0
  let lnB = 0
  const del = (text: string) => out.push({ type: 'del', text, a: ++lnA, b: null })
  const add = (text: string) => out.push({ type: 'add', text, a: null, b: ++lnB })
  const same = (text: string) => out.push({ type: 'same', text, a: ++lnA, b: ++lnB })
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      same(a[i])
      i++
      j++
    } else if (dp[at(i + 1, j)] >= dp[at(i, j + 1)]) {
      del(a[i])
      i++
    } else {
      add(b[j])
      j++
    }
  }
  while (i < n) del(a[i++])
  while (j < m) add(b[j++])
  return out
}

/** Groups the aligned lines into unified hunks with ≤3 context lines. */
function toHunks(lines: DiffLine[]): DiffHunk[] {
  const changed: number[] = []
  for (let i = 0; i < lines.length; i++) if (lines[i].type !== 'same') changed.push(i)
  if (changed.length === 0) return []

  const hunks: DiffHunk[] = []
  let start = changed[0]
  let end = changed[0]
  const flush = (from: number, to: number) => {
    const lo = Math.max(0, from - CONTEXT)
    const hi = Math.min(lines.length, to + CONTEXT + 1)
    const slice = lines.slice(lo, hi)
    const first = slice[0]
    hunks.push({
      aStart: first.a ?? first.b ?? 1,
      bStart: first.b ?? first.a ?? 1,
      lines: slice,
    })
  }
  for (let k = 1; k < changed.length; k++) {
    if (changed[k] - end <= CONTEXT * 2) {
      end = changed[k]
    } else {
      flush(start, end)
      start = changed[k]
      end = changed[k]
    }
  }
  flush(start, end)
  return hunks
}

function hunkHeader(h: DiffHunk): string {
  const aCount = h.lines.filter((l) => l.type !== 'add').length
  const bCount = h.lines.filter((l) => l.type !== 'del').length
  return `@@ -${h.aStart},${aCount} +${h.bStart},${bCount} @@`
}

/** Diffs two arbitrary values as pretty-printed JSON. */
export function diffJson(a: unknown, b: unknown): DiffHunk[] | null {
  return diffText(stringify(a), stringify(b))
}

/** Diffs two strings line by line. Returns null when either side exceeds the cap. */
export function diffText(a: string, b: string): DiffHunk[] | null {
  const la = a.split('\n')
  const lb = b.split('\n')
  if (la.length > MAX_LINES || lb.length > MAX_LINES) return null
  return toHunks(diffLines(la, lb))
}

/* ---------- renderer ---------- */

const LINE_STYLES: Record<DiffLineType, string> = {
  same: '',
  add: 'bg-success-soft',
  del: 'bg-danger-soft',
}

const SIGN: Record<DiffLineType, string> = { same: ' ', add: '+', del: '-' }

const SIGN_STYLES: Record<DiffLineType, string> = {
  same: 'text-ink-3',
  add: 'text-success',
  del: 'text-danger',
}

export interface DiffViewProps {
  hunks: DiffHunk[] | null
  className?: string
  /** Shown when `hunks` is an empty array (no changes). */
  emptyMessage?: string
  /** Shown when `hunks` is null (too large). */
  tooLargeMessage?: string
}

export function DiffView({
  hunks,
  className,
  emptyMessage = 'No changes — identical.',
  tooLargeMessage = 'Diff too large to display.',
}: DiffViewProps) {
  if (hunks === null) {
    return (
      <div className={cx('rounded-lg border border-line bg-app px-3 py-2.5 text-xs text-ink-2', className)}>
        {tooLargeMessage}
      </div>
    )
  }
  if (hunks.length === 0) {
    return (
      <div className={cx('rounded-lg border border-line bg-app px-3 py-2.5 text-xs text-ink-2', className)}>
        {emptyMessage}
      </div>
    )
  }
  return (
    <div className={cx('overflow-hidden rounded-lg border border-line bg-surface font-mono text-xs leading-[1.6]', className)}>
      {hunks.map((h, hi) => (
        <div key={hi}>
          <div className="border-b border-line bg-app px-2 py-1 text-2xs text-ink-3 select-none">
            {hunkHeader(h)}
          </div>
          {h.lines.map((l, li) => (
            <div key={li} className={cx('flex min-w-0', LINE_STYLES[l.type])}>
              <span className="w-9 shrink-0 select-none pr-1 text-right text-ink-3/60">
                {l.a ?? ''}
              </span>
              <span className="w-9 shrink-0 select-none pr-1 text-right text-ink-3/60">
                {l.b ?? ''}
              </span>
              <span className={cx('w-4 shrink-0 select-none text-center font-medium', SIGN_STYLES[l.type])}>
                {SIGN[l.type]}
              </span>
              <span
                className={cx(
                  'min-w-0 flex-1 whitespace-pre-wrap break-words pr-2',
                  l.type === 'same' ? 'text-ink-2' : 'text-ink',
                )}
              >
                {l.text || ' '}
              </span>
            </div>
          ))}
        </div>
      ))}
    </div>
  )
}
