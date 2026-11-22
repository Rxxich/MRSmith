import { useCallback, useEffect, useRef, useState } from 'react'

export interface AsyncState<T> {
  data: T | undefined
  error: Error | undefined
  loading: boolean
  reload: () => Promise<void>
  setData: (v: T | undefined | ((prev: T | undefined) => T | undefined)) => void
}

/** Runs an async loader on mount and whenever deps change. */
export function useAsync<T>(load: () => Promise<T>, deps: unknown[]): AsyncState<T> {
  const [data, setData] = useState<T>()
  const [error, setError] = useState<Error>()
  const [loading, setLoading] = useState(true)
  const seq = useRef(0)
  const loader = useRef(load)
  loader.current = load

  const reload = useCallback(async () => {
    const my = ++seq.current
    setLoading(true)
    try {
      const v = await loader.current()
      if (my === seq.current) {
        setData(v)
        setError(undefined)
      }
    } catch (e) {
      if (my === seq.current) setError(e as Error)
    } finally {
      if (my === seq.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  return { data, error, loading, reload, setData }
}

/** Calls fn every `ms` while `active` is true. */
export function useInterval(fn: () => void, ms: number, active = true) {
  const ref = useRef(fn)
  ref.current = fn
  useEffect(() => {
    if (!active) return
    const id = window.setInterval(() => ref.current(), ms)
    return () => window.clearInterval(id)
  }, [ms, active])
}

/** Re-renders periodically so relative times ("5 минут назад") stay fresh. */
export function useNow(ms = 30_000) {
  const [, setTick] = useState(0)
  useInterval(() => setTick((t) => t + 1), ms)
}

export function useDebounced<T>(value: T, ms = 250): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const id = window.setTimeout(() => setV(value), ms)
    return () => window.clearTimeout(id)
  }, [value, ms])
  return v
}

export function useTitle(title: string) {
  useEffect(() => {
    document.title = title ? `${title} — MRSmith` : 'MRSmith'
  }, [title])
}
