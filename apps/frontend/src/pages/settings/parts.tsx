/** Small shared pieces of the LLM providers settings page (plan §3.2–§3.3). */
import { useEffect, useRef, useState } from 'react'
import { Check, Copy } from 'lucide-react'
import { Badge, IconButton, cx } from '../../ui'
import type { ProviderAdapter } from '../../api/settings'

/** Adapter badge for list rows and the detail header; `none` gets an extra-muted "no adapter" style. */
export function AdapterBadge({ adapter, className }: { adapter: ProviderAdapter; className?: string }) {
  if (adapter === 'none') {
    return (
      <Badge tone="neutral" className={cx('bg-transparent text-ink-3 ring-line', className)}>
        no adapter
      </Badge>
    )
  }
  return (
    <Badge tone="neutral" className={className}>
      {adapter}
    </Badge>
  )
}

/** Icon button that copies `value` to the clipboard and flashes a check mark. */
export function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<number | null>(null)

  useEffect(() => {
    return () => {
      if (timer.current !== null) window.clearTimeout(timer.current)
    }
  }, [])

  return (
    <IconButton
      size="sm"
      label={label}
      icon={copied ? <Check /> : <Copy />}
      className={cx(copied && 'text-success')}
      onClick={() => {
        const write = navigator.clipboard?.writeText(value)
        if (!write) return
        void write
          .then(() => {
            setCopied(true)
            if (timer.current !== null) window.clearTimeout(timer.current)
            timer.current = window.setTimeout(() => setCopied(false), 1500)
          })
          .catch(() => {})
      }}
    />
  )
}

/** Unique `${VAR}` placeholder names of a base-URL template, in order of appearance. */
export function apiVarNames(api: string | undefined): string[] {
  if (!api) return []
  const names: string[] = []
  for (const match of api.matchAll(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g)) {
    if (!names.includes(match[1])) names.push(match[1])
  }
  return names
}
