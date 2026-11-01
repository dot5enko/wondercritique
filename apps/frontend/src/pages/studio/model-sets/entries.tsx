/**
 * Model-set entries editor (plan §3.4): an ordered list of models picked from
 * the enabled catalog, filtered by capability requirements, with per-entry
 * params (reasoning effort from the model's `reasoning_options`, max output
 * tokens). Rendered inside the model-set editor page.
 */
import { ArrowDown, ArrowUp, Eye, Plus, Wrench, X } from 'lucide-react'
import type { LucideIcon, LucideProps } from 'lucide-react'
import { cx } from '../../../ui/cx'
import { Badge, Button, IconButton, Input, Select, Tooltip } from '../../../ui'
import type { SelectOption } from '../../../ui'
import type { CatalogModel, ModelCapabilities, ModelParams, ModelSetEntry } from '../../../api/studio'

/** `provider/model` identity used as the select value across entries. */
export function modelKey(m: Pick<CatalogModel, 'provider_id' | 'model_id'>): string {
  return `${m.provider_id}/${m.model_id}`
}

/** True when the model satisfies the required capability filters. */
export function matchesRequirements(m: CatalogModel, vision: boolean, tools: boolean): boolean {
  return (!vision || m.capabilities.vision) && (!tools || m.capabilities.tools)
}

/**
 * Composite capability icon (Eye = vision, Wrench = tool use). Select options
 * carry a single `LucideIcon`, so this is rendered as one combined glyph.
 */
function CapabilityGlyphs({
  vision,
  tools,
  size = 14,
  className,
}: {
  vision: boolean
  tools: boolean
  size?: string | number
  className?: string
}) {
  return (
    <span className={cx('inline-flex shrink-0 items-center gap-0.5', className)}>
      {vision && <Eye size={size} />}
      {tools && <Wrench size={size} />}
    </span>
  )
}

function capabilityIcon(caps: ModelCapabilities): LucideIcon {
  const Glyphs = (props: LucideProps) => <CapabilityGlyphs vision={caps.vision} tools={caps.tools} {...props} />
  return Glyphs as unknown as LucideIcon
}

export type EntryStatus = 'ok' | 'filtered' | 'unavailable'

interface EntryRowProps {
  index: number
  total: number
  entry: ModelSetEntry
  /** Resolved catalog model, or undefined when unselected / not in the catalog. */
  model: CatalogModel | undefined
  status: EntryStatus
  options: SelectOption[]
  disabled: boolean
  placeholder: string
  onModel: (value: string) => void
  onParam: (patch: Partial<ModelParams>) => void
  onMove: (dir: -1 | 1) => void
  onRemove: () => void
}

function EntryRow({ index, total, entry, model, status, options, disabled, placeholder, onModel, onParam, onMove, onRemove }: EntryRowProps) {
  const value = entry.model_id ? modelKey(entry) : null
  const reasoning = model?.capabilities.reasoning_options ?? []
  const effort = entry.params?.reasoning_effort
  const maxTokens = entry.params?.max_output_tokens

  const effortOptions: SelectOption[] = [{ value: '', label: 'Default' }]
  effortOptions.push(
    ...reasoning.map((o) => ({ value: o, label: o.charAt(0).toUpperCase() + o.slice(1) })),
  )
  if (effort && !reasoning.includes(effort)) effortOptions.push({ value: effort, label: effort })

  return (
    <div className="rounded-lg border border-line bg-app p-2">
      <div className="flex items-center gap-1.5">
        <span className="w-4 shrink-0 text-center font-mono text-2xs text-ink-3">{index + 1}</span>
        <IconButton size="sm" icon={<ArrowUp />} label="Move up" disabled={index === 0} onClick={() => onMove(-1)} />
        <IconButton size="sm" icon={<ArrowDown />} label="Move down" disabled={index === total - 1} onClick={() => onMove(1)} />
        <Select
          options={options}
          value={value}
          onChange={onModel}
          disabled={disabled}
          placeholder={placeholder}
          className="min-w-0 flex-1 [&>button]:w-full"
          ariaLabel={`Model for entry ${index + 1}`}
        />
        {status === 'filtered' && (
          <Tooltip content="Model doesn't satisfy the capability requirements — remove it or relax the filters.">
            <Badge tone="warning">filtered</Badge>
          </Tooltip>
        )}
        {status === 'unavailable' && (
          <Tooltip content="This model is no longer offered by the enabled providers.">
            <Badge tone="warning">unavailable</Badge>
          </Tooltip>
        )}
        <IconButton size="sm" icon={<X />} label="Remove model" onClick={onRemove} />
      </div>

      {value != null && (
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1.5 border-t border-line pt-2">
          {model && reasoning.length > 0 && (
            <>
              <span className="text-2xs text-ink-3">Effort</span>
              <Select
                size="sm"
                className="w-32 [&>button]:w-full"
                options={effortOptions}
                value={effort ?? ''}
                placeholder="Default"
                ariaLabel={`Reasoning effort for entry ${index + 1}`}
                onChange={(v) => onParam(v === '' ? { reasoning_effort: undefined } : { reasoning_effort: v })}
              />
            </>
          )}
          <span className="text-2xs text-ink-3">Max output</span>
          <Input
            size="sm"
            type="number"
            min={1}
            className="w-28"
            value={maxTokens ?? ''}
            placeholder="Unlimited"
            aria-label="Max output tokens"
            onChange={(e) => {
              const n = e.target.value === '' ? undefined : Number(e.target.value)
              onParam({ max_output_tokens: n !== undefined && Number.isFinite(n) && n > 0 ? n : undefined })
            }}
          />
          <span className="text-2xs text-ink-3">tokens</span>
        </div>
      )}
    </div>
  )
}

export interface EntryListProps {
  entries: ModelSetEntry[]
  models: CatalogModel[]
  requireVision: boolean
  requireTools: boolean
  /** False while the catalog request failed / is still loading. */
  catalogReady: boolean
  onChange: (entries: ModelSetEntry[]) => void
}

export function EntryList({ entries, models, requireVision, requireTools, catalogReady, onChange }: EntryListProps) {
  const selectable = catalogReady
    ? models.filter((m) => m.enabled !== false && matchesRequirements(m, requireVision, requireTools))
    : []
  const byKey = new Map(models.map((m) => [modelKey(m), m]))

  const move = (index: number, dir: -1 | 1) => {
    const j = index + dir
    if (j < 0 || j >= entries.length) return
    const next = [...entries]
    const [item] = next.splice(index, 1)
    next.splice(j, 0, item)
    onChange(next)
  }

  const setModel = (index: number, value: string) => {
    const slash = value.indexOf('/')
    const provider_id = value.slice(0, slash)
    const model_id = value.slice(slash + 1)
    const model = byKey.get(value)
    onChange(
      entries.map((e, i) => {
        if (i !== index) return e
        const params: ModelParams = { ...(e.params ?? {}) }
        if (
          params.reasoning_effort !== undefined &&
          (!model || !model.capabilities.reasoning_options.includes(params.reasoning_effort))
        ) {
          delete params.reasoning_effort
        }
        return { ...e, provider_id, model_id, params: Object.keys(params).length > 0 ? params : undefined }
      }),
    )
  }

  const setParam = (index: number, patch: Partial<ModelParams>) => {
    onChange(
      entries.map((e, i) => {
        if (i !== index) return e
        const params: ModelParams = { ...(e.params ?? {}), ...patch }
        if (params.reasoning_effort === undefined) delete params.reasoning_effort
        if (params.max_output_tokens === undefined) delete params.max_output_tokens
        return { ...e, params: Object.keys(params).length > 0 ? params : undefined }
      }),
    )
  }

  return (
    <div className="space-y-2">
      {entries.length === 0 && (
        <p className="rounded-lg border border-dashed border-line px-3 py-4 text-center text-xs text-ink-3">
          No models yet — add the first entry.
        </p>
      )}

      {entries.map((entry, index) => {
        const value = entry.model_id ? modelKey(entry) : null
        const model = value ? byKey.get(value) : undefined
        const status: EntryStatus = !value
          ? 'ok'
          : !catalogReady
            ? 'ok'
            : !model
              ? 'unavailable'
              : matchesRequirements(model, requireVision, requireTools)
                ? 'ok'
                : 'filtered'
        const options: SelectOption[] = selectable.map((m) => ({
          value: modelKey(m),
          label: modelKey(m),
          icon: capabilityIcon(m.capabilities),
        }))
        /* Keep the current selection visible even when filters exclude it. */
        if (value && !options.some((o) => o.value === value)) {
          options.unshift(model ? { value, label: value, icon: capabilityIcon(model.capabilities) } : { value, label: value })
        }
        const loading = !catalogReady && !value
        const noneAvailable = catalogReady && selectable.length === 0 && !value

        return (
          <EntryRow
            key={index}
            index={index}
            total={entries.length}
            entry={entry}
            model={model}
            status={status}
            options={options}
            disabled={loading || noneAvailable}
            placeholder={loading ? 'Loading catalog…' : noneAvailable ? 'No enabled model satisfies these requirements' : 'Select model…'}
            onModel={(v) => setModel(index, v)}
            onParam={(patch) => setParam(index, patch)}
            onMove={(dir) => move(index, dir)}
            onRemove={() => onChange(entries.filter((_, i) => i !== index))}
          />
        )
      })}

      <div>
        <Button
          size="sm"
          variant="secondary"
          icon={<Plus />}
          onClick={() => onChange([...entries, { provider_id: '', model_id: '' }])}
        >
          Add model
        </Button>
      </div>
    </div>
  )
}
