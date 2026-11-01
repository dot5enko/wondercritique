import {
  createContext,
  forwardRef,
  useContext,
  useId,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type TextareaHTMLAttributes,
} from 'react'
import { Check } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { cx } from './cx'

/* ---------- Field: label / hint / error wrapper with id + invalid context ---------- */

interface FieldContextValue {
  id: string
  invalid: boolean
}
const FieldContext = createContext<FieldContextValue | null>(null)

/** Returns the surrounding Field's id/invalid state, or null outside a Field. */
export function useField(): FieldContextValue | null {
  return useContext(FieldContext)
}

export interface FieldProps {
  label?: string
  hint?: string
  error?: string
  optional?: boolean
  children: ReactNode
  className?: string
}

export function Field({ label, hint, error, optional = false, children, className }: FieldProps) {
  const id = useId()
  return (
    <FieldContext.Provider value={{ id, invalid: Boolean(error) }}>
      <div className={cx('flex flex-col items-start gap-1.5', className)}>
        {label && (
          <label htmlFor={id} className="flex w-full items-baseline justify-between text-xs font-medium text-ink-2">
            {label}
            {optional && <span className="text-2xs font-normal text-ink-3">Optional</span>}
          </label>
        )}
        {children}
        {error ? (
          <p className="text-xs text-danger">{error}</p>
        ) : hint ? (
          <p className="text-xs text-ink-3">{hint}</p>
        ) : null}
      </div>
    </FieldContext.Provider>
  )
}

/* ---------- shared control classes ---------- */

const CONTROL_FOCUS =
  'outline-none transition duration-100 focus:border-accent focus:ring-2 focus:ring-accent/15 disabled:pointer-events-none disabled:opacity-45'
const CONTROL_INVALID = 'border-danger focus:border-danger focus:ring-danger/15'

/* ---------- Input ---------- */

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'size'> {
  icon?: ReactNode
  trail?: ReactNode
  size?: 'sm' | 'md' | 'lg'
  variant?: 'outline' | 'filled'
}

const INPUT_SIZES = {
  sm: 'h-6 rounded-md px-2 text-xs',
  md: 'h-[30px] rounded-md px-2 text-base',
  lg: 'h-9 rounded-lg px-2.5 text-sm',
}
const INPUT_VARIANTS = {
  outline: 'border border-line bg-surface hover:border-line-strong',
  filled: 'border border-transparent bg-app hover:border-line focus:bg-surface',
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { icon, trail, size = 'md', variant = 'outline', className, id, ...rest },
  ref,
) {
  const field = useField()
  return (
    <div className="relative w-full">
      {icon && (
        <span className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-ink-3 [&_svg]:size-3.5">
          {icon}
        </span>
      )}
      {trail && (
        <span className="absolute right-2 top-1/2 -translate-y-1/2 text-ink-3">{trail}</span>
      )}
      <input
        ref={ref}
        id={id ?? field?.id}
        aria-invalid={field?.invalid || undefined}
        className={cx(
          'w-full text-ink placeholder:text-ink-3',
          INPUT_SIZES[size],
          INPUT_VARIANTS[variant],
          CONTROL_FOCUS,
          field?.invalid && CONTROL_INVALID,
          icon ? 'pl-7' : undefined,
          trail ? 'pr-9' : undefined,
          className,
        )}
        {...rest}
      />
    </div>
  )
})

/* ---------- Textarea ---------- */

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  mono?: boolean
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { mono = false, className, id, rows = 3, ...rest },
  ref,
) {
  const field = useField()
  return (
    <textarea
      ref={ref}
      id={id ?? field?.id}
      aria-invalid={field?.invalid || undefined}
      rows={rows}
      className={cx(
        'w-full resize-y rounded-md border border-line bg-surface px-2.5 py-2 text-ink placeholder:text-ink-3 hover:border-line-strong',
        mono ? 'font-mono text-xs' : 'text-base',
        'leading-relaxed',
        CONTROL_FOCUS,
        field?.invalid && CONTROL_INVALID,
        className,
      )}
      {...rest}
    />
  )
})

/* ---------- Checkbox ---------- */

export interface CheckboxProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> {
  label?: ReactNode
}

export function Checkbox({ label, className, ...rest }: CheckboxProps) {
  return (
    <label
      className={cx(
        'group/cb inline-flex select-none items-center gap-2 text-base text-ink',
        rest.disabled && 'cursor-not-allowed opacity-50',
        className,
      )}
    >
      <input type="checkbox" className="peer sr-only" {...rest} />
      <span
        aria-hidden
        className="grid size-4 shrink-0 place-items-center rounded-[5px] border border-line-strong bg-surface text-white transition duration-100 group-hover/cb:border-ink-3 active:scale-90 peer-checked:border-accent peer-checked:bg-accent peer-checked:[&_svg]:scale-100 peer-checked:[&_svg]:opacity-100 peer-focus-visible:ring-2 peer-focus-visible:ring-accent/30 [&_svg]:scale-50 [&_svg]:opacity-0 [&_svg]:transition-all [&_svg]:duration-100"
      >
        <Check size={11} strokeWidth={3.5} />
      </span>
      {label && <span>{label}</span>}
    </label>
  )
}

/* ---------- RadioGroup ---------- */

export interface RadioOption {
  value: string
  label: string
  hint?: string
}

export interface RadioGroupProps {
  options: RadioOption[]
  value: string
  onChange: (value: string) => void
  name?: string
  disabled?: boolean
  className?: string
}

export function RadioGroup({ options, value, onChange, name, disabled = false, className }: RadioGroupProps) {
  const autoName = useId()
  const groupName = name ?? autoName
  return (
    <div role="radiogroup" className={cx('grid gap-1', className)}>
      {options.map((o) => {
        const selected = o.value === value
        return (
          <label
            key={o.value}
            className={cx(
              'group/rg flex cursor-pointer items-start gap-2.5 rounded-md p-1.5 text-base text-ink transition-colors duration-100 hover:bg-ink/[0.03]',
              disabled && 'pointer-events-none opacity-50',
            )}
          >
            <input
              type="radio"
              name={groupName}
              value={o.value}
              checked={selected}
              onChange={() => onChange(o.value)}
              disabled={disabled}
              className="peer sr-only"
            />
            <span
              aria-hidden
              className={cx(
                'mt-0.5 grid size-4 shrink-0 place-items-center rounded-full border bg-surface transition duration-100 active:scale-90',
                selected ? 'border-accent' : 'border-line-strong group-hover/rg:border-ink-3',
                'peer-focus-visible:ring-2 peer-focus-visible:ring-accent/30',
              )}
            >
              <span className={cx('size-1.5 rounded-full bg-accent transition-opacity duration-100', selected ? 'opacity-100' : 'opacity-0')} />
            </span>
            <span className="min-w-0 leading-tight">
              {o.label}
              {o.hint && <span className="block text-xs text-ink-3">{o.hint}</span>}
            </span>
          </label>
        )
      })}
    </div>
  )
}

/* ---------- Switch ---------- */

export interface SwitchProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'onChange'> {
  checked: boolean
  onChange: (checked: boolean) => void
}

export function Switch({ checked, onChange, className, type = 'button', ...rest }: SwitchProps) {
  return (
    <button
      type={type}
      role="switch"
      aria-checked={checked}
      onClick={() => onChange(!checked)}
      className={cx(
        'relative inline-flex h-[18px] w-8 shrink-0 cursor-pointer items-center rounded-full p-0 outline-none transition-colors duration-150 focus-visible:ring-2 focus-visible:ring-accent/40 active:[&>span]:scale-90',
        checked ? 'bg-accent' : 'bg-ink/15 hover:bg-ink/25',
        className,
      )}
      {...rest}
    >
      <span
        aria-hidden
        className={cx(
          'pointer-events-none absolute left-[2px] top-1/2 size-[14px] -translate-y-1/2 rounded-full bg-white shadow-xs transition-transform duration-150',
          checked ? 'translate-x-[14px]' : 'translate-x-0',
        )}
      />
    </button>
  )
}

/* ---------- Segmented ---------- */

export interface SegmentedOption<T extends string> {
  value: T
  label: ReactNode
  icon?: LucideIcon
}

export interface SegmentedProps<T extends string> {
  options: SegmentedOption<T>[]
  value: T
  onChange: (value: T) => void
  className?: string
}

export function Segmented<T extends string>({ options, value, onChange, className }: SegmentedProps<T>) {
  return (
    <div className={cx('inline-flex items-center gap-0.5 rounded-lg border border-line bg-app p-0.5', className)}>
      {options.map((o) => {
        const active = o.value === value
        const Icon = o.icon
        return (
          <button
            key={o.value}
            type="button"
            aria-pressed={active}
            onClick={() => onChange(o.value)}
            className={cx(
              'inline-flex h-6 cursor-pointer items-center gap-1.5 rounded-[6px] px-2 text-xs font-medium outline-none transition duration-100 active:scale-[0.97] focus-visible:ring-2 focus-visible:ring-accent/40',
              active ? 'bg-surface text-ink shadow-xs' : 'text-ink-2 hover:text-ink',
            )}
          >
            {Icon && <Icon size={13} className={active ? 'text-accent' : 'text-ink-3'} />}
            {o.label}
          </button>
        )
      })}
    </div>
  )
}
