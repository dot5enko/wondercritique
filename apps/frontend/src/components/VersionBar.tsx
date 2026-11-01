/**
 * Version strip at the top of every studio editor: draft status, Save draft,
 * Publish (note modal), Versions popover → diff modal with "Restore as draft"
 * (guarded by a ConfirmDialog when the draft is dirty). Calls the studio API
 * directly — pages only pass state.
 */
import { useEffect, useRef, useState } from 'react'
import { History, RotateCcw, Save, Upload } from 'lucide-react'
import { cx } from '../ui/cx'
import { Button } from '../ui/Button'
import { Field, Textarea } from '../ui/Form'
import { Badge, Skeleton, StatusDot, Tooltip } from '../ui/Display'
import { useDismiss } from '../ui/Select'
import {
  listVersions,
  publishDef,
  putDraft,
  restoreVersion,
  type BodyOf,
  type StudioDef,
  type StudioKind,
  type StudioVersion,
} from '../api/studio'
import { Modal } from './Modal'
import { ConfirmDialog } from './ConfirmDialog'
import { DiffView, diffJson } from './Diff'
import { absoluteTime, relativeTime } from './format'

export interface VersionBarProps {
  kind: StudioKind
  def: StudioDef<StudioKind> | null
  draft: unknown
  onDraftLoaded: (body: unknown) => void
  dirty: boolean
  /** Called after a successful save/publish/restore so the page can refetch. */
  onSaved?: () => void
  className?: string
}

function message(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback
}

export function VersionBar({ kind, def, draft, onDraftLoaded, dirty, onSaved, className }: VersionBarProps) {
  const [lastSavedAt, setLastSavedAt] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  const [publishOpen, setPublishOpen] = useState(false)
  const [note, setNote] = useState('')
  const [publishing, setPublishing] = useState(false)
  const [publishError, setPublishError] = useState<string | null>(null)

  const [versionsOpen, setVersionsOpen] = useState(false)
  const [versions, setVersions] = useState<StudioVersion<StudioKind>[] | null>(null)
  const [loadingVersions, setLoadingVersions] = useState(false)

  const [diffTarget, setDiffTarget] = useState<StudioVersion<StudioKind> | null>(null)
  const [confirmRestore, setConfirmRestore] = useState<StudioVersion<StudioKind> | null>(null)
  const [restoring, setRestoring] = useState(false)
  const [restoreError, setRestoreError] = useState<string | null>(null)

  const popRef = useRef<HTMLDivElement>(null)
  useDismiss(popRef, () => setVersionsOpen(false), versionsOpen)

  /* Reset the local "saved at" when switching definitions. */
  useEffect(() => {
    setLastSavedAt(null)
    setSaveError(null)
  }, [def?.id])

  /* Load (or refresh) the version list whenever the popover opens. */
  useEffect(() => {
    if (!versionsOpen || !def) return
    let alive = true
    setLoadingVersions(true)
    listVersions(kind, def.id)
      .then((v) => {
        if (alive) setVersions(v.items)
      })
      .catch(() => {
        if (alive) setVersions([])
      })
      .finally(() => {
        if (alive) setLoadingVersions(false)
      })
    return () => {
      alive = false
    }
  }, [versionsOpen, def, kind])

  const savedAt = lastSavedAt ?? def?.draft_updated_at ?? null
  const status = dirty
    ? 'Draft — unsaved changes'
    : savedAt
      ? `Draft saved ${relativeTime(savedAt)}`
      : 'Draft'

  const save = async (): Promise<void> => {
    if (!def || saving) return
    setSaving(true)
    setSaveError(null)
    try {
      const updated = await putDraft(kind, def.id, draft as BodyOf[StudioKind])
      setLastSavedAt(updated.draft_updated_at ?? new Date().toISOString())
      onSaved?.()
    } catch (err) {
      setSaveError(message(err, 'Failed to save draft'))
    } finally {
      setSaving(false)
    }
  }

  const publish = async (): Promise<void> => {
    if (!def || publishing) return
    setPublishing(true)
    setPublishError(null)
    try {
      await publishDef(kind, def.id, note)
      setPublishOpen(false)
      setNote('')
      onSaved?.()
    } catch (err) {
      setPublishError(message(err, 'Failed to publish'))
    } finally {
      setPublishing(false)
    }
  }

  const doRestore = async (v: StudioVersion<StudioKind>): Promise<void> => {
    if (!def || restoring) return
    setRestoring(true)
    setRestoreError(null)
    try {
      const updated = await restoreVersion(kind, def.id, v.version)
      setDiffTarget(null)
      setConfirmRestore(null)
      onDraftLoaded(updated.draft)
      onSaved?.()
    } catch (err) {
      setRestoreError(message(err, 'Failed to restore version'))
    } finally {
      setRestoring(false)
    }
  }

  const requestRestore = (v: StudioVersion<StudioKind>): void => {
    if (dirty) setConfirmRestore(v)
    else void doRestore(v)
  }

  const sorted = [...(versions ?? [])].sort((a, b) => b.version - a.version)
  const nextVersion = (def?.latest_version ?? 0) + 1

  return (
    <div className={cx('flex items-center gap-2.5 border-b border-line bg-surface px-4 py-2', className)}>
      <StatusDot tone={dirty ? 'warning' : 'success'} />
      <Tooltip content={absoluteTime(savedAt)} side="bottom">
        <span className={cx('cursor-default text-xs', dirty ? 'text-ink' : 'text-ink-2')}>{status}</span>
      </Tooltip>
      {def?.latest_version != null && <Badge tone="neutral">v{def.latest_version}</Badge>}
      {def?.archived && <Badge tone="warning">Archived</Badge>}
      {saveError && <span className="min-w-0 truncate text-xs text-danger">{saveError}</span>}

      <div className="ml-auto flex shrink-0 items-center gap-1.5">
        <Button
          size="sm"
          variant="secondary"
          icon={<Save />}
          loading={saving}
          disabled={!def || !dirty}
          onClick={() => void save()}
        >
          Save draft
        </Button>
        <Button
          size="sm"
          variant="primary"
          icon={<Upload />}
          disabled={!def}
          onClick={() => setPublishOpen(true)}
        >
          Publish
        </Button>

        <div className="relative">
          <Button
            size="sm"
            variant="ghost"
            icon={<History />}
            disabled={!def}
            onClick={() => setVersionsOpen((o) => !o)}
          >
            Versions
          </Button>
          {versionsOpen && (
            <div
              ref={popRef}
              className="animate-pop absolute right-0 top-full z-40 mt-1 w-80 rounded-lg border border-line bg-surface p-1 shadow-pop"
            >
              <p className="px-2 pb-0.5 pt-1.5 text-2xs font-medium text-ink-3">Version history</p>
              {loadingVersions && (
                <div className="space-y-1.5 p-2">
                  <Skeleton className="h-8" />
                  <Skeleton className="h-8" />
                </div>
              )}
              {!loadingVersions && sorted.length === 0 && (
                <p className="px-2 py-3 text-xs text-ink-3">No published versions yet.</p>
              )}
              {!loadingVersions &&
                sorted.map((v) => (
                  <button
                    key={v.version}
                    type="button"
                    onClick={() => {
                      setVersionsOpen(false)
                      setDiffTarget(v)
                    }}
                    className="flex w-full cursor-pointer flex-col items-start gap-0.5 rounded-md px-2 py-1.5 text-left transition-colors duration-100 hover:bg-app"
                  >
                    <span className="flex w-full items-center gap-2">
                      <span className="font-mono text-2xs text-ink-3">v{v.version}</span>
                      <span className="min-w-0 flex-1 truncate text-base text-ink">{v.note || '—'}</span>
                    </span>
                    <span className="truncate text-2xs text-ink-3">
                      {v.published_by} · {relativeTime(v.published_at)}
                    </span>
                  </button>
                ))}
            </div>
          )}
        </div>
      </div>

      {/* Publish: optional note → immutable version */}
      <Modal
        open={publishOpen}
        onClose={publishing ? () => {} : () => setPublishOpen(false)}
        size="sm"
        title="Publish version"
        description={`Creates immutable v${nextVersion} from the current draft.`}
        footer={
          <>
            {publishError && <span className="mr-auto min-w-0 truncate text-xs text-danger">{publishError}</span>}
            <Button size="sm" variant="secondary" disabled={publishing} onClick={() => setPublishOpen(false)}>
              Cancel
            </Button>
            <Button size="sm" variant="primary" loading={publishing} onClick={() => void publish()}>
              Publish
            </Button>
          </>
        }
      >
        <Field label="Note" optional hint="What changed in this version?">
          <Textarea
            value={note}
            rows={3}
            autoFocus
            disabled={publishing}
            onChange={(e) => setNote(e.target.value)}
          />
        </Field>
      </Modal>

      {/* Diff: selected version vs current draft */}
      <Modal
        open={diffTarget !== null}
        onClose={restoring ? () => {} : () => setDiffTarget(null)}
        size="lg"
        title={diffTarget ? `v${diffTarget.version} vs current draft` : undefined}
        description={
          diffTarget
            ? `${diffTarget.published_by} · ${relativeTime(diffTarget.published_at)}${diffTarget.note ? ` — ${diffTarget.note}` : ''}`
            : undefined
        }
        footer={
          <>
            {restoreError && <span className="mr-auto min-w-0 truncate text-xs text-danger">{restoreError}</span>}
            <Button size="sm" variant="secondary" disabled={restoring} onClick={() => setDiffTarget(null)}>
              Close
            </Button>
            <Button
              size="sm"
              variant="secondary"
              icon={<RotateCcw />}
              loading={restoring}
              disabled={!diffTarget}
              onClick={() => diffTarget && requestRestore(diffTarget)}
            >
              Restore as draft
            </Button>
          </>
        }
      >
        {diffTarget && (
          <div className="max-h-[60vh] overflow-y-auto">
            <DiffView hunks={diffJson(diffTarget.body, draft)} />
          </div>
        )}
      </Modal>

      {/* Dirty guard: restoring would discard unsaved edits */}
      <ConfirmDialog
        open={confirmRestore !== null}
        onClose={() => setConfirmRestore(null)}
        title="Discard unsaved changes?"
        body={`Restoring v${confirmRestore?.version ?? ''} will replace the current draft. Unsaved changes will be lost.`}
        confirmLabel="Restore"
        danger
        busy={restoring}
        onConfirm={() => confirmRestore && void doRestore(confirmRestore)}
      />
    </div>
  )
}
