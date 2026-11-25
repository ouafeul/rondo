import { createContext, useContext, useEffect, useState } from "react"

export interface FetchResult {
  at: Date
  error?: string
}

export interface RefreshState {
  auto: boolean
  nonce: number
  onResult: (r: FetchResult) => void
}

export const RefreshContext = createContext<RefreshState>({ auto: true, nonce: 0, onResult: () => {} })

export const POLL_MS = 5000

async function read(path: string, signal: AbortSignal): Promise<Response> {
  const resp = await fetch(path, { signal })
  if (!resp.ok) throw new Error((await resp.text()).trim() || `HTTP ${resp.status}`)
  return resp
}

const asJSON = (r: Response) => r.json()
const asText = (r: Response) => r.text()

interface State<T> {
  path: string
  key: string
  data?: T
  error?: string
}

function usePoll<T>(path: string | null, parse: (r: Response) => Promise<T>, report: boolean) {
  const { auto, nonce, onResult } = useContext(RefreshContext)
  const [tick, setTick] = useState(0)
  const [state, setState] = useState<State<T>>()

  useEffect(() => {
    if (!auto) return
    const id = setInterval(() => setTick((t) => t + 1), POLL_MS)
    return () => clearInterval(id)
  }, [auto])

  const key = `${path}|${nonce}|${tick}`
  useEffect(() => {
    if (!path) return
    const ctrl = new AbortController()
    read(path, ctrl.signal)
      .then(parse)
      .then(
        (data: T) => {
          if (ctrl.signal.aborted) return
          setState({ path, key, data })
          if (report) onResult({ at: new Date() })
        },
        (e: unknown) => {
          if (ctrl.signal.aborted) return
          const error = e instanceof Error ? e.message : String(e)
          setState((s) => ({ path, key, error, data: s?.path === path ? s.data : undefined }))
          if (report) onResult({ at: new Date(), error })
        },
      )
    return () => ctrl.abort()
  }, [path, key, parse, report, onResult])

  const current = state?.path === path ? state : undefined
  return { data: current?.data, error: current?.error, loading: !!path && state?.key !== key }
}

export function useApi<T>(path: string) {
  return usePoll<T>(path, asJSON, true)
}

export function useText(path: string | null) {
  return usePoll<string>(path, asText, false)
}
