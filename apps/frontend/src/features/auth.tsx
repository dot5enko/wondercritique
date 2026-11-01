/**
 * Auth feature — session state, route guards.
 *
 * `AuthProvider` hydrates the user from localStorage on boot (optimistic) and
 * confirms it with `GET /auth/me` (an expired access token transparently
 * refreshes in the client; a failed refresh clears the session and routes to
 * /login via the session-expired event).
 */
import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router'
import { ShieldAlert } from 'lucide-react'
import { EmptyState, Spinner } from '../ui'
import * as api from '../api'
import type { EmailCodeStart, User } from '../api'

export interface AuthContextValue {
  user: User | null
  /** True while the boot-time session hydration is in flight. */
  loading: boolean
  /** Sends the email code (step 1); resolves with its validity window. Throws ApiError (429 carries `retryAfter`). */
  login: (email: string) => Promise<EmailCodeStart>
  /** Verifies the code, stores the session and sets the user (step 2). */
  verify: (email: string, code: string) => Promise<void>
  logout: () => Promise<void>
  /** Forces a token refresh now (also re-fetches the user). */
  refresh: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within <AuthProvider>')
  return ctx
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [loading, setLoading] = useState(true)
  const navigate = useNavigate()

  useEffect(() => {
    let cancelled = false
    const stored = api.getSession()
    if (!stored) {
      setLoading(false)
      return
    }
    setUser(stored.user)
    api
      .me()
      .then((fresh) => {
        if (!cancelled) setUser(fresh)
      })
      .catch(() => {
        // 401 after a failed refresh — the client already cleared the session.
        if (!cancelled) setUser(null)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  // Refresh failure anywhere in the app → anonymous + back to /login.
  useEffect(() => {
    return api.onSessionExpired(() => {
      setUser(null)
      navigate('/login', { replace: true })
    })
  }, [navigate])

  const value: AuthContextValue = {
    user,
    loading,
    login: async (email) => api.startEmailCode(email),
    verify: async (email, code) => {
      setUser(await api.verifyEmailCode(email, code))
    },
    logout: async () => {
      await api.logout()
      setUser(null)
      navigate('/login', { replace: true })
    },
    refresh: async () => {
      await api.refreshSession()
      setUser(await api.me())
    },
  }

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

/** Centered spinner shown while the session is being restored. */
export function BootScreen() {
  return (
    <div className="grid h-dvh w-dvw place-items-center bg-page">
      <Spinner size={18} className="text-ink-3" />
    </div>
  )
}

/** Blocks rendering until signed in; anonymous users land on /login. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { user, loading } = useAuth()
  const location = useLocation()
  if (loading) return <BootScreen />
  if (!user) return <Navigate to="/login" replace state={{ from: location.pathname }} />
  return <>{children}</>
}

/** Admin-only zone: anonymous → /login, signed-in non-admin → Forbidden page. */
export function RequireAdmin({ children }: { children: ReactNode }) {
  const { user, loading } = useAuth()
  if (loading) return <BootScreen />
  if (!user) return <Navigate to="/login" replace />
  if (user.role !== 'admin') {
    return (
      <div className="grid h-full place-items-center p-6">
        <EmptyState
          icon={ShieldAlert}
          title="Forbidden"
          description="This section is only available to admins."
        />
      </div>
    )
  }
  return <>{children}</>
}
