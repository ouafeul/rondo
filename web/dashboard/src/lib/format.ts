const relative = new Intl.RelativeTimeFormat("en", { numeric: "auto" })

export function ago(iso?: string): string {
  if (!iso) return "—"
  const s = Math.round((Date.parse(iso) - Date.now()) / 1000)
  const abs = Math.abs(s)
  if (abs < 60) return relative.format(s, "second")
  if (abs < 3600) return relative.format(Math.round(s / 60), "minute")
  if (abs < 86400) return relative.format(Math.round(s / 3600), "hour")
  return relative.format(Math.round(s / 86400), "day")
}

export function dateTime(iso?: string): string {
  return iso ? new Date(iso).toLocaleString("en-GB") : ""
}

export function duration(start?: string, end?: string): string {
  if (!start) return "—"
  const s = Math.max(0, Math.round(((end ? Date.parse(end) : Date.now()) - Date.parse(start)) / 1000))
  if (s < 60) return `${s} s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} min ${String(s % 60).padStart(2, "0")} s`
  return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")} min`
}

export function lastSegment(ref?: string): string {
  return ref ? ref.slice(ref.lastIndexOf("/") + 1) : ""
}
