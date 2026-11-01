/**
 * Per-iteration panel (plan §5 Runs UI): shots side by side, the HTML artifact
 * preview, critiques (kind `json` artifacts + step outputs that look like a
 * critique), and — while a run streams — live outputs discovered through
 * `step_completed` events (ids not yet in the run's artifact list; their kind
 * is sniffed from the fetched blob).
 */
import { useMemo } from 'react'
import { Zap } from 'lucide-react'
import { Badge, Card, CardBody, CardHeader, EmptyState } from '../../ui/Display'
import type { Run } from '../../api/runs'
import {
  ArtifactCritiqueCard,
  HtmlArtifactCard,
  ShotsGrid,
  useArtifactContent,
} from './ArtifactViews'
import { jsonText, looksLikeCritique, scoreOf, shortId } from './shared'

/** One artifact discovered live via SSE — kind comes from the blob itself. */
function LiveArtifactCard({ artifactId, nodeId, nodeLabels }: { artifactId: string; nodeId: string; nodeLabels: Map<string, string> }) {
  const { content, loading, error } = useArtifactContent(artifactId)
  const title = nodeLabels.get(nodeId) ?? nodeId
  return (
    <Card>
      <CardHeader
        title={
          <span className="inline-flex items-center gap-2">
            <Zap size={13} className="shrink-0 text-accent" />
            {title}
          </span>
        }
        description={`New output · ${shortId(artifactId)}`}
        actions={
          content && <Badge tone="accent">{content.kind}</Badge>
        }
      />
      <CardBody>
        {loading && <div className="h-40" />}
        {!loading && (error || !content) && (
          <p className="text-xs text-danger">Couldn't load this output.</p>
        )}
        {!loading && content && content.kind === 'image' && (
          <img
            src={content.url}
            alt={`Live output from ${nodeId}`}
            className="max-h-64 w-auto rounded-md border border-line"
          />
        )}
        {!loading && content && content.kind === 'html' && (
          <iframe
            srcDoc={content.text ?? ''}
            sandbox=""
            title={`Live output from ${nodeId}`}
            className="h-64 w-full rounded-md border border-line bg-white"
          />
        )}
        {!loading && content && content.kind === 'json' && (
          <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-md bg-app p-2.5 font-mono text-xs leading-relaxed text-ink-2">
            {content.text ?? '(empty)'}
          </pre>
        )}
      </CardBody>
    </Card>
  )
}

export interface IterationPanelProps {
  run: Run
  iteration: number
  nodeLabels: Map<string, string>
}

export function IterationPanel({ run, iteration, nodeLabels }: IterationPanelProps) {
  const steps = useMemo(() => run.steps.filter((s) => s.iteration === iteration), [run, iteration])
  const artifacts = useMemo(
    () => run.artifacts.filter((a) => a.iteration === iteration),
    [run, iteration],
  )

  const knownIds = useMemo(() => new Set(run.artifacts.map((a) => a.id)), [run])
  const images = artifacts.filter((a) => a.kind === 'image')
  const html = artifacts.filter((a) => a.kind === 'html')
  const jsonArts = artifacts.filter((a) => a.kind === 'json')
  const jsonNodes = useMemo(() => new Set(jsonArts.map((a) => a.node_id)), [jsonArts])
  const pending = useMemo(
    () =>
      steps.flatMap((s) =>
        (s.artifact_ids ?? [])
          .filter((id) => !knownIds.has(id))
          .map((id) => ({ id, node_id: s.node_id })),
      ),
    [steps, knownIds],
  )
  const outputCritiques = useMemo(
    () => steps.filter((s) => s.output != null && looksLikeCritique(s.output) && !jsonNodes.has(s.node_id)),
    [steps, jsonNodes],
  )

  const nothing = steps.length === 0 && artifacts.length === 0 && pending.length === 0
  if (nothing) {
    return (
      <EmptyState
        title="Nothing recorded for this iteration"
        description="No steps or artifacts were persisted for it (yet)."
      />
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {pending.length > 0 && (
        <div className="flex flex-col gap-2">
          {pending.map((p) => (
            <LiveArtifactCard key={p.id} artifactId={p.id} nodeId={p.node_id} nodeLabels={nodeLabels} />
          ))}
        </div>
      )}

      {images.length > 0 && <ShotsGrid artifacts={images} bestArtifactId={run.best_artifact_id} />}

      {html.map((a) => (
        <HtmlArtifactCard
          key={a.id}
          artifact={a}
          title={nodeLabels.get(a.node_id) ?? a.node_id}
        />
      ))}

      {(jsonArts.length > 0 || outputCritiques.length > 0) && (
        <div className="flex flex-col gap-2">
          {jsonArts.map((a) => (
            <ArtifactCritiqueCard
              key={a.id}
              artifact={a}
              title={nodeLabels.get(a.node_id) ?? a.node_id}
            />
          ))}
          {outputCritiques.map((s) => (
            <Card key={s.id}>
              <CardHeader
                title={nodeLabels.get(s.node_id) ?? s.node_id}
                actions={
                  scoreOf(s.output) != null ? (
                    <Badge tone="accent">score {scoreOf(s.output)}</Badge>
                  ) : undefined
                }
              />
              <CardBody>
                <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md bg-app p-2.5 font-mono text-xs leading-relaxed text-ink-2">
                  {jsonText(s.output)}
                </pre>
              </CardBody>
            </Card>
          ))}
        </div>
      )}
    </div>
  )
}
