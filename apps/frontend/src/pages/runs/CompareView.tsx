/**
 * Compare mode (plan §5 Runs UI): pick any two iterations of the run and see
 * them side by side — score header, shots and critique summaries (from step
 * outputs / json artifacts, trimmed). Scroll syncing is intentionally not
 * synced; each column scrolls independently.
 */
import { useMemo, useState, type ReactNode } from 'react'
import { Badge, Card, CardBody, CardHeader, EmptyState } from '../../ui/Display'
import { Select } from '../../ui/Select'
import type { Run } from '../../api/runs'
import { ArtifactCritiqueCard, ShotsGrid } from './ArtifactViews'
import { formatScore, jsonText, looksLikeCritique, scoreOf } from './shared'

export interface CompareViewProps {
  run: Run
  iterations: number[]
  /** Best score per iteration (same source as the score chart). */
  scoreFor: (iteration: number) => number | null
  nodeLabels: Map<string, string>
  defaultA: number
  defaultB: number
}

function CompareColumn({
  run,
  iteration,
  score,
  nodeLabels,
}: {
  run: Run
  iteration: number
  score: number | null
  nodeLabels: Map<string, string>
}) {
  const artifacts = useMemo(() => run.artifacts.filter((a) => a.iteration === iteration), [run, iteration])
  const images = artifacts.filter((a) => a.kind === 'image')
  const jsonArts = artifacts.filter((a) => a.kind === 'json')
  const jsonNodes = useMemo(() => new Set(jsonArts.map((a) => a.node_id)), [jsonArts])
  const outputCritiques = useMemo(
    () => run.steps.filter((s) => s.iteration === iteration && s.output != null && looksLikeCritique(s.output) && !jsonNodes.has(s.node_id)),
    [run, iteration, jsonNodes],
  )

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex items-center justify-between gap-2">
        <p className="text-base font-semibold text-ink">Iteration {iteration}</p>
        <span className="inline-flex items-center gap-1.5 text-2xs text-ink-3">
          score <span className="font-mono text-ink">{formatScore(score)}</span>
        </span>
      </div>

      {images.length > 0 ? (
        <ShotsGrid artifacts={images} bestArtifactId={run.best_artifact_id} />
      ) : (
        <p className="text-xs text-ink-3">No shots in this iteration.</p>
      )}

      <div className="flex flex-col gap-2">
        {jsonArts.map((a) => (
          <ArtifactCritiqueCard
            key={a.id}
            artifact={a}
            title={nodeLabels.get(a.node_id) ?? a.node_id}
            compact
          />
        ))}
        {outputCritiques.map((s) => {
          const text = jsonText(s.output)
          return (
            <Card key={s.id}>
              <CardHeader
                title={nodeLabels.get(s.node_id) ?? s.node_id}
                actions={
                  scoreOf(s.output) != null ? (
                    <Badge tone="accent">score {scoreOf(s.output)}</Badge>
                  ) : undefined
                }
              />
              <CardBody className="pb-2">
                <pre className="overflow-hidden font-mono text-xs leading-relaxed text-ink-2" style={{ maxHeight: '8rem' }}>
                  {text.length > 420 ? `${text.slice(0, 420)}…` : text}
                </pre>
              </CardBody>
            </Card>
          )
        })}
        {jsonArts.length === 0 && outputCritiques.length === 0 && (
          <p className="text-xs text-ink-3">No critiques in this iteration.</p>
        )}
      </div>
    </div>
  )
}

export function CompareView({ run, iterations, scoreFor, nodeLabels, defaultA, defaultB }: CompareViewProps) {
  const options = iterations.map((n) => ({ value: String(n), label: `Iteration ${n}` }))
  const [a, setA] = useState(String(defaultA))
  const [b, setB] = useState(String(defaultB))

  if (iterations.length === 0) {
    return <EmptyState title="Nothing to compare yet" description="Run at least one iteration first." />
  }

  const pick = (
    value: string,
    onChange: (v: string) => void,
    other: string,
    aria: string,
  ): ReactNode => (
    <Select
      options={options}
      value={value}
      onChange={(v) => onChange(v === other ? value : v)} // keep the two sides distinct
      size="sm"
      ariaLabel={aria}
      className="min-w-36"
    />
  )

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        {pick(a, setA, b, 'Compare side A')}
        <span className="text-2xs text-ink-3">vs</span>
        {pick(b, setB, a, 'Compare side B')}
      </div>
      <div className="grid gap-6 lg:grid-cols-2">
        <CompareColumn run={run} iteration={Number(a)} score={scoreFor(Number(a))} nodeLabels={nodeLabels} />
        <CompareColumn run={run} iteration={Number(b)} score={scoreFor(Number(b))} nodeLabels={nodeLabels} />
      </div>
    </div>
  )
}
