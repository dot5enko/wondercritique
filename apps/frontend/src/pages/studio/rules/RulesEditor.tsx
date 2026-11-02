/**
 * Rules editor (plan §4.6) — PageHeader + VersionBar over stacked group cards.
 * Each group holds rule rows: severity select, auto-growing text textarea and
 * an optional component-validator code (catalog list or free text).
 */
import { useLayoutEffect, useRef, useState, type TextareaHTMLAttributes } from 'react'
import { CircleX, Info, Plus, Trash2, TriangleAlert, X } from 'lucide-react'
import { Card, IconButton, Input, Select, Textarea, Tooltip, type SelectOption } from '../../../ui'
import { ConfirmDialog, Loadable, PageHeader, VersionBar } from '../../../components'
import { useAsync, useDirtyDraft } from '../../../hooks'
import { getCatalog, getEditableDef, putDraft, type RuleBody, type RuleGroup, type RuleItem, type StudioDef } from '../../../api/studio'
import { emptyRuleBody } from './body'

/** Sentinel option values inside the validator-code select. */
const NONE = '__none__'
const CUSTOM = '__custom__'

/* RuleItem.severity is a plain string; these are the canonical values. */
const SEVERITY_OPTIONS: SelectOption[] = [
  { value: 'info', label: 'Info', icon: Info },
  { value: 'warning', label: 'Warning', icon: TriangleAlert },
  { value: 'error', label: 'Error', icon: CircleX },
]

export default function RulesEditor({ id }: { id: string }) {
  const def = useAsync((signal) => getEditableDef<'rule'>('rule', id, signal), [id])
  return (
    <Loadable
      loading={def.loading && !def.data}
      error={def.data ? null : def.error}
      onRetry={def.reload}
      className="p-6"
    >
      {def.data && <RulesEditorBody def={def.data} refreshDef={def.reload} />}
    </Loadable>
  )
}

function RulesEditorBody({ def, refreshDef }: { def: StudioDef<'rule'>; refreshDef: () => void }) {
  const d = useDirtyDraft<RuleBody>(def.draft ?? emptyRuleBody(), {
    autosave: (body) => putDraft('rule', def.id, body),
    onSaved: refreshDef,
  })
  const groups = d.draft.groups

  const catalog = useAsync((signal) => getCatalog(signal), [])
  const validatorCodes = catalog.data?.validator_rule_codes ?? []

  const [removeGroupIdx, setRemoveGroupIdx] = useState<number | null>(null)
  const removing = removeGroupIdx != null ? groups[removeGroupIdx] : undefined

  /* ---------- group/rule mutations (all flow through setDraft → autosave) ---------- */

  const setGroups = (next: RuleGroup[]): void => d.setDraft((prev) => ({ ...prev, groups: next }))

  const renameGroup = (gi: number, name: string): void =>
    setGroups(groups.map((g, i) => (i === gi ? { ...g, name } : g)))

  const addGroup = (): void => setGroups([...groups, { name: '', rules: [] }])

  const removeGroup = (gi: number): void => {
    setGroups(groups.filter((_, i) => i !== gi))
    setRemoveGroupIdx(null)
  }

  const updateRule = (gi: number, ri: number, rule: RuleItem): void =>
    setGroups(groups.map((g, i) => (i === gi ? { ...g, rules: g.rules.map((r, j) => (j === ri ? rule : r)) } : g)))

  const addRule = (gi: number): void =>
    setGroups(groups.map((g, i) => (i === gi ? { ...g, rules: [...g.rules, { text: '', severity: 'warning' }] } : g)))

  const removeRule = (gi: number, ri: number): void =>
    setGroups(groups.map((g, i) => (i === gi ? { ...g, rules: g.rules.filter((_, j) => j !== ri) } : g)))

  return (
    <div>
      <PageHeader back={{ to: '/studio/rules', label: 'Rules' }} title={def.name} description={def.description || undefined} />

      <div className="mt-4 overflow-hidden rounded-lg border border-line bg-surface">
        <VersionBar
          kind="rule"
          def={def}
          draft={d.draft}
          dirty={d.dirty}
          onDraftLoaded={(body) => d.reset(body as RuleBody)}
          onSaved={refreshDef}
        />
        {d.saveState === 'error' && (
          <div className="flex items-center gap-1.5 border-b border-line bg-danger-soft px-4 py-1.5 text-xs text-danger">
            <TriangleAlert size={13} className="shrink-0" />
            Autosave failed — your edits are kept. Use “Save draft” to retry.
          </div>
        )}

        <div className="space-y-3 p-4">
          {groups.map((group, gi) => (
            <Card key={gi}>
              <div className="flex items-center gap-2 border-b border-line px-3 py-2">
                <Input
                  variant="filled"
                  value={group.name}
                  onChange={(e) => renameGroup(gi, e.target.value)}
                  placeholder="Group name"
                  aria-label="Group name"
                  className="flex-1"
                />
                <Tooltip content={groups.length === 1 ? 'Keep at least one group' : 'Remove group'}>
                  <IconButton
                    icon={<Trash2 />}
                    label="Remove group"
                    size="sm"
                    disabled={groups.length === 1}
                    onClick={() => setRemoveGroupIdx(gi)}
                  />
                </Tooltip>
              </div>
              <div className="space-y-2 p-3">
                {group.rules.map((rule, ri) => (
                  <RuleRow
                    key={ri}
                    rule={rule}
                    validatorCodes={validatorCodes}
                    onChange={(next) => updateRule(gi, ri, next)}
                    onRemove={() => removeRule(gi, ri)}
                  />
                ))}
                <button
                  type="button"
                  onClick={() => addRule(gi)}
                  className="flex w-full cursor-pointer items-center justify-center gap-1.5 rounded-md border border-dashed border-line py-1.5 text-xs text-ink-3 outline-none transition-colors duration-100 hover:border-line-strong hover:text-ink-2 focus-visible:ring-2 focus-visible:ring-accent/40"
                >
                  <Plus size={13} />
                  Add rule
                </button>
              </div>
            </Card>
          ))}

          <button
            type="button"
            onClick={addGroup}
            className="flex w-full cursor-pointer items-center justify-center gap-1.5 rounded-lg border border-dashed border-line bg-surface py-2.5 text-xs text-ink-3 outline-none transition-colors duration-100 hover:border-line-strong hover:text-ink-2 focus-visible:ring-2 focus-visible:ring-accent/40"
          >
            <Plus size={13} />
            Add group
          </button>
        </div>
      </div>

      <p className="mt-2.5 flex items-center gap-1.5 text-xs text-ink-3">
        <Info size={13} className="shrink-0" />
        <span>
          Rules are appended to the agent's prompt as a “Rules” section; validator codes feed the component validator.
        </span>
      </p>

      {/* Remove group (blocked while it is the only one) */}
      <ConfirmDialog
        open={removing !== undefined}
        onClose={() => setRemoveGroupIdx(null)}
        title="Remove group?"
        body={`“${removing?.name || 'Unnamed group'}” and its ${removing?.rules.length ?? 0} rule${
          removing?.rules.length === 1 ? '' : 's'
        } will be removed from the draft.`}
        confirmLabel="Remove"
        danger
        onConfirm={() => {
          if (removeGroupIdx != null) removeGroup(removeGroupIdx)
        }}
      />
    </div>
  )
}

/** One rule row: severity · auto-growing text · optional validator code. */
function RuleRow({
  rule,
  validatorCodes,
  onChange,
  onRemove,
}: {
  rule: RuleItem
  validatorCodes: string[]
  onChange: (rule: RuleItem) => void
  onRemove: () => void
}) {
  const code = rule.validator_code ?? ''
  /* A code outside the catalog (typed before the catalog loaded, or restored) edits as free text. */
  const [customMode, setCustomMode] = useState(code !== '' && !validatorCodes.includes(code))
  const showCustom = customMode || (code !== '' && !validatorCodes.includes(code))

  return (
    <div className="flex items-start gap-2">
      <Select
        size="sm"
        className="w-24 shrink-0"
        ariaLabel="Severity"
        options={SEVERITY_OPTIONS}
        value={rule.severity}
        onChange={(severity) => onChange({ ...rule, severity })}
      />
      <AutoTextarea
        value={rule.text}
        onChange={(text) => onChange({ ...rule, text })}
        placeholder="Rule text…"
        aria-label="Rule text"
        className="flex-1"
      />
      <div className="w-52 shrink-0">
        <Select
          size="sm"
          className="w-full"
          ariaLabel="Validator code"
          placeholder="Validator…"
          options={[
            { value: NONE, label: 'None' },
            ...validatorCodes.map((c) => ({ value: c, label: c })),
            { value: CUSTOM, label: 'Custom…' },
          ]}
          value={showCustom ? CUSTOM : code === '' ? NONE : code}
          onChange={(v) => {
            if (v === CUSTOM) {
              setCustomMode(true)
              return
            }
            setCustomMode(false)
            onChange({ ...rule, validator_code: v === NONE ? undefined : v })
          }}
        />
        {showCustom && (
          <Input
            size="sm"
            className="mt-1.5 font-mono"
            autoFocus
            value={code}
            placeholder="custom_rule_code"
            aria-label="Custom validator code"
            onChange={(e) => {
              const v = e.target.value
              onChange({ ...rule, validator_code: v.trim() === '' ? undefined : v })
            }}
          />
        )}
      </div>
      <IconButton icon={<X />} label="Remove rule" size="sm" className="mt-0.5 shrink-0" onClick={onRemove} />
    </div>
  )
}

/** Textarea that grows with its content (rows=2 floor). */
function AutoTextarea({
  value,
  onChange,
  ...rest
}: {
  value: string
  onChange: (value: string) => void
} & Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, 'value' | 'onChange' | 'ref'>) {
  const ref = useRef<HTMLTextAreaElement>(null)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${el.scrollHeight}px`
  }, [value])
  return <Textarea ref={ref} rows={2} value={value} onChange={(e) => onChange(e.target.value)} {...rest} />
}
