/** Confirmation dialog — thin wrapper over {@link Modal} with Cancel/confirm footer. */
import { RotateCcw, TriangleAlert } from 'lucide-react'
import { Modal } from './Modal'
import { Button } from '../ui/Button'

export interface ConfirmDialogProps {
  open: boolean
  onClose: () => void
  title: string
  body: React.ReactNode
  confirmLabel: string
  /** Called when the confirm button is pressed. */
  onConfirm: () => void
  /** Renders the confirm button in the danger variant. */
  danger?: boolean
  /** Disables both buttons and shows a spinner on confirm while true. */
  busy?: boolean
  cancelLabel?: string
}

export function ConfirmDialog({
  open,
  onClose,
  title,
  body,
  confirmLabel,
  onConfirm,
  danger = false,
  busy = false,
  cancelLabel = 'Cancel',
}: ConfirmDialogProps) {
  return (
    <Modal
      open={open}
      onClose={busy ? () => {} : onClose}
      size="sm"
      title={
        <span className="inline-flex items-center gap-2">
          {danger && <TriangleAlert size={15} className="shrink-0 text-danger" />}
          {title}
        </span>
      }
      footer={
        <>
          <Button variant="secondary" size="sm" disabled={busy} onClick={onClose}>
            {cancelLabel}
          </Button>
          <Button
            variant={danger ? 'danger' : 'primary'}
            size="sm"
            loading={busy}
            onClick={onConfirm}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="flex items-start gap-2.5">
        {!danger && <RotateCcw size={15} className="mt-0.5 shrink-0 text-ink-3" />}
        <div className="min-w-0 text-base leading-relaxed text-ink-2">{body}</div>
      </div>
    </Modal>
  )
}
