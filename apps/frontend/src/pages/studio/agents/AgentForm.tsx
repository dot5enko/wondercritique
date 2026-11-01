/**
 * Agent editor main form (plan §4.4): identity, model set, system prompt and
 * input template (plain markdown textareas with {{variable}} chips), tools,
 * skills/rules, output contract (artifact / critique / custom fields compiled
 * to a JSON Schema) and limits. Draft state comes from the parent's
 * useDirtyDraft; autosave runs there.
 */
import { useEffect, useMemo, useRef, useState, type SyntheticEvent } from 'react'
import { ChevronRight, Eye, Plus, X } from 'lucide-react'
import {
  Button,
  Callout,
  Card,
  CardBody,
  CardHeader,
  Checkbox,
  Field,
  IconButton,
  Input,
  Segmented,
  Select,
  Skeleton,
  Textarea,
  Tooltip,
} from '../../../ui'
import { Modal } from '../../../components'
import {
  previewAgent,
  type AgentBody,
  type AgentOutputContract,
  type AgentPreview,
  type PromptVariable,
  type StudioCatalog,
  type StudioDef,
} from '../../../api/studio'
import type { DirtyDraft } from '../../../hooks'
import {
  compileFields,
  defaultSampleInput,
  newFieldRow,
  parseFields,
  type FieldRow,
} from './defaults'

export interface AgentFormProps {
  def: StudioDef<'agent'>
  catalog: StudioCatalog
  dd: DirtyDraft<AgentBody>
  /** Saves the draft immediately — used before previewing so the assembled prompt matches the screen. */
  flushDraft: () => Promise<void>
}

type PromptField = 'system_prompt' | 'input_template'

const FIELD_TYPE_OPTIONS = [
  { value: 'string', label: 'string' },
  { value: 'number', label: 'number' },
  { value: 'boolean', label: 'boolean' },
  { value: 'enum', label: 'enum' },
]

const SUMMARY_CLASS =
  'flex cursor-pointer list-none select-none items-center gap-1.5 outline-none [&::-webkit-details-marker]:hidden'

function toggleIn(list: string[], id: string): string[] {
  return list.includes(id) ? list.filter((x) => x !== id) : [...list, id]
}

/* ---------- prompt variable chips (insert at caret) ---------- */

function PromptChips({ variables, onInsert }: { variables: PromptVariable[]; onInsert: (name: string) => void }) {
  if (variables.length === 0) return null
  return (
    <div className="flex flex-wrap gap-1">
      {variables.map((v) => (
        <Tooltip key={v.name} content={v.description || `Insert {{${v.name}}}`} side="bottom">
          <button
            type="button"
            onClick={() => onInsert(v.name)}
            className="cursor-pointer rounded border border-line bg-app px-1.5 py-0.5 font-mono text-2xs text-ink-2 outline-none transition-colors duration-100 hover:border-line-strong hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/40"
          >
            {`{{${v.name}}}`}
          </button>
        </Tooltip>
      ))}
    </div>
  )
}

/* ---------- custom output contract: one field row ---------- */

function FieldRowEditor({
  row,
  onChange,
  onRemove,
}: {
  row: FieldRow
  onChange: (patch: Partial<Omit<FieldRow, 'id'>>) => void
  onRemove: () => void
}) {
  return (
    <div className="rounded-md border border-line bg-app p-2">
      <div className="flex items-center gap-2">
        <Input
          placeholder="field_name"
          className="min-w-0 font-mono text-xs"
          value={row.name}
          onChange={(e) => onChange({ name: e.target.value })}
        />
        <Select
          size="sm"
          className="w-28 shrink-0"
          ariaLabel="Field type"
          options={FIELD_TYPE_OPTIONS}
          value={row.type}
          onChange={(type) => onChange({ type: type as FieldRow['type'] })}
        />
        <Checkbox
          className="shrink-0 text-xs"
          label="Required"
          checked={row.required}
          onChange={(e) => onChange({ required: e.target.checked })}
        />
        <IconButton
          size="sm"
          icon={<X />}
          label="Remove field"
          className="shrink-0 text-ink-3 hover:text-danger"
          onClick={onRemove}
        />
      </div>
      <Input
        placeholder="Description shown to the model"
        className="mt-1.5"
        value={row.description}
        onChange={(e) => onChange({ description: e.target.value })}
      />
      {row.type === 'enum' && (
        <Input
          placeholder="value one, value two, …"
          className="mt-1.5 font-mono text-xs"
          value={row.enumValues}
          onChange={(e) => onChange({ enumValues: e.target.value })}
        />
      )}
    </div>
  )
}

/* ---------- "Show assembled prompt" modal ---------- */

function PreviewModal({
  open,
  onClose,
  def,
  catalog,
  dirty,
  flushDraft,
}: {
  open: boolean
  onClose: () => void
  def: StudioDef<'agent'>
  catalog: StudioCatalog
  dirty: boolean
  flushDraft: () => Promise<void>
}) {
  const sample = useMemo(() => defaultSampleInput(catalog), [catalog])
  const [preview, setPreview] = useState<AgentPreview | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>(null)

  useEffect(() => {
    if (!open) return
    const ctrl = new AbortController()
    let alive = true
    const load = async (): Promise<void> => {
      setLoading(true)
      setError(null)
      setPreview(null)
      try {
        if (dirty) await flushDraft()
        if (!alive || ctrl.signal.aborted) return
        const result = await previewAgent(def.id, { input: sample }, ctrl.signal)
        if (!alive || ctrl.signal.aborted) return
        setPreview(result)
      } catch (err) {
        if (!alive || ctrl.signal.aborted) return
        setError(err)
      } finally {
        if (alive && !ctrl.signal.aborted) setLoading(false)
      }
    }
    void load()
    return () => {
      alive = false
      ctrl.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, def.id, sample, dirty])

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title="Assembled prompt"
      description="Sample values fill the prompt variables; the agent's saved draft is used."
      footer={
        <Button size="sm" variant="secondary" onClick={onClose}>
          Close
        </Button>
      }
    >
      {loading ? (
        <div className="space-y-2" aria-busy="true">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-40" />
          <Skeleton className="h-4 w-16" />
          <Skeleton className="h-24" />
        </div>
      ) : error != null ? (
        <Callout tone="danger" title="Preview failed">
          {error instanceof Error ? error.message : 'Something went wrong.'}
        </Callout>
      ) : preview ? (
        <div className="space-y-3">
          <div>
            <p className="mb-1 text-2xs font-medium uppercase tracking-wide text-ink-3">System</p>
            <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md border border-line bg-sunken p-2.5 font-mono text-2xs leading-relaxed text-ink">
              {preview.system}
            </pre>
          </div>
          <div>
            <p className="mb-1 text-2xs font-medium uppercase tracking-wide text-ink-3">Input</p>
            <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-md border border-line bg-sunken p-2.5 font-mono text-2xs leading-relaxed text-ink">
              {preview.input}
            </pre>
          </div>
        </div>
      ) : null}
    </Modal>
  )
}

/* ---------- main form ---------- */

export function AgentForm({ def, catalog, dd, flushDraft }: AgentFormProps) {
  const { draft, setDraft, dirty } = dd
  const [previewOpen, setPreviewOpen] = useState(false)

  /* --- prompt variable insertion at the caret --- */
  const systemRef = useRef<HTMLTextAreaElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const caret = useRef<{ field: PromptField; start: number; end: number; known: boolean }>({
    field: 'system_prompt',
    start: 0,
    end: 0,
    known: false,
  })
  const [pendingCaret, setPendingCaret] = useState<{ field: PromptField; pos: number } | null>(null)

  const noteCaret = (field: PromptField) => (e: SyntheticEvent<HTMLTextAreaElement>) => {
    const el = e.currentTarget
    caret.current = { field, start: el.selectionStart ?? 0, end: el.selectionEnd ?? 0, known: true }
  }

  useEffect(() => {
    if (!pendingCaret) return
    const el = pendingCaret.field === 'system_prompt' ? systemRef.current : inputRef.current
    if (el) {
      el.focus()
      el.setSelectionRange(pendingCaret.pos, pendingCaret.pos)
    }
    setPendingCaret(null)
  }, [pendingCaret])

  const insertVariable = (name: string): void => {
    const field = caret.current.known ? caret.current.field : 'system_prompt'
    const value = field === 'system_prompt' ? draft.system_prompt : draft.input_template
    // No focused/caret state yet → append at the end of the system prompt.
    const start = caret.current.known ? Math.min(caret.current.start, value.length) : value.length
    const end = caret.current.known ? Math.min(caret.current.end, value.length) : value.length
    const snippet = `{{${name}}}`
    const nextValue = value.slice(0, start) + snippet + value.slice(end)
    setDraft((d) =>
      field === 'system_prompt' ? { ...d, system_prompt: nextValue } : { ...d, input_template: nextValue },
    )
    setPendingCaret({ field, pos: start + snippet.length })
  }

  /* --- custom output contract: field rows ⇄ compiled schema --- */
  const [rows, setRows] = useState<FieldRow[]>(() => parseFields(dd.draft.output_contract.fields_schema))
  const compiledRef = useRef<string | null>(
    dd.draft.output_contract.fields_schema ? JSON.stringify(dd.draft.output_contract.fields_schema) : null,
  )

  // Adopt externally replaced drafts (restore-as-draft) back into the rows editor.
  useEffect(() => {
    const schema = draft.output_contract.fields_schema
    if (!schema) return
    const json = JSON.stringify(schema)
    if (json === compiledRef.current) return
    compiledRef.current = json
    setRows(parseFields(schema))
  }, [draft.output_contract])

  const commitRows = (next: FieldRow[]): void => {
    setRows(next)
    const schema = compileFields(next)
    compiledRef.current = JSON.stringify(schema)
    setDraft((d) => ({ ...d, output_contract: { type: 'custom', fields_schema: schema } }))
  }

  const setContractType = (type: AgentOutputContract['type']): void => {
    if (type === 'custom') {
      commitRows(rows) // (re)pushes the current rows into the draft schema
      return
    }
    setDraft((d) => ({ ...d, output_contract: { type } }))
  }

  const compiledJson = useMemo(
    () => JSON.stringify(draft.output_contract.fields_schema ?? { type: 'object', properties: {} }, null, 2),
    [draft.output_contract],
  )

  /* --- limits --- */
  const setLimit = (key: 'max_steps' | 'max_iterations', raw: string): void => {
    const value = raw === '' ? undefined : Number(raw)
    setDraft((d) => {
      const limits = { ...d.limits }
      if (value !== undefined && Number.isFinite(value) && value > 0) limits[key] = value
      else delete limits[key]
      return { ...d, limits }
    })
  }

  const selectedSet = catalog.model_sets.find((r) => r.id === draft.model_set_id)

  return (
    <div className="space-y-4">
      {/* Identity */}
      <Card>
        <CardHeader title="Identity" description="Fixed at creation — the slug is the agent's API identity." />
        <CardBody className="space-y-3">
          <Field label="Name">
            <Input value={def.name} readOnly />
          </Field>
          <Field label="Description" optional>
            <Input value={def.description} readOnly />
          </Field>
          <Field label="Slug" hint="Used in API paths, e.g. /api/v1/agents/{slug}/runs.">
            <Input value={def.slug} readOnly className="font-mono text-xs" />
          </Field>
        </CardBody>
      </Card>

      {/* Model set */}
      <Card>
        <CardHeader title="Model set" description="Pool of models this agent runs on (see Model sets)." />
        <CardBody className="space-y-1.5">
          <Select
            className="w-full max-w-sm"
            ariaLabel="Model set"
            placeholder="Choose a model set…"
            options={[
              { value: '', label: 'None' },
              ...catalog.model_sets.map((set) => ({ value: set.id, label: set.name, hint: set.slug })),
            ]}
            value={draft.model_set_id}
            onChange={(model_set_id) => setDraft((d) => ({ ...d, model_set_id }))}
          />
          {selectedSet && (
            <p className="text-xs text-ink-3">
              Slug <span className="font-mono text-ink-2">{selectedSet.slug}</span>
            </p>
          )}
          {draft.model_set_id && !selectedSet && (
            <p className="text-xs text-warning">The selected model set is no longer in the catalog.</p>
          )}
          {catalog.model_sets.length === 0 && (
            <p className="text-xs text-ink-3">No model sets yet — create one under Model sets.</p>
          )}
        </CardBody>
      </Card>

      {/* Prompts */}
      <Card>
        <CardHeader title="Prompts" description="Plain markdown, assembled by the engine at run time." />
        <CardBody className="space-y-2">
          <Field label="System prompt">
            <Textarea
              ref={systemRef}
              mono
              rows={14}
              className="min-h-80"
              spellCheck={false}
              value={draft.system_prompt}
              onChange={(e) => {
                setDraft((d) => ({ ...d, system_prompt: e.target.value }))
                noteCaret('system_prompt')(e)
              }}
              onFocus={noteCaret('system_prompt')}
              onSelect={noteCaret('system_prompt')}
            />
          </Field>
          <PromptChips variables={catalog.prompt_variables} onInsert={insertVariable} />
          <p className="text-xs text-ink-3">
            The engine appends <span className="font-mono">## Rules</span>,{' '}
            <span className="font-mono">## Skills</span> and <span className="font-mono">## Output</span> sections
            automatically.
          </p>

          <div className="border-t border-line pt-3">
            <Field label="Input template">
              <Textarea
                ref={inputRef}
                mono
                rows={6}
                className="min-h-35"
                spellCheck={false}
                value={draft.input_template}
                onChange={(e) => {
                  setDraft((d) => ({ ...d, input_template: e.target.value }))
                  noteCaret('input_template')(e)
                }}
                onFocus={noteCaret('input_template')}
                onSelect={noteCaret('input_template')}
              />
            </Field>
            <div className="mt-1.5 space-y-1.5">
              <PromptChips variables={catalog.prompt_variables} onInsert={insertVariable} />
              <p className="text-xs text-ink-3">
                Optional <span className="font-mono">{'{{port_name}}'}</span> placeholders control where inputs land;
                inputs also arrive as markdown sections (<span className="font-mono">## Brief</span>,{' '}
                <span className="font-mono">## Previous critique</span>, <span className="font-mono">## Current file</span>)
                + images.
              </p>
            </div>
          </div>
        </CardBody>
      </Card>

      {/* Tools */}
      <Card>
        <CardHeader title="Tools" description="Allowlist of tools the agent may call — all-off is valid." />
        <CardBody>
          {catalog.tools.length === 0 ? (
            <p className="text-xs text-ink-3">No tools are registered in the catalog.</p>
          ) : (
            <div className="grid gap-x-6 gap-y-2 sm:grid-cols-2">
              {catalog.tools.map((tool) => (
                <Checkbox
                  key={tool.name}
                  className="min-w-0 items-start"
                  checked={draft.tools.includes(tool.name)}
                  onChange={() => setDraft((d) => ({ ...d, tools: toggleIn(d.tools, tool.name) }))}
                  label={
                    <span className="flex min-w-0 flex-col gap-px">
                      <span className="font-mono text-xs text-ink">{tool.name}</span>
                      <Tooltip content={tool.description || tool.name} side="right" className="min-w-0">
                        <span className="block truncate text-xs text-ink-3">{tool.description || '—'}</span>
                      </Tooltip>
                    </span>
                  }
                />
              ))}
            </div>
          )}
        </CardBody>
      </Card>

      {/* Skills & rules */}
      <Card>
        <CardHeader title="Skills & rules" description="Attached references the engine appends to the prompt." />
        <CardBody>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="min-w-0">
              <p className="mb-1.5 text-xs font-medium text-ink-2">Skills</p>
              {catalog.skills.length === 0 ? (
                <p className="text-xs text-ink-3">No skills in the catalog.</p>
              ) : (
                <div className="grid gap-1">
                  {catalog.skills.map((skill) => (
                    <Checkbox
                      key={skill.id}
                      label={
                        <span className="min-w-0 truncate">
                          {skill.name} <span className="font-mono text-2xs text-ink-3">{skill.slug}</span>
                        </span>
                      }
                      checked={draft.skill_ids.includes(skill.id)}
                      onChange={() => setDraft((d) => ({ ...d, skill_ids: toggleIn(d.skill_ids, skill.id) }))}
                    />
                  ))}
                </div>
              )}
            </div>
            <div className="min-w-0">
              <p className="mb-1.5 text-xs font-medium text-ink-2">Rules</p>
              {catalog.rules.length === 0 ? (
                <p className="text-xs text-ink-3">No rule sets in the catalog.</p>
              ) : (
                <div className="grid gap-1">
                  {catalog.rules.map((rule) => (
                    <Checkbox
                      key={rule.id}
                      label={
                        <span className="min-w-0 truncate">
                          {rule.name} <span className="font-mono text-2xs text-ink-3">{rule.slug}</span>
                        </span>
                      }
                      checked={draft.rule_ids.includes(rule.id)}
                      onChange={() => setDraft((d) => ({ ...d, rule_ids: toggleIn(d.rule_ids, rule.id) }))}
                    />
                  ))}
                </div>
              )}
            </div>
          </div>
        </CardBody>
      </Card>

      {/* Output contract */}
      <Card>
        <CardHeader title="Output contract" description="What the agent must submit when it finishes." />
        <CardBody className="space-y-3">
          <Segmented
            options={[
              { value: 'artifact', label: 'Artifact' },
              { value: 'critique', label: 'Critique' },
              { value: 'custom', label: 'Custom' },
            ]}
            value={draft.output_contract.type}
            onChange={setContractType}
          />
          {draft.output_contract.type === 'custom' && (
            <>
              {rows.length > 0 && (
                <div className="space-y-2">
                  {rows.map((row) => (
                    <FieldRowEditor
                      key={row.id}
                      row={row}
                      onChange={(patch) => commitRows(rows.map((r) => (r.id === row.id ? { ...r, ...patch } : r)))}
                      onRemove={() => commitRows(rows.filter((r) => r.id !== row.id))}
                    />
                  ))}
                </div>
              )}
              <div>
                <Button size="sm" variant="ghost" icon={<Plus />} onClick={() => commitRows([...rows, newFieldRow()])}>
                  Add field
                </Button>
              </div>
              <details className="group rounded-md border border-line bg-app">
                <summary className={`${SUMMARY_CLASS} px-2.5 py-2 text-xs text-ink-2 transition-colors duration-100 hover:text-ink`}>
                  <ChevronRight size={12} className="shrink-0 transition-transform duration-150 group-open:rotate-90" />
                  Compiled JSON Schema
                </summary>
                <pre className="overflow-x-auto border-t border-line px-2.5 py-2 font-mono text-2xs leading-relaxed text-ink-2">
                  {compiledJson}
                </pre>
              </details>
            </>
          )}
        </CardBody>
      </Card>

      {/* Limits */}
      <Card>
        <CardHeader title="Limits" description="Guardrails for the agent's run loop." />
        <CardBody className="space-y-1.5">
          <div className="flex flex-wrap gap-3">
            <Field label="Max steps" optional className="w-36">
              <Input
                type="number"
                min={1}
                step={1}
                value={draft.limits.max_steps ?? ''}
                onChange={(e) => setLimit('max_steps', e.target.value)}
              />
            </Field>
            <Field label="Max iterations" optional className="w-40">
              <Input
                type="number"
                min={1}
                step={1}
                value={draft.limits.max_iterations ?? ''}
                onChange={(e) => setLimit('max_iterations', e.target.value)}
              />
            </Field>
          </div>
          <p className="text-xs text-ink-3">Engine defaults apply when empty.</p>
        </CardBody>
      </Card>

      {/* Actions */}
      <div className="flex items-center gap-2">
        <Button variant="secondary" icon={<Eye />} onClick={() => setPreviewOpen(true)}>
          Show assembled prompt
        </Button>
      </div>

      <PreviewModal
        open={previewOpen}
        onClose={() => setPreviewOpen(false)}
        def={def}
        catalog={catalog}
        dirty={dirty}
        flushDraft={flushDraft}
      />
    </div>
  )
}
