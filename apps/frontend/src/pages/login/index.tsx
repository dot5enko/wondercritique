/**
 * Login — email-code flow (start → 6-digit code), design-system only.
 *
 * Rate limiting (docs/backend.md): resend within 60 s or per-IP limits answer
 * 429 with `Retry-After`. The resend countdown starts at `min(expires_in, 60)`
 * — the backend's documented resend cooldown; `expires_in` itself (600 s) is
 * the code validity window. A 429 resets the countdown to `retryAfter`.
 */
import { ArrowLeft, KeyRound, Mail } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router'
import type { ApiError, EmailCodeStart } from '../../api'
import { useAuth } from '../../features/auth'
import { Button, Callout, Field, Input } from '../../ui'

const RESEND_COOLDOWN = 60

export default function LoginPage() {
  const { user, login, verify } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const from = (location.state as { from?: string } | null)?.from ?? '/'

  const [step, setStep] = useState<'email' | 'code'>('email')
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [resendIn, setResendIn] = useState(0)
  // Local mode (no email delivery): the backend returns the code itself.
  const [devCode, setDevCode] = useState<{ code: string; notice: string } | null>(null)

  // Refresh the resend countdown once per second while it runs.
  useEffect(() => {
    if (resendIn <= 0) return
    const timer = setInterval(() => setResendIn((s) => Math.max(0, s - 1)), 1000)
    return () => clearInterval(timer)
  }, [resendIn])

  const sendCode = async () => {
    setError(null)
    setBusy(true)
    try {
      const start: EmailCodeStart = await login(email)
      setStep('code')
      setDevCode(start.dev_code ? { code: start.dev_code, notice: start.dev_notice ?? '' } : null)
      setResendIn(Math.min(start.expires_in, RESEND_COOLDOWN))
    } catch (err) {
      applyApiError(err)
    } finally {
      setBusy(false)
    }
  }

  const applyApiError = (err: unknown) => {
    if (err instanceof Error && 'status' in err) {
      const apiErr = err as ApiError
      if (apiErr.status === 429) {
        setResendIn(apiErr.retryAfter ?? RESEND_COOLDOWN)
        setError(`Too many attempts — try again in ${apiErr.retryAfter ?? RESEND_COOLDOWN}s.`)
      } else {
        setError(apiErr.message)
      }
    } else {
      setError(err instanceof Error ? err.message : 'Something went wrong')
    }
  }

  const confirmCode = async (e: FormEvent) => {
    e.preventDefault()
    setError(null)
    setBusy(true)
    try {
      await verify(email, code.trim())
      navigate(from, { replace: true })
    } catch (err) {
      applyApiError(err)
      setBusy(false)
    }
  }

  const onEmailSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (email.trim()) void sendCode()
  }

  // Already signed in → nothing to do here (kept after all hooks).
  if (user) return <Navigate to={from} replace />

  return (
    <div className="grid h-dvh w-dvw place-items-center bg-page p-2.5">
      <div className="w-full max-w-sm rounded-xl border border-line bg-surface shadow-xs">
        <div className="flex flex-col gap-4 p-6">
          <div className="flex items-center gap-2.5">
            <div>
              <h1 className="text-sm font-semibold text-ink">Wonder Critique</h1>
              <p className="text-base text-ink-2">
                {step === 'email' ? 'Sign in with a one-time code' : `Code sent to ${email}`}
              </p>
            </div>
          </div>

          {error && <Callout tone="danger">{error}</Callout>}

          {step === 'email' ? (
            <form onSubmit={onEmailSubmit} className="flex flex-col gap-4">
              <Input
                type="email"
                required
                autoFocus
                autoComplete="email"
                placeholder="you@example.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                icon={<Mail />}
              />
              <Button variant="primary" block type="submit" loading={busy} disabled={!email.trim()}>
                Send code
              </Button>
            </form>
          ) : (
            <form onSubmit={confirmCode} className="flex flex-col gap-4">
              {devCode && (
                <Callout tone="warning" title="Local mode — no email sent">
                  <p>{devCode.notice}</p>
                  <div className="mt-2 flex items-center gap-2">
                    <code className="font-mono text-sm tracking-[0.3em] text-ink">{devCode.code}</code>
                    <Button variant="ghost" size="sm" type="button" onClick={() => setCode(devCode.code)}>
                      Use code
                    </Button>
                  </div>
                </Callout>
              )}
              <Field
                label="6-digit code"
                hint={devCode ? 'The code is valid for 10 minutes.' : 'Check your inbox — the code is valid for 10 minutes.'}
              >
                <Input
                  autoFocus
                  required
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  pattern="[0-9]{6}"
                  maxLength={6}
                  placeholder="000000"
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
                  icon={<KeyRound />}
                  className="text-center font-mono text-sm tracking-[0.4em]"
                />
              </Field>
              <Button variant="primary" block type="submit" loading={busy} disabled={code.length !== 6}>
                Sign in
              </Button>
              <div className="flex items-center justify-between text-base">
                <Button
                  variant="ghost"
                  size="sm"
                  icon={<ArrowLeft />}
                  disabled={busy}
                  onClick={() => {
                    setStep('email')
                    setCode('')
                    setDevCode(null)
                    setError(null)
                  }}
                >
                  Different email
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={busy || resendIn > 0}
                  onClick={() => void sendCode()}
                >
                  {resendIn > 0 ? `Resend in ${resendIn}s` : 'Resend code'}
                </Button>
              </div>
            </form>
          )}
        </div>
      </div>
    </div>
  )
}
