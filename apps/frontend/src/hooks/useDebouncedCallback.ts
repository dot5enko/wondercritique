/** Classic debounce: trailing-edge call `delay` ms after the last invocation. */
import { useCallback, useEffect, useRef } from 'react'

export function useDebouncedCallback<Args extends unknown[]>(
  fn: (...args: Args) => void,
  delay: number,
): (...args: Args) => void {
  const fnRef = useRef(fn)
  useEffect(() => {
    fnRef.current = fn
  })
  const timer = useRef<number | null>(null)

  useEffect(
    () => () => {
      if (timer.current !== null) window.clearTimeout(timer.current)
    },
    [],
  )

  return useCallback(
    (...args: Args) => {
      if (timer.current !== null) window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => {
        timer.current = null
        fnRef.current(...args)
      }, delay)
    },
    [delay],
  )
}
