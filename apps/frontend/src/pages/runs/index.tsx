/**
 * Runs list (plan §5 Runs UI): compact table of pipeline runs — id, status,
 * pipeline version, iterations, best score, cost, started — with a "New run"
 * modal (pipeline + input form → startRun → navigate to the run).
 */
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import { Play } from 'lucide-react'
import { ListPage, PageHeader, ErrorState, absoluteTime, relativeTime } from '../../components'
import { useAsync } from '../../hooks'
import { listRuns, type RunListItem } from '../../api/runs'
import { RunStatusBadge, formatCost, formatScore, shortId } from './shared'
import { NewRunModal } from './NewRunModal'

/** Row as passed to ListPage — `name` mirrors the pipeline name so the built-in search matches runs. */
type RunRow = RunListItem & { name: string }

export default function RunsPage() {
  const navigate = useNavigate()
  const runsQ = useAsync((signal) => listRuns({ limit: 100 }, signal), [])
  const [newOpen, setNewOpen] = useState(false)

  const rows = useMemo<RunRow[] | null>(
    () => runsQ.data?.items.map((r) => ({ ...r, name: r.pipeline.name })) ?? null,
    [runsQ.data],
  )

  if (runsQ.error && !runsQ.data) {
    return (
      <div className="p-5">
        <PageHeader
          title="Runs"
          description="Pipeline executions and their results."
        />
        <div className="mt-4">
          <ErrorState error={runsQ.error} onRetry={runsQ.reload} />
        </div>
      </div>
    )
  }

  return (
    <div className="p-5">
      <ListPage<RunRow>
        title="Runs"
        description="Pipeline executions and their results."
        newButton={{ label: 'New run', onClick: () => setNewOpen(true) }}
        searchPlaceholder="Search by pipeline…"
        columns={[
          {
            key: 'id',
            header: 'Run',
            width: 'w-24',
            render: (r) => (
              <span className="font-mono text-xs text-ink" title={r.id}>
                {shortId(r.id)}
              </span>
            ),
          },
          {
            key: 'status',
            header: 'Status',
            width: 'w-28',
            render: (r) => <RunStatusBadge status={r.status} />,
          },
          {
            key: 'pipeline',
            header: 'Pipeline',
            width: 'w-56',
            render: (r) => (
              <span className="flex min-w-0 items-baseline gap-1.5">
                <span className="truncate text-ink">{r.pipeline.name}</span>
                {r.pipeline.version != null && (
                  <span className="shrink-0 font-mono text-2xs text-ink-3">v{r.pipeline.version}</span>
                )}
              </span>
            ),
          },
          {
            key: 'iterations',
            header: 'Iterations',
            width: 'w-20',
            render: (r) => <span className="font-mono text-xs">{r.iterations}</span>,
          },
          {
            key: 'best_score',
            header: 'Best score',
            width: 'w-20',
            render: (r) => <span className="font-mono text-xs">{formatScore(r.best_score)}</span>,
          },
          {
            key: 'cost',
            header: 'Cost',
            width: 'w-24',
            render: (r) => <span className="font-mono text-xs">{formatCost(r.cost)}</span>,
          },
          {
            key: 'created_at',
            header: 'Started',
            width: 'w-24',
            render: (r) => (
              <span title={absoluteTime(r.created_at)} className="whitespace-nowrap">
                {relativeTime(r.created_at)}
              </span>
            ),
          },
        ]}
        rows={rows}
        loading={runsQ.loading}
        onOpen={(row) => navigate(`/runs/${row.id}`)}
        emptyState={{
          icon: Play,
          title: 'No runs yet',
          description: 'Start a run to see it here.',
        }}
      />
      <NewRunModal open={newOpen} onClose={() => setNewOpen(false)} />
    </div>
  )
}
