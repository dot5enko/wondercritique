/**
 * Skill editor (plan §4.6) — PageHeader + VersionBar over a two-pane body:
 * file tree on the left, per-file textarea on the right. A skill body is a
 * plain `{ files: [{ path, content }] }` tree; the version manifest of blob
 * hashes is built server-side on publish, so the frontend only edits files.
 *
 * TODO: run-script endpoint (plan §4.6) — "run script in the sandbox" has no
 * API yet; when it lands, a Run action belongs in the file tree header.
 */
import { useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { FileCode2, FilePlus2, FileText, Info, Pencil, Trash2, TriangleAlert } from 'lucide-react'
import { Button, cx, EmptyState, Field, IconButton, Input, Textarea, Tooltip } from '../../../ui'
import { ConfirmDialog, Loadable, Modal, PageHeader, VersionBar } from '../../../components'
import { useAsync, useDirtyDraft } from '../../../hooks'
import { getEditableDef, putDraft, type SkillBody, type StudioDef } from '../../../api/studio'
import { emptySkillBody, isSkillEntry, skillPathError } from './body'

export default function SkillsEditor({ id }: { id: string }) {
  const def = useAsync((signal) => getEditableDef<'skill'>('skill', id, signal), [id])
  return (
    <Loadable
      loading={def.loading && !def.data}
      error={def.data ? null : def.error}
      onRetry={def.reload}
      className="p-6"
    >
      {def.data && <SkillsEditorBody def={def.data} refreshDef={def.reload} />}
    </Loadable>
  )
}

function SkillsEditorBody({ def, refreshDef }: { def: StudioDef<'skill'>; refreshDef: () => void }) {
  const d = useDirtyDraft<SkillBody>(def.draft ?? emptySkillBody(def.name), {
    autosave: (body) => putDraft('skill', def.id, body),
    onSaved: refreshDef,
  })
  const files = d.draft.files

  const [activePath, setActivePath] = useState<string | null>(null)
  const active = files.find((f) => f.path === activePath) ?? files[0] ?? null

  const [addOpen, setAddOpen] = useState(false)
  const [addPath, setAddPath] = useState('')
  const [renamingPath, setRenamingPath] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [deletingPath, setDeletingPath] = useState<string | null>(null)
  /** Guards the blur-commit against the Escape path (and stray blurs). */
  const renameCancelled = useRef(false)

  /* ---------- file tree mutations (all flow through setDraft → autosave) ---------- */

  const setFileContent = (path: string, content: string): void =>
    d.setDraft((prev) => ({ ...prev, files: prev.files.map((f) => (f.path === path ? { ...f, content } : f)) }))

  const addFile = (path: string): void => {
    d.setDraft((prev) => ({ ...prev, files: [...prev.files, { path, content: '' }] }))
    setActivePath(path)
    setAddPath('')
    setAddOpen(false)
  }

  const startRename = (path: string): void => {
    renameCancelled.current = false
    setRenamingPath(path)
    setRenameValue(path)
  }

  const renameOthers = renamingPath ? files.filter((f) => f.path !== renamingPath).map((f) => f.path) : []
  const renameTargetError = renamingPath ? skillPathError(renameValue, renameOthers) : null

  const commitRename = (): void => {
    const from = renamingPath
    if (!from || renameCancelled.current) {
      setRenamingPath(null)
      return
    }
    if (renameTargetError) {
      setRenamingPath(null)
      return
    }
    renameCancelled.current = true
    const to = renameValue.trim()
    d.setDraft((prev) => ({ ...prev, files: prev.files.map((f) => (f.path === from ? { ...f, path: to } : f)) }))
    setActivePath((p) => (p === from ? to : p))
    setRenamingPath(null)
  }

  const cancelRename = (): void => {
    renameCancelled.current = true
    setRenamingPath(null)
  }

  const deleteFile = (path: string): void => {
    d.setDraft((prev) => ({ ...prev, files: prev.files.filter((f) => f.path !== path) }))
    if (activePath === path) setActivePath(null)
    setDeletingPath(null)
  }

  /* Tab inserts two spaces (spec: the content textarea is code-friendly). */
  const onContentKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>): void => {
    if (e.key !== 'Tab' || !active) return
    e.preventDefault()
    const el = e.currentTarget
    const start = el.selectionStart
    const end = el.selectionEnd
    setFileContent(active.path, `${el.value.slice(0, start)}  ${el.value.slice(end)}`)
    requestAnimationFrame(() => {
      el.selectionStart = start + 2
      el.selectionEnd = start + 2
    })
  }

  const addPathError = addPath.trim() ? skillPathError(addPath, files.map((f) => f.path)) : null

  return (
    <div>
      <PageHeader back={{ to: '/studio/skills', label: 'Skills' }} title={def.name} description={def.description || undefined} />

      <div className="mt-4 overflow-hidden rounded-lg border border-line bg-surface">
        <VersionBar
          kind="skill"
          def={def}
          draft={d.draft}
          dirty={d.dirty}
          onDraftLoaded={(body) => d.reset(body as SkillBody)}
          onSaved={refreshDef}
        />
        {d.saveState === 'error' && (
          <div className="flex items-center gap-1.5 border-b border-line bg-danger-soft px-4 py-1.5 text-xs text-danger">
            <TriangleAlert size={13} className="shrink-0" />
            Autosave failed — your edits are kept. Use “Save draft” to retry.
          </div>
        )}

        <div className="flex items-stretch">
          {/* Left: file tree */}
          <aside className="flex w-60 shrink-0 flex-col border-r border-line bg-sunken">
            <div className="flex items-center gap-1.5 border-b border-line px-3 py-2">
              <span className="text-2xs font-medium uppercase tracking-wide text-ink-3">Files</span>
              <span className="text-2xs text-ink-3">{files.length}</span>
              <IconButton
                icon={<FilePlus2 />}
                label="Add file"
                size="sm"
                className="ml-auto"
                onClick={() => {
                  setAddPath('')
                  setAddOpen(true)
                }}
              />
            </div>
            <div className="flex-1 overflow-y-auto p-1.5">
              {files.map((file) => {
                const isActive = active?.path === file.path
                const Icon = file.path.endsWith('.js') ? FileCode2 : FileText
                const entry = isSkillEntry(file.path)
                if (renamingPath === file.path) {
                  return (
                    <div key={file.path} className="px-0.5 py-0.5">
                      <input
                        autoFocus
                        value={renameValue}
                        aria-label="File path"
                        onChange={(e) => setRenameValue(e.target.value)}
                        onBlur={commitRename}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter') commitRename()
                          if (e.key === 'Escape') cancelRename()
                        }}
                        className={cx(
                          'h-6 w-full rounded-md border bg-surface px-1.5 font-mono text-xs text-ink outline-none focus:ring-2',
                          renameTargetError ? 'border-danger focus:ring-danger/15' : 'border-accent focus:ring-accent/15',
                        )}
                      />
                    </div>
                  )
                }
                return (
                  <div key={file.path} className="group/row flex items-center gap-0.5 pr-0.5">
                    <button
                      type="button"
                      onClick={() => setActivePath(file.path)}
                      className={cx(
                        'flex min-w-0 flex-1 cursor-pointer items-center gap-1.5 rounded-md px-1.5 py-1 text-left outline-none transition-colors duration-100',
                        isActive ? 'bg-accent-soft text-accent-ink' : 'text-ink-2 hover:bg-app hover:text-ink',
                      )}
                    >
                      <Icon size={13} className={cx('shrink-0', isActive ? 'text-accent' : 'text-ink-3')} />
                      <span className={cx('min-w-0 flex-1 truncate text-xs', isActive && 'font-medium')}>{file.path}</span>
                    </button>
                    <Tooltip content={entry ? 'SKILL.md is required' : 'Rename'}>
                      <IconButton
                        icon={<Pencil />}
                        label="Rename file"
                        size="sm"
                        disabled={entry}
                        className="opacity-0 transition-opacity duration-100 group-hover/row:opacity-100"
                        onClick={() => startRename(file.path)}
                      />
                    </Tooltip>
                    <Tooltip content={entry ? 'SKILL.md is required' : 'Delete'}>
                      <IconButton
                        icon={<Trash2 />}
                        label="Delete file"
                        size="sm"
                        disabled={entry}
                        className="opacity-0 transition-opacity duration-100 group-hover/row:opacity-100"
                        onClick={() => setDeletingPath(file.path)}
                      />
                    </Tooltip>
                  </div>
                )
              })}
            </div>
          </aside>

          {/* Right: content of the selected file */}
          <div className="flex min-w-0 flex-1 flex-col">
            {active ? (
              <>
                <div className="truncate border-b border-line px-3 py-1.5 font-mono text-2xs text-ink-3">{active.path}</div>
                <div className="p-3">
                  <Textarea
                    key={active.path}
                    mono={!active.path.endsWith('.md')}
                    value={active.content}
                    onChange={(e) => setFileContent(active.path, e.target.value)}
                    onKeyDown={onContentKeyDown}
                    className="min-h-[420px]"
                    placeholder={active.path.endsWith('.md') ? 'Markdown instructions…' : undefined}
                    aria-label={`Content of ${active.path}`}
                  />
                </div>
              </>
            ) : (
              <EmptyState icon={FileText} title="No files" description="Add the first file to this skill." className="py-16" />
            )}
          </div>
        </div>
      </div>

      <p className="mt-2.5 flex items-center gap-1.5 text-xs text-ink-3">
        <Info size={13} className="shrink-0" />
        <span>
          Mounted read-only at <span className="font-mono">/skills/{def.slug}/</span> inside the agent workspace. Tab inserts two spaces.
        </span>
      </p>

      {/* Add file */}
      <Modal
        open={addOpen}
        onClose={() => setAddOpen(false)}
        size="sm"
        title="Add file"
        description={`Relative to /skills/${def.slug}/ — e.g. refs/example.png or lib/util.js.`}
        footer={
          <>
            <Button size="sm" variant="secondary" onClick={() => setAddOpen(false)}>
              Cancel
            </Button>
            <Button
              size="sm"
              variant="primary"
              disabled={!addPath.trim() || addPathError != null}
              onClick={() => addFile(addPath.trim())}
            >
              Add file
            </Button>
          </>
        }
      >
        <Field label="Path" error={addPathError ?? undefined} hint="Folders are implied by the path — no leading or trailing slash.">
          <Input
            autoFocus
            className="font-mono text-xs"
            value={addPath}
            placeholder="lib/util.js"
            onChange={(e) => setAddPath(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && addPath.trim() && !addPathError) addFile(addPath.trim())
            }}
          />
        </Field>
      </Modal>

      {/* Delete file (SKILL.md is protected at the button level) */}
      <ConfirmDialog
        open={deletingPath !== null}
        onClose={() => setDeletingPath(null)}
        title="Delete file?"
        body={`“${deletingPath}” will be removed from the skill draft.`}
        confirmLabel="Delete"
        danger
        onConfirm={() => {
          if (deletingPath) deleteFile(deletingPath)
        }}
      />
    </div>
  )
}
