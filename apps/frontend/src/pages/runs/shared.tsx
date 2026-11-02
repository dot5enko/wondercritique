/**
 * Shared helpers + small widgets for the Runs pages (plan §5 Runs UI):
 * status → badge mapping, token/cost/duration/score formatting, node labels,
 * critique heuristics, and the mono/copy/stat primitives used across panels.
 */
import { useState, type ReactNode } from 'react'
import { Check, Copy } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { Box, Bot, Code, Filter, LogIn, LogOut, Merge, ShieldCheck } from 'lucide-react'
import { cx } from '../../ui/cx'
import { Badge, type BadgeTone } from '../../ui/Display'
import type { PipelineNode } from '../../api/studio'
import type { RunStatus } from '../../api/runs'

/* ---------- status ---------- */

export function runStatusTone(status: RunStatus): { tone: BadgeTone; pulse: boolean } {
  switch (status) {
    case 'running':
      return { tone: 'accent', pulse: true }
    case 'completed':
      return { tone: 'success', pulse: false }
    case 'failed':
      return { tone: 'danger', pulse: false }
    case 'cancelled':
    case 'interrupted':
      return { tone: 'warning', pulse: false }
    default:
      return { tone: 'neutral', pulse: false }
  }
}

/** Status badge with the shared run vocabulary (running pulses). */
export function RunStatusBadge({ status, className }: { status: RunStatus; className?: string }) {
  const { tone, pulse } = runStatusTone(status)
  return (
    <Badge tone={tone} dot className={cx(pulse && 'animate-pulse', className)}>
      {status}
    </Badge>
  )
}

/* ---------- formatting ---------- */

/** First 8 chars of an id, for compact display. */
export function shortId(id: string | null | undefined): string {
  return id ? id.slice(0, 8) : '—'
}

function trim1(n: number): string {
  return String(Math.round(n * 10) / 10)
}

/** 950 → "950", 12_300 → "12.3k", 4_500_000 → "4.5m". */
export function formatTokens(n: number | null | undefined): string {
  if (n == null || !Number.isFinite(n)) return '—'
  if (n < 1000) return String(Math.round(n))
  if (n < 1_000_000) return `${trim1(n / 1000)}k`
  return `${trim1(n / 1_000_000)}m`
}

/** `$12.34` / `$0.123` / `$0.0041`; null → em dash. */
export function formatCost(cost: number | null | undefined): string {
  if (cost == null || !Number.isFinite(cost)) return '—'
  if (cost < 0.01) return `$${cost.toFixed(4)}`
  if (cost < 1) return `$${cost.toFixed(3)}`
  return `$${cost.toFixed(2)}`
}

/** Critique scores run 0–10; from here up a design counts as good (the engine gate's default target). */
export const GOOD_SCORE = 8

/** `0.87` / `42` / `—`. */
export function formatScore(score: number | null | undefined): string {
  if (score == null || !Number.isFinite(score)) return '—'
  return Number.isInteger(score) ? String(score) : score.toFixed(2)
}

/** Wall-clock step duration, e.g. "0.3s" / "12s" / "1m 04s". */
export function formatDuration(startedAt?: string | null, completedAt?: string | null): string {
  if (!startedAt || !completedAt) return '—'
  const ms = new Date(completedAt).getTime() - new Date(startedAt).getTime()
  if (!Number.isFinite(ms) || ms < 0) return '—'
  if (ms < 1000) return `${(ms / 1000).toFixed(1)}s`
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`
}

/* ---------- pipeline nodes ---------- */

const NODE_TYPE_ICONS: Record<string, LucideIcon> = {
  input: LogIn,
  agent: Bot,
  validate: ShieldCheck,
  render: Code,
  merge: Merge,
  gate: Filter,
  output: LogOut,
}

/** Icon for a pipeline node type (falls back to a neutral box). */
export function nodeIcon(nodeType: string): LucideIcon {
  return NODE_TYPE_ICONS[nodeType] ?? Box
}

/** Display label for a node: config label/name when present, else the node id. */
export function nodeLabel(node: PipelineNode): string {
  const label = node.config?.['label']
  if (typeof label === 'string' && label.trim()) return label.trim()
  const name = node.config?.['name']
  if (typeof name === 'string' && name.trim()) return name.trim()
  return node.id
}

/* ---------- critiques ---------- */

const CRITIQUE_KEYS = ['score', 'verdict', 'critique', 'feedback', 'issues', 'suggestions', 'strengths', 'weaknesses']

/** Heuristic: does a step output look like a critique (agent `critique` contract)? */
export function looksLikeCritique(output: unknown): boolean {
  if (output == null || typeof output !== 'object') return false
  const o = output as Record<string, unknown>
  return CRITIQUE_KEYS.some((k) => k in o)
}

/** Pretty text for a JSON-ish value; strings pass through untouched. */
export function jsonText(value: unknown): string {
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value, null, 2) ?? ''
  } catch {
    return String(value)
  }
}

/** Extracts a numeric score from a critique-shaped object, if any. */
export function scoreOf(output: unknown): number | null {
  if (output == null || typeof output !== 'object') return null
  const s = (output as Record<string, unknown>)['score']
  return typeof s === 'number' && Number.isFinite(s) ? s : null
}

/* ---------- small widgets ---------- */

/** Monospace pre block; long content is collapsed behind a "Show all" toggle. */
export function MonoBlock({ text, className }: { text: string; className?: string }) {
  const [expanded, setExpanded] = useState(false)
  const long = text.length > 700 || text.split('\n').length > 10
  return (
    <div className={className}>
      <pre
        className="overflow-auto whitespace-pre-wrap break-words rounded-md bg-app p-2.5 font-mono text-xs leading-relaxed text-ink-2"
        style={long && !expanded ? { maxHeight: '10rem' } : undefined}
      >
        {text}
      </pre>
      {long && (
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          className="mt-1 cursor-pointer text-2xs font-medium text-accent-ink transition-colors duration-100 hover:text-accent"
        >
          {expanded ? 'Show less' : 'Show all'}
        </button>
      )}
    </div>
  )
}

/** Muted stat chip for the header (tokens, cost, iterations). */
export function StatChip({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span
      title={title}
      className="inline-flex items-center gap-1 rounded-md border border-line bg-surface px-1.5 py-0.5 text-2xs text-ink-2"
    >
      {children}
    </span>
  )
}

/** Copyable chip for opaque ids (agent sessions). Click copies, icon flips to a check. */
export function CopyChip({ value, label = 'session' }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    void navigator.clipboard?.writeText(value).then(
      () => {
        setCopied(true)
        window.setTimeout(() => setCopied(false), 1500)
      },
      () => undefined,
    )
  }
  return (
    <button
      type="button"
      onClick={copy}
      title={`Copy ${label} ${value}`}
      className="inline-flex cursor-pointer items-center gap-1 rounded-md border border-line bg-surface px-1.5 py-0.5 font-mono text-2xs text-ink-2 outline-none transition-colors duration-100 hover:border-line-strong hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/40"
    >
      {label} {shortId(value)}
      {copied ? <Check size={11} className="text-success" /> : <Copy size={11} className="text-ink-3" />}
    </button>
  )
}
