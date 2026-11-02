/**
 * The pipeline body a run executed (graph + node labels/types). Prefers the
 * snapshot the run carries (`pipeline_body` — available to every viewer, and
 * exact for draft test runs); falls back to the studio API for older runs.
 */
import { useAsync } from '../../hooks'
import type { Run } from '../../api/runs'
import { getDef, getVersion, type PipelineBody } from '../../api/studio'

export function usePipelineBody(run: Run | null | undefined): { data: PipelineBody | null; loading: boolean } {
  const snapshot = run?.pipeline_body ?? null
  const pipelineId = run?.pipeline.id
  const version = run?.pipeline.version
  const hasSnapshot = snapshot != null

  const fallback = useAsync(
    async (signal): Promise<PipelineBody | null> => {
      if (hasSnapshot || !pipelineId) return null
      if (version != null) {
        const ver = await getVersion('pipeline', pipelineId, version, signal).catch(() => null)
        if (ver) return ver.body
      }
      const def = await getDef('pipeline', pipelineId, signal).catch(() => null)
      return def?.draft ?? null
    },
    [pipelineId, version, hasSnapshot],
  )

  if (snapshot) return { data: snapshot, loading: false }
  return { data: fallback.data ?? null, loading: fallback.loading }
}
