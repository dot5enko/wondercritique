/**
 * Draft state with dirty tracking (JSON snapshot compare) and a debounced
 * autosave (800ms). Typical wiring in a studio editor page:
 *
 *   const d = useDirtyDraft(initialBody, { autosave: (body) => putDraft(kind, id, body) })
 *   // d.draft / d.dirty feed SchemaForm + VersionBar; d.reset(def.draft)
 *   // re-seeds after restore/external reload; d.save(fn) saves immediately
 *   // and remembers `fn` as the autosave saver.
 *
 * When `initial` changes while the draft has no unsaved edits (e.g. the def
 * finished loading), the new value is adopted automatically.
 */
import { useCallback, useEffect, useMemo, useRef, useState, type Dispatch, type SetStateAction } from 'react'

export type DraftSaveState = 'idle' | 'saving' | 'saved' | 'error'

export interface UseDirtyDraftOptions<T> {
  /** Default saver used by the debounced autosave. */
  autosave?: (draft: T) => Promise<unknown>
  /** Autosave debounce in ms (default 800). */
  delay?: number
  onSaved?: () => void
  onError?: (err: unknown) => void
}

export interface DirtyDraft<T> {
  draft: T
  setDraft: Dispatch<SetStateAction<T>>
  dirty: boolean
  saveState: DraftSaveState
  /** Replaces the draft and the saved snapshot (marks clean). */
  reset: (value: T) => void
  /**
   * Saves now using `fn` (also remembered as the autosave saver) and resolves
   * when the request settles. Resolves immediately if another save is in
   * flight (it will be re-run afterwards when edits are pending).
   */
  save: (fn: (draft: T) => Promise<unknown>) => Promise<void>
}

export function useDirtyDraft<T>(initial: T, opts?: UseDirtyDraftOptions<T>): DirtyDraft<T> {
  const delay = opts?.delay ?? 800
  const [draft, setDraftState] = useState<T>(initial)
  const [savedJson, setSavedJson] = useState(() => JSON.stringify(initial))
  const [saveState, setSaveState] = useState<DraftSaveState>('idle')

  /* Ref mirrors so debounced callbacks always see the latest values. */
  const draftRef = useRef(draft)
  const savedJsonRef = useRef(savedJson)
  const dirtyRef = useRef(false)
  const saverRef = useRef<((draft: T) => Promise<unknown>) | undefined>(opts?.autosave)
  const onSavedRef = useRef(opts?.onSaved)
  const onErrorRef = useRef(opts?.onError)

  useEffect(() => {
    draftRef.current = draft
  })
  useEffect(() => {
    savedJsonRef.current = savedJson
  })
  useEffect(() => {
    if (opts?.autosave) saverRef.current = opts.autosave
  })
  useEffect(() => {
    onSavedRef.current = opts?.onSaved
    onErrorRef.current = opts?.onError
  })

  const dirty = useMemo(() => JSON.stringify(draft) !== savedJson, [draft, savedJson])
  useEffect(() => {
    dirtyRef.current = dirty
  })

  const savingRef = useRef(false)
  const pendingRef = useRef(false)
  const timerRef = useRef<number | null>(null)

  const clearTimer = useCallback(() => {
    if (timerRef.current !== null) {
      window.clearTimeout(timerRef.current)
      timerRef.current = null
    }
  }, [])

  const doSave = useCallback(async (): Promise<void> => {
    const fn = saverRef.current
    if (!fn) return
    if (savingRef.current) {
      pendingRef.current = true
      return
    }
    const snapshot = draftRef.current
    savingRef.current = true
    setSaveState('saving')
    try {
      await fn(snapshot)
      savedJsonRef.current = JSON.stringify(snapshot)
      setSavedJson(savedJsonRef.current)
      setSaveState('saved')
      onSavedRef.current?.()
    } catch (err) {
      setSaveState('error')
      onErrorRef.current?.(err)
    } finally {
      savingRef.current = false
      if (pendingRef.current) {
        pendingRef.current = false
        void doSave()
      }
    }
  }, [])

  const schedule = useCallback(() => {
    clearTimer()
    timerRef.current = window.setTimeout(() => {
      timerRef.current = null
      if (JSON.stringify(draftRef.current) !== savedJsonRef.current) void doSave()
    }, delay)
  }, [clearTimer, doSave, delay])

  const setDraft = useCallback(
    (value: SetStateAction<T>) => {
      setDraftState(value)
      setSaveState((s) => (s === 'saved' || s === 'error' ? 'idle' : s))
      schedule()
    },
    [schedule],
  )

  const reset = useCallback(
    (value: T) => {
      clearTimer()
      pendingRef.current = false
      setDraftState(value)
      const json = JSON.stringify(value)
      savedJsonRef.current = json
      setSavedJson(json)
      setSaveState('idle')
    },
    [clearTimer],
  )

  const save = useCallback(
    (fn: (draft: T) => Promise<unknown>): Promise<void> => {
      saverRef.current = fn
      clearTimer()
      if (savingRef.current) {
        pendingRef.current = true
        return Promise.resolve()
      }
      return doSave()
    },
    [clearTimer, doSave],
  )

  /* Adopt a changed `initial` while clean (def finished loading, etc.). */
  const prevInitialJson = useRef(JSON.stringify(initial))
  useEffect(() => {
    const json = JSON.stringify(initial)
    if (json === prevInitialJson.current) return
    prevInitialJson.current = json
    if (!dirtyRef.current) reset(initial)
  })

  useEffect(() => clearTimer, [clearTimer])

  return { draft, setDraft, dirty, saveState, reset, save }
}
