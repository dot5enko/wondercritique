/**
 * JSON-Schema-driven form (2020-12 subset) rendered with the ui/Form
 * primitives: objects → field stacks, strings → Input/Textarea/Select
 * (enum/enumNames), numbers → number Input, booleans → Switch, arrays of
 * scalars/objects → reorderable rows, oneOf/anyOf → variant Select.
 *
 * Honors `ui:*` hints when present: `ui:widget` (textarea/select/segmented/
 * radio/checkbox/switch/password/hidden), `ui:placeholder`, `ui:section`,
 * `ui:order`, `ui:multiline`, `ui:rows`, `ui:mono`, `ui:step`.
 *
 * The value is kept loose (`unknown`) with safe internal typing — missing or
 * malformed schema parts degrade to "no fields" instead of crashing.
 * `emptyValueFor(schema)` seeds a fresh value for a schema.
 */
import { useEffect, useId, useState, type ReactNode } from 'react'
import { ChevronDown, ChevronUp, Plus, X } from 'lucide-react'
import { cx } from '../ui/cx'
import { Button, IconButton } from '../ui/Button'
import { Checkbox, Field, Input, RadioGroup, Segmented, Switch, Textarea } from '../ui/Form'
import { Select } from '../ui/Select'
import type { JSONSchema } from '../api/studio'

export interface SchemaFormProps {
  schema: JSONSchema | undefined
  value: unknown
  onChange: (value: unknown) => void
  disabled?: boolean
  /** Base for generated control ids (defaults to an auto id). */
  idPrefix?: string
}

/* ---------- small safe accessors ---------- */

function isObj(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function str(v: unknown): string | undefined {
  return typeof v === 'string' && v.length > 0 ? v : undefined
}

function num(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) ? v : undefined
}

function bool(v: unknown): boolean {
  return v === true
}

function asJSONSchema(v: unknown): JSONSchema | null {
  return isObj(v) ? (v as JSONSchema) : null
}

/** Effective type of a schema; array-typed `type` drops `null`, falls back to shape. */
function effType(s: JSONSchema): string | undefined {
  const t: unknown = s.type
  if (typeof t === 'string') return t
  if (Array.isArray(t)) {
    const names = t.filter((x): x is string => typeof x === 'string')
    return names.find((n) => n !== 'null') ?? names[0]
  }
  if (s.properties != null || s.additionalProperties === false) return 'object'
  if (s.items != null) return 'array'
  return undefined
}

/** oneOf/anyOf branches validated as objects. */
function branchesOf(s: JSONSchema): JSONSchema[] {
  const raw = s.oneOf ?? s.anyOf
  if (!Array.isArray(raw)) return []
  return raw.map(asJSONSchema).filter((b): b is JSONSchema => b !== null)
}

function prettifyKey(key: string): string {
  return key
    .replace(/[_-]+/g, ' ')
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/(^|\s)\w/g, (c) => c.toUpperCase())
    .trim()
}

function displayRaw(v: unknown): string {
  if (typeof v === 'string') return v
  if (v === null) return 'None'
  if (typeof v === 'object') {
    try {
      return JSON.stringify(v)
    } catch {
      return String(v)
    }
  }
  return String(v)
}

function cloneDefault(v: unknown): unknown {
  try {
    return JSON.parse(JSON.stringify(v ?? null))
  } catch {
    return null
  }
}

/** Formats that render as a multi-line textarea. */
const MULTILINE_FORMATS = new Set(['markdown', 'textarea', 'multiline', 'longtext'])

/* ---------- emptyValueFor: seed a value for a schema ---------- */

export function emptyValueFor(schema: JSONSchema | undefined): unknown {
  const s = asJSONSchema(schema)
  if (!s) return null
  if (s.default !== undefined) return cloneDefault(s.default)

  const branches = branchesOf(s)
  if (branches.length > 0) return emptyValueFor(branches[0])

  const enumRaw = Array.isArray(s.enum) ? s.enum : null
  if (enumRaw && enumRaw.length > 0) return cloneDefault(enumRaw[0])

  switch (effType(s)) {
    case 'object': {
      const out: Record<string, unknown> = {}
      const props = isObj(s.properties) ? s.properties : {}
      for (const [key, prop] of Object.entries(props)) {
        const v = emptyValueFor(asJSONSchema(prop) ?? undefined)
        if (v !== undefined && v !== null) out[key] = v
      }
      return out
    }
    case 'array':
      return []
    case 'string':
      return ''
    case 'boolean':
      return false
    case 'number':
    case 'integer':
      return null
    default:
      return null
  }
}

/* ---------- node plumbing ---------- */

interface NodeCtx {
  disabled: boolean
  id: string
  depth: number
}

interface SchemaNodeProps {
  schema: JSONSchema | null
  value: unknown
  onChange: (value: unknown) => void
  required: boolean
  ctx: NodeCtx
}

function isContainer(s: JSONSchema | null): boolean {
  if (!s) return false
  if (branchesOf(s).length > 1) return true
  if (Array.isArray(s.enum)) return false
  const t = effType(s)
  return t === 'object' || t === 'array'
}

function SchemaNode({ schema, value, onChange, required, ctx }: SchemaNodeProps) {
  const s = schema
  if (!s) return null
  if (str(s['ui:widget']) === 'hidden') return null

  const branches = branchesOf(s)
  if (branches.length > 1) {
    return <VariantNode schema={s} branches={branches} value={value} onChange={onChange} required={required} ctx={ctx} />
  }
  if (branches.length === 1) {
    return <SchemaNode schema={branches[0]} value={value} onChange={onChange} required={required} ctx={ctx} />
  }

  const enumRaw = Array.isArray(s.enum) ? s.enum : null
  if (enumRaw && enumRaw.length > 0) {
    return <EnumNode schema={s} raws={enumRaw} value={value} onChange={onChange} disabled={ctx.disabled} />
  }

  switch (effType(s)) {
    case 'object':
      return <ObjectFields schema={s} value={value} onChange={onChange} required={required} ctx={ctx} />
    case 'array':
      return <ArrayEditor schema={s} value={value} onChange={onChange} required={required} ctx={ctx} />
    case 'boolean':
      return <BooleanNode schema={s} value={value} onChange={onChange} required={required} ctx={ctx} />
    case 'number':
    case 'integer':
      return <NumberNode schema={s} value={value} onChange={onChange} ctx={ctx} />
    case 'string':
    case undefined: {
      const password = str(s.format) === 'password' || str(s['ui:widget']) === 'password'
      return <StringNode schema={s} value={value} onChange={onChange} ctx={ctx} password={password} />
    }
    default:
      return <p className="text-xs text-ink-3">Unsupported schema type.</p>
  }
}

/* ---------- object ---------- */

function ObjectFields({
  schema,
  value,
  onChange,
  ctx,
}: SchemaNodeProps & { schema: JSONSchema }) {
  const obj = isObj(value) ? value : {}
  const props = isObj(schema.properties) ? (schema.properties as Record<string, unknown>) : {}
  const entries = Object.entries(props)

  if (entries.length === 0) {
    return ctx.depth === 0 ? null : <p className="text-xs text-ink-3">No fields.</p>
  }

  const orderRaw = schema['ui:order']
  const orderNames = Array.isArray(orderRaw)
    ? orderRaw.filter((n): n is string => typeof n === 'string')
    : []
  const names = [
    ...orderNames.filter((n) => entries.some(([k]) => k === n)),
    ...entries.map(([k]) => k).filter((k) => !orderNames.includes(k)),
  ]
  const requiredRaw = schema.required
  const requiredKeys = Array.isArray(requiredRaw)
    ? requiredRaw.filter((r): r is string => typeof r === 'string')
    : []

  const isEmptyValue = (v: unknown) => v === undefined || v === null || v === ''

  const fields = names.map((key) => {
    const def = asJSONSchema(props[key])
    if (!def) return null
    const propValue = obj[key]
    const isRequired = requiredKeys.includes(key)
    const label = str(def.title) ?? prettifyKey(key)
    const hint = str(def.description)
    const container = isContainer(def)

    const node = (
      <SchemaNode
        schema={def}
        value={propValue}
        onChange={(v) => onChange({ ...obj, [key]: v })}
        required={isRequired}
        ctx={{ disabled: ctx.disabled, id: `${ctx.id}-${key}`, depth: ctx.depth + 1 }}
      />
    )

    return (
      <Field
        key={key}
        label={label}
        hint={hint}
        optional={!isRequired && !container}
        error={isRequired && isEmptyValue(propValue) ? 'Required' : undefined}
      >
        {node}
      </Field>
    )
  })

  return <Group schema={schema} depth={ctx.depth}>{fields}</Group>
}

/** Wraps a group of fields; nested groups get a hairline box + title. */
function Group({ schema, depth, children }: { schema: JSONSchema; depth: number; children: ReactNode }) {
  const body = <div className="flex flex-col gap-3">{children}</div>
  if (depth === 0) return body
  const title = str(schema['ui:section'])
  return (
    <div className="rounded-lg border border-line bg-app/40 p-3">
      {title && <p className="mb-2.5 text-xs font-medium text-ink-2">{title}</p>}
      {body}
    </div>
  )
}

function isMultiline(s: JSONSchema): boolean {
  if (bool(s['ui:multiline'])) return true
  const w = str(s['ui:widget'])
  return w === 'textarea' || w === 'multiline' || MULTILINE_FORMATS.has(str(s.format) ?? '')
}

/* ---------- string / enum ---------- */

interface EnumInfo {
  raws: unknown[]
  labels: string[]
  index: number
}

function enumInfoOf(s: JSONSchema, value: unknown): EnumInfo {
  const raws = Array.isArray(s.enum) ? s.enum : []
  const names = s['enumNames']
  const labels = raws.map((r, i) => {
    const n = Array.isArray(names) && typeof names[i] === 'string' ? (names[i] as string) : undefined
    return n ?? displayRaw(r)
  })
  let index = raws.findIndex((r) => r === value)
  if (index < 0 && value != null) {
    const sv = String(value)
    index = raws.findIndex((r) => String(r) === sv)
  }
  return { raws, labels, index }
}

function EnumNode({
  schema,
  raws,
  value,
  onChange,
  disabled,
}: {
  schema: JSONSchema
  raws: unknown[]
  value: unknown
  onChange: (v: unknown) => void
  disabled: boolean
}) {
  const info = enumInfoOf(schema, value)
  const options = info.labels.map((label, i) => ({ value: String(i), label }))
  const current = info.index >= 0 ? String(info.index) : null
  const pick = (v: string) => onChange(raws[Number(v)])
  const widget = str(schema['ui:widget'])

  if (widget === 'segmented' || widget === 'radio') {
    const radioOptions = options.map((o) => ({ value: o.value, label: o.label }))
    if (widget === 'segmented') {
      return (
        <Segmented
          options={radioOptions}
          value={current ?? ''}
          onChange={pick}
          className={disabled ? 'pointer-events-none opacity-50' : undefined}
        />
      )
    }
    return <RadioGroup options={radioOptions} value={current ?? ''} onChange={pick} disabled={disabled} />
  }
  return (
    <Select
      options={options}
      value={current}
      onChange={pick}
      placeholder={str(schema['ui:placeholder']) ?? 'Select…'}
      disabled={disabled}
      ariaLabel={str(schema.title)}
    />
  )
}

function StringNode({
  schema,
  value,
  onChange,
  ctx,
  password = false,
}: {
  schema: JSONSchema
  value: unknown
  onChange: (v: unknown) => void
  ctx: NodeCtx
  password?: boolean
}) {
  const text = typeof value === 'string' ? value : value == null ? '' : String(value)
  const placeholder = str(schema['ui:placeholder'])
  const widget = str(schema['ui:widget'])
  const format = str(schema.format)
  const mono = bool(schema['ui:mono']) || format === 'code'
  const rows = num(schema['ui:rows']) ?? 3
  const maxLength = num(schema.maxLength)
  const disabled = ctx.disabled

  if (isMultiline(schema) || widget === 'textarea') {
    return (
      <Textarea
        id={ctx.id}
        value={text}
        rows={rows}
        mono={mono}
        placeholder={placeholder}
        disabled={disabled}
        maxLength={maxLength}
        onChange={(e) => onChange(e.target.value)}
      />
    )
  }

  const inputType = password
    ? 'password'
    : format === 'email'
      ? 'email'
      : format === 'uri' || format === 'url'
        ? 'url'
        : format === 'date'
          ? 'date'
          : format === 'time'
            ? 'time'
            : format === 'datetime'
              ? 'datetime-local'
              : 'text'

  return (
    <Input
      id={ctx.id}
      type={inputType}
      value={text}
      placeholder={placeholder}
      disabled={disabled}
      maxLength={maxLength}
      autoComplete={password ? 'off' : undefined}
      onChange={(e) => onChange(e.target.value)}
    />
  )
}

function NumberNode({
  schema,
  value,
  onChange,
  ctx,
}: {
  schema: JSONSchema
  value: unknown
  onChange: (v: unknown) => void
  ctx: NodeCtx
}) {
  const toText = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? String(v) : '')
  const [text, setText] = useState(() => toText(value))
  const [seen, setSeen] = useState<unknown>(value)

  // Re-sync when the value changes externally (restore, reset) but not after
  // our own keystrokes (prop value === what we sent).
  useEffect(() => {
    if (value !== seen) {
      setSeen(value)
      setText(toText(value))
    }
  })

  const handle = (raw: string) => {
    setText(raw)
    const t = raw.trim()
    if (t === '') {
      setSeen(null)
      onChange(null)
      return
    }
    const n = Number(t)
    if (Number.isFinite(n)) {
      setSeen(n)
      onChange(n)
    }
    // partial/invalid input ("1e", "-") stays local until it parses
  }

  return (
    <Input
      id={ctx.id}
      type="number"
      inputMode="decimal"
      value={text}
      min={num(schema.minimum)}
      max={num(schema.maximum)}
      step={num(schema.multipleOf) ?? num(schema['ui:step']) ?? 'any'}
      placeholder={str(schema['ui:placeholder'])}
      disabled={ctx.disabled}
      onChange={(e) => handle(e.target.value)}
    />
  )
}

function BooleanNode({
  schema,
  value,
  onChange,
  required,
  ctx,
}: {
  schema: JSONSchema
  value: unknown
  onChange: (v: unknown) => void
  required: boolean
  ctx: NodeCtx
}) {
  const checked = value === true
  const widget = str(schema['ui:widget'])

  if (widget === 'checkbox') {
    return (
      <Checkbox
        id={ctx.id}
        label={str(schema.title) ?? 'Enable'}
        checked={checked}
        disabled={ctx.disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
    )
  }

  const missing = required && value == null
  return (
    <span className="inline-flex items-center gap-2">
      <Switch checked={checked} disabled={ctx.disabled} onChange={(v) => onChange(v)} aria-label={str(schema.title)} />
      {missing && <span className="text-xs text-danger">Required</span>}
    </span>
  )
}

/* ---------- oneOf / anyOf variants ---------- */

function variantLabel(b: JSONSchema, i: number): string {
  const title = str(b.title)
  if (title) return title
  const t = effType(b)
  const names: Record<string, string> = {
    object: 'Object',
    array: 'List',
    string: 'Text',
    number: 'Number',
    integer: 'Number',
    boolean: 'On/off',
    null: 'None',
  }
  return (t && names[t]) || `Option ${i + 1}`
}

function pickBranch(branches: JSONSchema[], value: unknown): number {
  if (isObj(value)) {
    const keys = new Set(Object.keys(value))
    let best = -1
    let bestScore = -1
    for (let i = 0; i < branches.length; i++) {
      const b = branches[i]
      const props = isObj(b.properties) ? Object.keys(b.properties) : []
      const req = Array.isArray(b.required) ? b.required.filter((r): r is string => typeof r === 'string') : []
      if (req.length > 0 && !req.every((k) => keys.has(k))) continue
      const score = props.filter((k) => keys.has(k)).length + req.length * 10
      if (score > bestScore) {
        bestScore = score
        best = i
      }
    }
    if (best >= 0) return best
  } else if (value === null) {
    const i = branches.findIndex((b) => effType(b) === 'null')
    if (i >= 0) return i
  } else if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
    const i = branches.findIndex((b) => effType(b) === typeof value && !b.properties)
    if (i >= 0) return i
  }
  return 0
}

function VariantNode({
  branches,
  value,
  onChange,
  ctx,
}: {
  schema: JSONSchema
  branches: JSONSchema[]
  value: unknown
  onChange: (v: unknown) => void
  required: boolean
  ctx: NodeCtx
}) {
  const idx = Math.min(branches.length - 1, Math.max(0, pickBranch(branches, value)))
  const options = branches.map((b, i) => ({
    value: String(i),
    label: variantLabel(b, i),
    hint: str(b.description),
  }))

  const switchTo = (v: string) => {
    const next = branches[Number(v)]
    if (!next) return
    const base = emptyValueFor(next)
    // carry over keys the new branch also knows about
    if (isObj(base) && isObj(value)) {
      const props = isObj(next.properties) ? next.properties : {}
      for (const k of Object.keys(props)) if (k in value) base[k] = (value as Record<string, unknown>)[k]
    }
    onChange(base)
  }

  return (
    <div className="flex flex-col gap-2">
      <Select
        options={options}
        value={String(idx)}
        onChange={switchTo}
        size="sm"
        disabled={ctx.disabled}
        ariaLabel="Variant"
        className="self-start"
      />
      <SchemaNode
        schema={branches[idx]}
        value={value}
        onChange={onChange}
        required={false}
        ctx={ctx}
      />
    </div>
  )
}

/* ---------- arrays ---------- */

function ArrayEditor({
  schema,
  value,
  onChange,
  ctx,
}: SchemaNodeProps & { schema: JSONSchema }) {
  const items = Array.isArray(value) ? value : []
  const tuple = Array.isArray(schema.items)
  const itemSchema = !tuple ? asJSONSchema(schema.items) : null
  const tupleSchemas = tuple
    ? (schema.items as unknown[]).map(asJSONSchema)
    : null

  const scalarItem =
    !tuple &&
    itemSchema != null &&
    !isContainer(itemSchema) &&
    !Array.isArray(itemSchema.enum)

  const setAt = (i: number, v: unknown) => {
    const next = items.slice()
    next[i] = v
    onChange(next)
  }
  const move = (i: number, dir: -1 | 1) => {
    const j = i + dir
    if (j < 0 || j >= items.length) return
    const next = items.slice()
    const tmp = next[i]
    next[i] = next[j]
    next[j] = tmp
    onChange(next)
  }
  const removeAt = (i: number) => onChange(items.filter((_, k) => k !== i))
  const add = () => {
    const seed = tuple ? (tupleSchemas?.[items.length] ? emptyValueFor(tupleSchemas[items.length]!) : null) : emptyValueFor(itemSchema ?? undefined)
    onChange([...items, seed])
  }

  const rows: Array<{ key: string; schema: JSONSchema | null; value: unknown }> = tuple
    ? items.map((v, i) => ({ key: String(i), schema: tupleSchemas?.[i] ?? null, value: v }))
    : items.map((v, i) => ({ key: String(i), schema: itemSchema, value: v }))

  const count = rows.length
  const canEdit = !ctx.disabled && !tuple

  return (
    <div className="rounded-lg border border-line bg-surface">
      {count > 0 ? (
        <div className="divide-y divide-line">
          {rows.map((row, i) => (
            <div
              key={row.key}
              className={cx('flex gap-1.5 p-1.5', scalarItem ? 'items-center' : 'items-start')}
            >
              <div className="min-w-0 flex-1">
                <SchemaNode
                  schema={row.schema}
                  value={row.value}
                  onChange={(v) => setAt(i, v)}
                  required={false}
                  ctx={{ disabled: ctx.disabled, id: `${ctx.id}-${i}`, depth: ctx.depth + 1 }}
                />
              </div>
              {canEdit && (
                <div className={cx('flex shrink-0 gap-0.5', !scalarItem && 'pt-0.5')}>
                  <IconButton
                    icon={<ChevronUp />}
                    label="Move up"
                    size="sm"
                    disabled={i === 0}
                    onClick={() => move(i, -1)}
                  />
                  <IconButton
                    icon={<ChevronDown />}
                    label="Move down"
                    size="sm"
                    disabled={i === count - 1}
                    onClick={() => move(i, 1)}
                  />
                  <IconButton
                    icon={<X />}
                    label="Remove"
                    size="sm"
                    onClick={() => removeAt(i)}
                  />
                </div>
              )}
            </div>
          ))}
        </div>
      ) : (
        <p className="px-2.5 py-2 text-xs text-ink-3">
          {ctx.disabled ? 'Empty.' : 'No items yet.'}
        </p>
      )}
      {canEdit && (
        <div className="border-t border-line px-1.5 py-1.5">
          <Button size="sm" variant="ghost" icon={<Plus />} onClick={add}>
            Add
          </Button>
        </div>
      )}
    </div>
  )
}

/* ---------- root ---------- */

export function SchemaForm({ schema, value, onChange, disabled = false, idPrefix }: SchemaFormProps) {
  const autoId = useId().replace(/[^a-zA-Z0-9_-]/g, '')
  const base = idPrefix ?? `sf-${autoId}`
  const s = asJSONSchema(schema)
  if (!s) return null
  return (
    <SchemaNode
      schema={s}
      value={value}
      onChange={onChange}
      required={false}
      ctx={{ disabled, id: base, depth: 0 }}
    />
  )
}
