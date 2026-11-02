/**
 * DesignStage — the hero of a design-loop run. The rendered design of the
 * selected iteration is shown large; prev/next (buttons, ←/→) step through
 * iterations, a filmstrip shows every iteration at a glance with its score,
 * and a one-line "what changed" strip says why the design moved on
 * (score delta + the critic's top asks). Critiques and step transcripts are
 * tucked behind a single toggle.
 */
import { useEffect, useMemo, useState } from 'react'
import { ArrowRight, ChevronLeft, ChevronRight, ChevronDown, Maximize2, Sparkles } from 'lucide-react'
import { cx } from '../../ui'
import { Badge, Skeleton } from '../../ui/Display'
import { Segmented } from '../../ui/Form'
import { Modal } from '../../components/Modal'
import type { Artifact, Run } from '../../api/runs'
import { GOOD_SCORE, formatScore } from '../runs/shared'
import { ArtifactCritiqueCard, useArtifactContent } from '../runs/ArtifactViews'
import { StepsAccordion } from '../runs/StepsAccordion'

/* ---------- per-iteration data ---------- */

interface IterationData {
  n: number
  score: number | null
  shots: Artifact[]
  html: Artifact | null
  critiques: Artifact[]
  /** Critique that represents the iteration (merged one when present). */
  mainCritique: Artifact | null
  running: boolean
}

/** Short human lines out of a critique JSON (issues first, else free text). */
function critiquePoints(text: string | null | undefined, max = 3): string[] {
  if (!text) return []
  let obj: unknown
  try {
    obj = JSON.parse(text)
  } catch {
    return []
  }
  if (obj == null || typeof obj !== 'object') return []
  const o = obj as Record<string, unknown>
  const out: string[] = []
  const issues = o['issues']
  if (Array.isArray(issues)) {
    for (const it of issues) {
      if (typeof it === 'string') out.push(it)
      else if (it && typeof it === 'object') {
        const m = (it as Record<string, unknown>)['message']
        if (typeof m === 'string') out.push(m)
      }
    }
  }
  if (out.length === 0) {
    for (const k of ['feedback', 'critique', 'verdict', 'summary']) {
      const v = o[k]
      if (typeof v === 'string' && v.trim()) {
        out.push(v.trim())
        break
      }
    }
  }
  return out.map((s) => s.replace(/\s+/g, ' ').trim()).filter(Boolean).slice(0, max)
}

/** "What changed" strip between the previous and the selected iteration. */
function ChangeNote({
  n,
  score,
  prevScore,
  basis,
}: {
  n: number
  score: number | null
  prevScore: number | null
  /** Critique artifact that drove this iteration (previous one; own for the first). */
  basis: Artifact | null
}) {
  const { content } = useArtifactContent(basis?.id)
  const points = useMemo(() => critiquePoints(content?.text), [content?.text])
  const delta = score != null && prevScore != null ? score - prevScore : null
  return (
    <div className="flex flex-wrap items-start gap-x-3 gap-y-1 text-xs">
      {n === 1 ? (
        <span className="inline-flex items-center gap-1.5 font-medium text-ink-2">
          <Sparkles size={13} className="text-accent" /> First draft
          {score != null && <Badge tone={score >= GOOD_SCORE ? 'success' : 'neutral'}>{formatScore(score)}</Badge>}
        </span>
      ) : (
        <span className="inline-flex items-center gap-1.5 font-medium text-ink-2">
          {prevScore != null ? formatScore(prevScore) : '—'}
          <ArrowRight size={12} className="text-ink-3" />
          <span className={score != null && score >= GOOD_SCORE ? 'text-success' : undefined}>{formatScore(score)}</span>
          {delta != null && delta !== 0 && (
            <span className={cx('tabular-nums', delta > 0 ? 'text-success' : 'text-danger')}>
              {delta > 0 ? '+' : ''}
              {formatScore(Number(delta.toFixed(2)))}
            </span>
          )}
        </span>
      )}
      {points.length > 0 && (
        <span className="min-w-0 flex-1 basis-64 text-ink-2">
          <span className="text-ink-3">{n === 1 ? 'Critic notes: ' : 'Asked to fix: '}</span>
          {points.join(' · ')}
        </span>
      )}
    </div>
  )
}

/* ---------- visual pieces ---------- */

function Render({ artifact, className }: { artifact: Artifact; className?: string }) {
  const { content, loading, error } = useArtifactContent(artifact.id)
  if (loading && !content) return <Skeleton className={cx('h-96 w-full rounded-none', className)} />
  if (error != null || !content) return <p className="p-6 text-center text-xs text-danger">Couldn't load this render.</p>
  if (content.kind === 'image') {
    return <img src={content.url} alt="Rendered design" draggable={false} className={cx('block w-full bg-white', className)} />
  }
  if (content.text != null) {
    return <iframe srcDoc={content.text} sandbox="" title="Design preview" className={cx('h-[560px] w-full bg-white', className)} />
  }
  return <p className="p-6 text-center text-xs text-ink-3">Nothing to preview.</p>
}

function FilmThumb({
  data,
  selected,
  best,
  onClick,
}: {
  data: IterationData
  selected: boolean
  best: boolean
  onClick: () => void
}) {
  const art = data.shots[0] ?? null
  const { content } = useArtifactContent(art?.id)
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={`Iteration ${data.n}`}
      aria-current={selected}
      className={cx(
        'group relative w-28 shrink-0 cursor-pointer overflow-hidden rounded-md text-left outline-none ring-2 transition duration-100 focus-visible:ring-accent/40',
        selected ? 'ring-accent' : 'opacity-70 ring-transparent hover:opacity-100',
      )}
    >
      <div className="aspect-[4/3] w-full overflow-hidden bg-white">
        {content?.kind === 'image' ? (
          <img src={content.url} alt="" draggable={false} className="h-full w-full object-cover object-top" />
        ) : data.running ? (
          <Skeleton className="h-full w-full rounded-none" />
        ) : (
          <span className="grid h-full place-items-center text-2xs text-ink-3">no render</span>
        )}
      </div>
      <div className="flex items-center justify-between gap-1 bg-app px-1.5 py-0.5 text-2xs">
        <span className="font-medium text-ink-2">#{data.n}</span>
        <span className="flex items-center gap-1">
          {best && <span className="text-accent-ink">best</span>}
          {data.score != null && (
            <span className={cx('tabular-nums font-medium', data.score >= GOOD_SCORE ? 'text-success' : 'text-ink-2')}>
              {formatScore(data.score)}
            </span>
          )}
        </span>
      </div>
    </button>
  )
}

/* ---------- multi-component result ---------- */

function ResultTile({ artifactId, index, score }: { artifactId: string; index: number; score: number | null }) {
  const { content, loading } = useArtifactContent(artifactId)
  return (
    <div className="overflow-hidden rounded-lg border border-line bg-app">
      <div className="h-56 overflow-hidden bg-white">
        {loading && !content ? (
          <Skeleton className="h-full w-full rounded-none" />
        ) : content?.text != null ? (
          <iframe srcDoc={content.text} sandbox="" title={`Component ${index}`} className="h-full w-full bg-white" />
        ) : (
          <span className="grid h-full place-items-center text-2xs text-ink-3">Nothing to preview.</span>
        )}
      </div>
      <div className="flex items-center justify-between px-2 py-1 text-2xs text-ink-2">
        <span className="font-medium">#{index}</span>
        {score != null && <span className="tabular-nums">{formatScore(score)}</span>}
      </div>
    </div>
  )
}

/* ---------- stage ---------- */

export function DesignStage({
  run,
  iterations,
  scoreFor,
  nodeLabels,
  nodeTypes,
  nodeOrder,
  running,
  bestIteration,
}: {
  run: Run
  iterations: number[]
  scoreFor: (n: number) => number | null
  nodeLabels: Map<string, string>
  nodeTypes: Map<string, string>
  nodeOrder: Map<string, number>
  running: boolean
  bestIteration: number | null
}) {
  const [picked, setPicked] = useState<number | null>(null)
  const [view, setView] = useState<'render' | 'html'>('render')
  const [shotIdx, setShotIdx] = useState(0)
  const [details, setDetails] = useState(false)
  const [expanded, setExpanded] = useState(false)

  const data: IterationData[] = useMemo(
    () =>
      iterations.map((n) => {
        const arts = run.artifacts
          .filter((a) => a.iteration === n)
          .sort((a, b) => (nodeOrder.get(a.node_id) ?? 99) - (nodeOrder.get(b.node_id) ?? 99))
        const critiques = arts.filter((a) => a.kind === 'json' && ['agent', 'merge'].includes(nodeTypes.get(a.node_id) ?? ''))
        const mainCritique =
          critiques.find((a) => nodeTypes.get(a.node_id) === 'merge') ??
          critiques.find((a) => a.score != null) ??
          critiques[0] ??
          null
        return {
          n,
          score: scoreFor(n),
          shots: arts.filter((a) => a.kind === 'image'),
          html: arts.find((a) => a.kind === 'html') ?? null,
          critiques,
          mainCritique,
          running: run.steps.some((s) => s.iteration === n && s.status === 'running'),
        }
      }),
    [iterations, run.artifacts, run.steps, nodeOrder, nodeTypes, scoreFor],
  )

  const last = iterations[iterations.length - 1] ?? null
  const current = picked != null && iterations.includes(picked) ? picked : running ? last : (bestIteration ?? last)
  const idx = current != null ? iterations.indexOf(current) : -1
  const cur = idx >= 0 ? data[idx] : null
  const prev = idx > 0 ? data[idx - 1] : null

  const go = (delta: number): void => {
    const next = iterations[idx + delta]
    if (next != null) {
      setPicked(next)
      setShotIdx(0)
    }
  }

  // Reset per-iteration sub-selection when the shown iteration changes.
  useEffect(() => setShotIdx(0), [current])

  const results = run.results ?? []
  if (!running && results.length > 1) {
    return (
      <div className="grid gap-3 sm:grid-cols-2">
        {results.map((r, i) => (
          <ResultTile key={r.artifact_id} artifactId={r.artifact_id} index={i + 1} score={r.score} />
        ))}
      </div>
    )
  }

  if (!cur) return null

  const hasRender = cur.shots.length > 0
  const hasHtml = cur.html != null
  const mode: 'render' | 'html' = view === 'html' && hasHtml ? 'html' : hasRender ? 'render' : 'html'
  const shown: Artifact | null = mode === 'render' ? (cur.shots[Math.min(shotIdx, cur.shots.length - 1)] ?? null) : cur.html
  const steps = run.steps.filter((s) => s.iteration === cur.n)
  const isBest = bestIteration === cur.n && !running

  return (
    <div className="flex flex-col gap-2">
      <div
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === 'ArrowLeft') go(-1)
          else if (e.key === 'ArrowRight') go(1)
          else return
          e.preventDefault()
        }}
        className="flex flex-col gap-2 outline-none"
      >
        {/* stage toolbar */}
        <div className="flex items-center gap-2">
          <div className="flex items-center gap-0.5">
            <button
              type="button"
              aria-label="Previous iteration"
              disabled={idx <= 0}
              onClick={() => go(-1)}
              className="grid size-6 cursor-pointer place-items-center rounded-md text-ink-2 transition hover:bg-app disabled:cursor-default disabled:opacity-30"
            >
              <ChevronLeft size={15} />
            </button>
            <span className="min-w-14 text-center text-xs font-medium tabular-nums text-ink">
              {idx + 1} <span className="text-ink-3">/ {iterations.length}</span>
            </span>
            <button
              type="button"
              aria-label="Next iteration"
              disabled={idx >= iterations.length - 1}
              onClick={() => go(1)}
              className="grid size-6 cursor-pointer place-items-center rounded-md text-ink-2 transition hover:bg-app disabled:cursor-default disabled:opacity-30"
            >
              <ChevronRight size={15} />
            </button>
          </div>
          {cur.score != null && (
            <Badge tone={cur.score >= GOOD_SCORE ? 'success' : 'neutral'}>score {formatScore(cur.score)}</Badge>
          )}
          {isBest && <Badge tone="accent">best</Badge>}
          {cur.running && <Badge tone="accent">running</Badge>}
          <span className="flex-1" />
          {cur.shots.length > 1 && mode === 'render' && (
            <div className="flex items-center gap-1">
              {cur.shots.map((s, i) => (
                <button
                  key={s.id}
                  type="button"
                  onClick={() => setShotIdx(i)}
                  className={cx(
                    'cursor-pointer rounded-md px-1.5 py-0.5 text-2xs transition',
                    i === shotIdx ? 'bg-accent-soft font-medium text-accent-ink' : 'text-ink-3 hover:bg-app',
                  )}
                >
                  Shot {i + 1}
                </button>
              ))}
            </div>
          )}
          {hasRender && hasHtml && (
            <Segmented
              options={[
                { value: 'render', label: 'Render' },
                { value: 'html', label: 'Live HTML' },
              ]}
              value={mode}
              onChange={setView}
            />
          )}
          {shown && (
            <button
              type="button"
              aria-label="Expand"
              onClick={() => setExpanded(true)}
              className="grid size-6 cursor-pointer place-items-center rounded-md text-ink-2 transition hover:bg-app"
            >
              <Maximize2 size={13} />
            </button>
          )}
        </div>

        {/* the design */}
        <div className="max-h-[calc(100vh-14rem)] min-h-[320px] overflow-auto rounded-lg border border-line bg-app">
          {shown ? (
            <Render key={shown.id} artifact={shown} />
          ) : (
            <div className="grid h-72 place-items-center">
              {running ? (
                <Skeleton className="h-full w-full rounded-none" />
              ) : (
                <span className="text-xs text-ink-3">No render for this iteration.</span>
              )}
            </div>
          )}
        </div>

        <ChangeNote
          n={cur.n}
          score={cur.score}
          prevScore={prev?.score ?? null}
          basis={(prev ?? cur).mainCritique}
        />
      </div>

      {/* filmstrip */}
      {iterations.length > 1 && (
        <div className="flex gap-2 overflow-x-auto p-0.5">
          {data.map((d) => (
            <FilmThumb
              key={d.n}
              data={d}
              selected={d.n === cur.n}
              best={!running && d.n === bestIteration}
              onClick={() => {
                setPicked(d.n)
                setShotIdx(0)
              }}
            />
          ))}
        </div>
      )}

      {/* secondary stuff, out of the way */}
      {(cur.critiques.length > 0 || steps.length > 0) && (
        <div>
          <button
            type="button"
            onClick={() => setDetails((v) => !v)}
            className="inline-flex cursor-pointer items-center gap-1 text-xs text-ink-3 transition hover:text-ink-2"
          >
            <ChevronDown size={13} className={cx('transition-transform', !details && '-rotate-90')} />
            Critiques &amp; steps · iteration {cur.n}
          </button>
          {details && (
            <div className="mt-2 flex flex-col gap-2">
              {cur.critiques.map((a) => (
                <ArtifactCritiqueCard key={a.id} artifact={a} title={nodeLabels.get(a.node_id) ?? a.node_id} />
              ))}
              {steps.length > 0 && <StepsAccordion steps={steps} nodeLabels={nodeLabels} nodeTypes={nodeTypes} />}
            </div>
          )}
        </div>
      )}

      <Modal
        open={expanded}
        onClose={() => setExpanded(false)}
        size="xl"
        title={`Iteration ${cur.n}`}
        description={cur.score != null ? `score ${formatScore(cur.score)}` : undefined}
      >
        {shown && <Render artifact={shown} className="rounded-lg border border-line" />}
      </Modal>
    </div>
  )
}
