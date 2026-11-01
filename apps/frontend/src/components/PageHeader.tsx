/** Standard page header: optional back link, title, muted description, action slot. */
import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { ArrowLeft } from 'lucide-react'
import { cx } from '../ui/cx'

export interface PageHeaderProps {
  title: ReactNode
  description?: ReactNode
  /** Renders a small back link above the title. */
  back?: { to: string; label: string }
  /** Right-aligned action slot (buttons). */
  actions?: ReactNode
  className?: string
}

export function PageHeader({ title, description, back, actions, className }: PageHeaderProps) {
  return (
    <div className={cx('flex items-start justify-between gap-3', className)}>
      <div className="min-w-0">
        {back && (
          <Link
            to={back.to}
            className="mb-1 inline-flex items-center gap-1 text-xs text-ink-2 transition-colors duration-100 hover:text-ink"
          >
            <ArrowLeft size={13} />
            {back.label}
          </Link>
        )}
        <h1 className="text-lg font-semibold text-ink">{title}</h1>
        {description != null && <p className="mt-0.5 text-xs text-ink-2">{description}</p>}
      </div>
      {actions != null && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}
