/**
 * Artifact rendering pieces for the Runs UI (plan §5): an authorized content
 * loader hook (object URL revoked on unmount/id change), the shots grid with
 * lightbox, the HTML artifact preview (sandboxed iframe / source), critique
 * cards (raw JSON, no markdown rendering), and the live-output card used for
 * artifacts discovered mid-run whose kind is sniffed from the blob.
 */
import { useEffect, useState } from 'react'
import { FileCode, ImageOff } from 'lucide-react'
import { cx } from '../../ui/cx'
import { Badge, Card, CardBody, CardHeader, Skeleton } from '../../ui/Display'
import { Segmented } from '../../ui/Form'
import { Modal } from '../../components/Modal'
import { fetchArtifact, type Artifact, type ArtifactContent } from '../../api/runs'
import { formatScore, shortId } from './shared'

/* ---------- content loader ---------- */

export interface ArtifactContentState {
  loading: boolean
  error: unknown
  content: ArtifactContent | null
}

const EMPTY: ArtifactContentState = { loading: false, error: null, content: null }

/**
 * Loads one artifact's content (authorized) into an object URL and revokes it
 * when the id changes or the component unmounts. Also exposes the sniffed
 * kind/mime and, for textual artifacts, the text.
 */
export function useArtifactContent(id: string | null | undefined): ArtifactContentState {
  const [state, setState] = useState<ArtifactContentState>(EMPTY)
  useEffect(() => {
    if (!id) {
      setState(EMPTY)
      return
    }
    const ctrl = new AbortController()
    let alive = true
    let url: string | null = null
    setState((prev) => ({
      loading: true,
      error: null,
      content: prev.content && prev.content.id === id ? prev.content : null,
    }))
    fetchArtifact(id, ctrl.signal).then(
      (content) => {
        if (!alive) {
          URL.revokeObjectURL(content.url)
          return
        }
        url = content.url
        setState({ loading: false, error: null, content })
      },
      (err: unknown) => {
        if (!alive || ctrl.signal.aborted) return
        setState({ loading: false, error: err, content: null })
      },
    )
    return () => {
      alive = false
      ctrl.abort()
      if (url) URL.revokeObjectURL(url)
    }
  }, [id])
  return state
}

/* ---------- shots grid + lightbox ---------- */

/** One square shot thumbnail (image artifact); falls back to a file glyph when the blob isn't an image. */
export function ArtifactThumb({
  artifact,
  best = false,
  onClick,
}: {
  artifact: Artifact
  best?: boolean
  onClick?: () => void
}) {
  const { content, loading, error } = useArtifactContent(artifact.id)
  return (
    <button
      type="button"
      onClick={onClick}
      title={`${artifact.node_id} · ${shortId(artifact.id)}${artifact.score != null ? ` · score ${formatScore(artifact.score)}` : ''}`}
      className={cx(
        'group relative block aspect-square w-full cursor-pointer overflow-hidden rounded-lg border bg-app outline-none transition duration-100 hover:border-line-strong focus-visible:ring-2 focus-visible:ring-accent/40',
        best ? 'border-accent' : 'border-line',
      )}
    >
      {loading && <Skeleton className="absolute inset-0 rounded-none" />}
      {!loading && error != null && (
        <span className="grid h-full place-items-center text-ink-3">
          <ImageOff size={16} />
        </span>
      )}
      {!loading && content && content.kind === 'image' && (
        <img
          src={content.url}
          alt={`Shot from ${artifact.node_id} (${shortId(artifact.id)})`}
          draggable={false}
          className="h-full w-full object-cover transition-transform duration-100 group-hover:scale-[1.03]"
        />
      )}
      {!loading && content && content.kind !== 'image' && (
        <span className="grid h-full place-items-center text-ink-3">
          <FileCode size={16} />
        </span>
      )}
      {best && (
        <span className="absolute right-1 top-1">
          <Badge tone="accent">best</Badge>
        </span>
      )}
    </button>
  )
}

/** Full-size lightbox body for one artifact (inside a Modal). */
function LightboxImage({ artifact }: { artifact: Artifact }) {
  const { content, loading, error } = useArtifactContent(artifact.id)
  if (loading) return <Skeleton className="h-80 w-full" />
  if (error || !content)
    return <p className="text-xs text-danger">Couldn't load the artifact content.</p>
  if (content.kind !== 'image')
    return <MonoJson text={content.text ?? '(non-image content)'} />
  return (
    <img
      src={content.url}
      alt={`Shot from ${artifact.node_id} (${shortId(artifact.id)})`}
      className="mx-auto max-h-[68vh] w-auto rounded-lg border border-line"
    />
  )
}

function MonoJson({ text, maxHeight = '68vh' }: { text: string; maxHeight?: string }) {
  return (
    <pre
      style={{ maxHeight }}
      className="overflow-auto whitespace-pre-wrap break-words rounded-md bg-app p-3 font-mono text-xs leading-relaxed text-ink-2"
    >
      {text}
    </pre>
  )
}

/** Side-by-side shots for one iteration + shared lightbox. */
export function ShotsGrid({
  artifacts,
  bestArtifactId,
}: {
  artifacts: Artifact[]
  bestArtifactId?: string | null
}) {
  const [openId, setOpenId] = useState<string | null>(null)
  const open = artifacts.find((a) => a.id === openId) ?? null
  return (
    <>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(112px,1fr))] gap-2">
        {artifacts.map((a) => (
          <ArtifactThumb
            key={a.id}
            artifact={a}
            best={a.id === bestArtifactId}
            onClick={() => setOpenId(a.id)}
          />
        ))}
      </div>
      <Modal
        open={open != null}
        onClose={() => setOpenId(null)}
        size="lg"
        title={open ? `Shot · ${open.node_id}` : undefined}
        description={open ? shortId(open.id) : undefined}
      >
        {open && <LightboxImage artifact={open} />}
      </Modal>
    </>
  )
}

/* ---------- HTML artifact ---------- */

const VIEW_OPTIONS = [
  { value: 'preview', label: 'Preview' },
  { value: 'source', label: 'Source' },
] as const

/** HTML artifact card: sandboxed (script-free) preview or mono source. */
export function HtmlArtifactCard({
  artifact,
  title,
  className,
}: {
  artifact: Artifact
  title: string
  className?: string
}) {
  const [view, setView] = useState<'preview' | 'source'>('preview')
  const { content, loading, error } = useArtifactContent(artifact.id)
  return (
    <Card className={className}>
      <CardHeader
        title={title}
        description={`HTML artifact · ${shortId(artifact.id)}`}
        actions={<Segmented options={[...VIEW_OPTIONS]} value={view} onChange={setView} />}
      />
      <CardBody>
        {loading && <Skeleton className="h-72" />}
        {!loading && (error || !content || content.text == null) && (
          <p className="text-xs text-danger">Couldn't load the artifact content.</p>
        )}
        {!loading && content?.text != null && view === 'preview' && (
          <iframe
            srcDoc={content.text}
            sandbox=""
            title={`Preview of ${artifact.node_id} output`}
            className="h-80 w-full rounded-md border border-line bg-white"
          />
        )}
        {!loading && content?.text != null && view === 'source' && (
          <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-md bg-app p-2.5 font-mono text-xs leading-relaxed text-ink-2">
            {content.text}
          </pre>
        )}
      </CardBody>
    </Card>
  )
}

/* ---------- critiques ---------- */

/** Raw-JSON critique card (node name + pretty JSON, no markdown rendering). */
export function CritiqueCard({
  title,
  score,
  text,
  compact = false,
}: {
  title: string
  score?: number | null
  text: string
  /** Trims the JSON block further (compare columns). */
  compact?: boolean
}) {
  return (
    <Card>
      <CardHeader
        title={title}
        actions={
          score != null ? <Badge tone="accent">score {formatScore(score)}</Badge> : undefined
        }
      />
      <CardBody className={compact ? 'pb-2' : undefined}>
        {compact ? (
          <pre className="overflow-hidden font-mono text-xs leading-relaxed text-ink-2" style={{ maxHeight: '8rem' }}>
            {text.length > 420 ? `${text.slice(0, 420)}…` : text}
          </pre>
        ) : (
          <MonoJson text={text} maxHeight="18rem" />
        )}
      </CardBody>
    </Card>
  )
}

/** Critique card fed directly from artifact metadata (loads its JSON content). */
export function ArtifactCritiqueCard({
  artifact,
  title,
  compact = false,
}: {
  artifact: Artifact
  title: string
  compact?: boolean
}) {
  const { content, loading, error } = useArtifactContent(artifact.id)
  return (
    <CritiqueCard
      title={title}
      score={artifact.score}
      text={loading ? 'Loading…' : error || !content ? '(couldn\'t load artifact content)' : (content.text ?? '(empty)')}
      compact={compact}
    />
  )
}
