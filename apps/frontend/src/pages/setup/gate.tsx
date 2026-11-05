/**
 * First-run setup gate: admin users with no ready provider are sent to
 * `/setup` (the setup wizard). Mounted once inside `AuthProvider`; renders
 * nothing. Public routes and the wizard itself are never redirected.
 */
import { useEffect } from 'react'
import { useLocation, useNavigate } from 'react-router'
import { useAuth } from '../../features/auth'
import { getSetupStatus } from '../../api/settings'

export function SetupGate() {
  const { user, loading } = useAuth()
  const location = useLocation()
  const navigate = useNavigate()

  useEffect(() => {
    if (loading) return
    if (!user || user.role !== 'admin') return
    const path = location.pathname
    if (path.startsWith('/setup') || path === '/login' || path === '/showcase') return
    let cancelled = false
    getSetupStatus()
      .then((status) => {
        if (!cancelled && status.needs_setup) navigate('/setup', { replace: true })
      })
      .catch(() => {
        // Backend unreachable — leave the user where they are; pages already
        // render "backend isn't available yet" states for that case.
      })
    return () => {
      cancelled = true
    }
  }, [loading, user, location.pathname, navigate])

  return null
}
