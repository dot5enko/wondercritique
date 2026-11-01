/**
 * Runs an async function on mount (and whenever `deps` or `reload` change),
 * with AbortSignal support and stale-result protection. Previous data is kept
 * visible while refetching — gate skeletons on `loading && data == null`.
 */
import { useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react'

export interface AsyncState<T> {
  data: T | null
  loading: boolean
  error: unknown
}

export interface UseAsyncResult<T> extends AsyncState<T> {
  /** Re-runs the effect (new AbortController, loading=true). */
  reload: () => void
  setData: Dispatch<SetStateAction<T | null>>
}

export function useAsync<T>(
  fn: (signal: AbortSignal) => Promise<T>,
  deps: readonly unknown[],
): UseAsyncResult<T> {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [epoch, setEpoch] = useState(0)

  const fnRef = useRef(fn)
  useEffect(() => {
    fnRef.current = fn
  })

  useEffect(() => {
    const ctrl = new AbortController()
    let alive = true
    setLoading(true)
    setError(null)
    fnRef.current(ctrl.signal).then(
      (result) => {
        if (!alive) return
        setData(result)
        setLoading(false)
      },
      (err: unknown) => {
        if (!alive) return
        if (ctrl.signal.aborted || (err instanceof DOMException && err.name === 'AbortError')) return
        setError(err)
        setLoading(false)
      },
    )
    return () => {
      alive = false
      ctrl.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, epoch])

  const reload = useRef(() => setEpoch((e) => e + 1)).current

  return { data, loading, error, reload, setData }
}
