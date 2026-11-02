/**
 * Agents studio list (plan §4.4): searchable table of agent definitions —
 * name, slug, description, latest published version, last draft update.
 */
import { useState } from 'react'
import { useNavigate } from 'react-router'
import { Bot, MessageSquare } from 'lucide-react'
import { Badge, Button, Tooltip } from '../../../ui'
import { Callout } from '../../../ui/Display'
import { ErrorState, ListPage, absoluteTime, isBackendMissing, relativeTime } from '../../../components'
import { useAsync } from '../../../hooks'
import { listDefs, type StudioDef } from '../../../api/studio'
import { NewAgentModal } from './NewAgentModal'

export function AgentsList({ onOpen }: { onOpen: (def: StudioDef<'agent'>) => void }) {
  const defs = useAsync((signal) => listDefs('agent', undefined, signal), [])
  const [createOpen, setCreateOpen] = useState(false)
  const navigate = useNavigate()

  return (
    <div className="mx-auto w-full max-w-6xl p-5">
      {defs.error != null && defs.data == null ? (
        isBackendMissing(defs.error) ? (
          <Callout tone="warning" title="The studio backend isn't available yet">
            <p className="mt-1 text-sm">
              Agents can't be loaded or saved yet — the studio API hasn't been built.
            </p>
            {defs.error instanceof Error && <p className="mt-1 font-mono text-2xs text-ink-3">{defs.error.message}</p>}
          </Callout>
        ) : (
          <ErrorState error={defs.error} onRetry={defs.reload} />
        )
      ) : (
        <>
          <ListPage<StudioDef<'agent'>>
            title="Agents"
            description="Agents are the workers a pipeline calls: their instructions, tools, skills and model live here. Pipelines decide when each one runs."
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
              {
                key: 'chat',
                header: '',
                width: 'w-20',
                render: (d) =>
                  d.latest_version != null ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<MessageSquare />}
                      onClick={(e) => {
                        e.stopPropagation()
                        navigate(`/agent-chat?agent=${d.slug}`)
                      }}
                    >
                      Chat
                    </Button>
                  ) : null,
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
