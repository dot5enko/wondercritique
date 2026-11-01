import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react'
import { Loader2 } from 'lucide-react'
import { cx } from './cx'

export type ButtonVariant = 'primary' | 'secondary' | 'soft' | 'ghost' | 'danger'
export type ButtonSize = 'sm' | 'md' | 'lg'

const BUTTON_BASE =
  'inline-flex shrink-0 cursor-pointer select-none items-center justify-center gap-1.5 whitespace-nowrap rounded-md font-medium outline-none transition duration-100 active:scale-[0.98] disabled:pointer-events-none disabled:opacity-45 focus-visible:ring-2 focus-visible:ring-accent/40'

const BUTTON_SIZES: Record<ButtonSize, { box: string; svg: string }> = {
  sm: { box: 'h-6 px-2 text-xs', svg: '[&_svg]:size-3.5' },
  md: { box: 'h-[30px] px-2.5 text-base', svg: '[&_svg]:size-[15px]' },
  lg: { box: 'h-9 px-3.5 text-sm', svg: '[&_svg]:size-4' },
}

const BUTTON_VARIANTS: Record<ButtonVariant, string> = {
  primary: 'bg-accent text-white hover:bg-accent-hover',
  secondary: 'border border-line bg-transparent text-ink hover:border-line-strong hover:bg-app',
  soft: 'border border-accent/20 bg-accent-soft text-accent-ink hover:bg-accent-muted/50',
  ghost: 'text-ink-2 hover:bg-ink/5 hover:text-ink',
  danger: 'bg-danger text-white hover:bg-danger/85',
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  size?: ButtonSize
  icon?: ReactNode
  iconRight?: ReactNode
  loading?: boolean
  block?: boolean
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = 'secondary',
    size = 'md',
    icon,
    iconRight,
    loading = false,
    block = false,
    className,
    children,
    disabled,
    type = 'button',
    ...rest
  },
  ref,
) {
  const s = BUTTON_SIZES[size]
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      className={cx(BUTTON_BASE, s.box, s.svg, BUTTON_VARIANTS[variant], block && 'w-full', className)}
      {...rest}
    >
      {loading ? <Loader2 className="animate-spin" /> : icon}
      {children}
      {iconRight}
    </button>
  )
})

export interface IconButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'children'> {
  icon: ReactNode
  /** Accessible name — rendered as aria-label + title. */
  label: string
  variant?: ButtonVariant
  size?: ButtonSize
}

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { icon, label, variant = 'ghost', size = 'md', className, type = 'button', ...rest },
  ref,
) {
  const s = BUTTON_SIZES[size]
  const box = { sm: 'size-6', md: 'size-[30px]', lg: 'size-9' }[size]
  return (
    <button
      ref={ref}
      type={type}
      aria-label={label}
      title={label}
      className={cx(BUTTON_BASE, box, s.svg, BUTTON_VARIANTS[variant], className)}
      {...rest}
    >
      {icon}
    </button>
  )
})

export function Spinner({ size = 15, className }: { size?: number; className?: string }) {
  return <Loader2 size={size} className={cx('animate-spin', className)} />
}

export function ButtonGroup({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={cx(
        'inline-flex divide-x divide-line overflow-hidden rounded-md border border-line bg-surface [&_button]:rounded-none [&_button]:border-0',
        className,
      )}
    >
      {children}
    </div>
  )
}
