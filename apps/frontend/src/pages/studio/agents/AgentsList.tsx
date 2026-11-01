/**
 * Agents studio list (plan §4.4): searchable table of agent definitions —
 * name, slug, description, latest published version, last draft update.
 */
import { useState } from 'react'
import { Bot } from 'lucide-react'
import { Badge, Tooltip } from '../../../ui'
import { ErrorState, ListPage, absoluteTime, relativeTime } from '../../../components'
import { useAsync } from '../../../hooks'
import { listDefs, type StudioDef } from '../../../api/studio'
import { NewAgentModal } from './NewAgentModal'

export function AgentsList({ onOpen }: { onOpen: (def: StudioDef<'agent'>) => void }) {
  const defs = useAsync((signal) => listDefs('agent', undefined, signal), [])
  const [createOpen, setCreateOpen] = useState(false)

  return (
    <div className="mx-auto w-full max-w-6xl p-5">
      {defs.error != null && defs.data == null ? (
        <ErrorState error={defs.error} onRetry={defs.reload} />
      ) : (
        <>
          <ListPage<StudioDef<'agent'>>
            title="Agents"
            description="Reusable agents: prompts, model sets, tools and output contracts."
            newButton={{ label: 'New agent', onClick: () => setCreateOpen(true) }}
            searchPlaceholder="Search agents…"
            columns={[
              {
                key: 'name',
                header: 'Name',
                width: 'w-56',
                render: (d) => (
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="truncate font-medium text-ink">{d.name}</span>
                    {d.archived && <Badge tone="warning">Archived</Badge>}
                  </span>
                ),
              },
              {
                key: 'slug',
                header: 'Slug',
                width: 'w-44',
                render: (d) => <span className="block truncate font-mono text-xs text-ink-2">{d.slug}</span>,
              },
              {
                key: 'description',
                header: 'Description',
                width: 'w-72',
                render: (d) => <span className="block truncate text-ink-2">{d.description || '—'}</span>,
              },
              {
                key: 'latest_version',
                header: 'Version',
                width: 'w-20',
                render: (d) => (d.latest_version != null ? `v${d.latest_version}` : '—'),
              },
              {
                key: 'draft_updated_at',
                header: 'Updated',
                width: 'w-28',
                render: (d) => (
                  <Tooltip content={absoluteTime(d.draft_updated_at)} side="top">
                    <span className="cursor-default">{relativeTime(d.draft_updated_at)}</span>
                  </Tooltip>
                ),
              },
            ]}
            rows={defs.data?.items}
            loading={defs.loading && defs.data == null}
            onOpen={onOpen}
            emptyState={{
              icon: Bot,
              title: 'No agents yet',
              description: 'Create an agent to use it in pipelines and chat.',
            }}
          />
          <NewAgentModal open={createOpen} onClose={() => setCreateOpen(false)} onCreated={onOpen} />
        </>
      )}
    </div>
  )
}
