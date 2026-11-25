import { useMemo, useSyncExternalStore } from "react"

function subscribe(cb: () => void) {
  window.addEventListener("hashchange", cb)
  return () => window.removeEventListener("hashchange", cb)
}

export function href(view: string, params?: Record<string, string>): string {
  const q = new URLSearchParams(params).toString()
  return `#${view}${q ? `?${q}` : ""}`
}

export function useRoute() {
  const hash = useSyncExternalStore(subscribe, () => window.location.hash)
  return useMemo(() => {
    const [view, query] = hash.replace(/^#/, "").split("?")
    return { view: view || "overview", params: new URLSearchParams(query) }
  }, [hash])
}
