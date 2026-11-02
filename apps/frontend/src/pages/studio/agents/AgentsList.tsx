/**
 * Agents studio list (plan §4.4): searchable table of agent definitions —
 * name, slug, description, latest published version, last draft update.
 */
import { useState } from 'react'
import { Bot, Trash2 } from 'lucide-react'
import { Badge, IconButton, Tooltip } from '../../../ui'
import { Callout } from '../../../ui/Display'
import { ConfirmDialog, ErrorState, ListPage, absoluteTime, isBackendMissing, relativeTime } from '../../../components'
import { useAsync } from '../../../hooks'
import { deleteAgent, getAgentUsage, listDefs, type StudioDef } from '../../../api/studio'
import { NewAgentModal } from './NewAgentModal'

export function AgentsList({ onOpen }: { onOpen: (def: StudioDef<'agent'>) => void }) {
  const defs = useAsync((signal) => listDefs('agent', undefined, signal), [])
  const usageQ = useAsync((signal) => getAgentUsage(signal), [])
  const usage = usageQ.data?.usage
  const [createOpen, setCreateOpen] = useState(false)
  const [deleting, setDeleting] = useState<StudioDef<'agent'> | null>(null)
  const [busy, setBusy] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const closeDelete = () => {
    setDeleting(null)
    setDeleteError(null)
  }

  const confirmDelete = async () => {
    if (!deleting) return
    setBusy(true)
    setDeleteError(null)
    try {
      await deleteAgent(deleting.id)
      closeDelete()
      defs.reload()
      usageQ.reload()
    } catch (e) {
      // 409: a pipeline started using it meanwhile — refresh the Used in column.
      setDeleteError(e instanceof Error ? e.message : 'Could not delete the agent.')
      usageQ.reload()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto w-full max-w-6xl p-5">
      {defs.error != null && defs.data == null ? (
        isBackendMissing(defs.error) ? (
          <Callout tone="warning" title="The studio backend isn't available yet">
            <p className="mt-1 text-sm">
              Agents can't be loaded or saved right now — the studio API isn't responding.
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
                key: 'used_in',
                header: 'Used in',
                width: 'w-56',
                render: (d) => {
                  if (usage == null) return <span className="text-ink-3">…</span>
                  const refs = usage[d.id] ?? []
                  if (refs.length === 0) return <span className="text-ink-3">Unused</span>
                  return (
                    <Tooltip content={refs.map((p) => p.name).join(', ')} side="top">
                      <span className="flex min-w-0 items-center gap-1.5">
                        <span className="truncate text-ink-2">{refs[0].name}</span>
                        {refs.length > 1 && <Badge tone="neutral">+{refs.length - 1}</Badge>}
                      </span>
                    </Tooltip>
                  )
                },
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
                key: 'actions',
                header: '',
                width: 'w-12',
                render: (d) => {
                  const refs = usage?.[d.id] ?? []
                  const blocked = usage == null || refs.length > 0
                  return (
                    <span onClick={(e) => e.stopPropagation()} className="flex justify-end">
                      <Tooltip
                        content={
                          usage == null
                            ? 'Checking usage…'
                            : refs.length > 0
                              ? `Used in ${refs.length} pipeline${refs.length > 1 ? 's' : ''} — remove it there first`
                              : 'Delete agent'
                        }
                        side="top"
                      >
                        <IconButton
                          icon={<Trash2 />}
                          label="Delete agent"
                          size="sm"
                          disabled={blocked}
                          onClick={() => setDeleting(d)}
                        />
                      </Tooltip>
                    </span>
                  )
                },
              },
            ]}
            rows={defs.data?.items}
            loading={defs.loading && defs.data == null}
            onOpen={onOpen}
            emptyState={{
              icon: Bot,
              title: 'No agents yet',
              description: 'Create an agent to use it in pipelines.',
            }}
          />
          <NewAgentModal open={createOpen} onClose={() => setCreateOpen(false)} onCreated={onOpen} />
          <ConfirmDialog
            open={deleting != null}
            onClose={closeDelete}
            title="Delete agent"
            danger
            busy={busy}
            confirmLabel="Delete agent"
            onConfirm={confirmDelete}
            body={
              <div className="flex flex-col gap-2">
                <p>
                  Delete <span className="font-medium text-ink">{deleting?.name}</span>{' '}
                  <span className="font-mono text-xs text-ink-3">({deleting?.slug})</span>? Its draft and all
                  published versions are removed permanently. This can't be undone.
                </p>
                {deleteError != null && <p className="text-sm text-danger">{deleteError}</p>}
              </div>
            }
          />
        </>
      )}
    </div>
  )
}
