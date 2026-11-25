import { FileText, Inbox } from "lucide-react"
import type * as React from "react"
import { Badge, type BadgeVariant } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { href } from "@/hooks/use-route"
import { ago, dateTime } from "@/lib/format"
import { labelOf, toneOf } from "@/lib/status"
import { cn } from "@/lib/utils"

const DOT: Record<BadgeVariant, string> = {
  default: "bg-primary-foreground",
  secondary: "bg-muted-foreground",
  outline: "bg-muted-foreground",
  ok: "bg-emerald-500",
  bad: "bg-red-500",
  warn: "bg-amber-500",
  info: "bg-sky-500 animate-pulse",
  neutral: "bg-muted-foreground/60",
}

export function StatusBadge({
  value,
  tone,
  label,
  className,
}: {
  value?: string
  tone?: BadgeVariant
  label?: string
  className?: string
}) {
  const t = tone ?? toneOf(value)
  return (
    <Badge variant={t} className={className}>
      <span className={cn("size-1.5 rounded-full", DOT[t])} />
      {label ?? labelOf(value)}
    </Badge>
  )
}

export function Hint({ label, children }: { label: React.ReactNode; children: React.ReactElement }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>{children}</TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}

export function Dash() {
  return <span className="text-muted-foreground">—</span>
}

export function Time({ iso, className }: { iso?: string; className?: string }) {
  if (!iso) return <Dash />
  return (
    <Hint label={dateTime(iso)}>
      <span className={cn("text-muted-foreground tabular-nums", className)}>{ago(iso)}</span>
    </Hint>
  )
}

export function Chips({ values, className }: { values?: Record<string, string>; className?: string }) {
  const entries = Object.entries(values ?? {})
  if (!entries.length) return <Dash />
  return (
    <div className={cn("flex flex-wrap gap-1", className)}>
      {entries.map(([k, v]) => (
        <Badge key={k} variant="neutral" className="max-w-full font-mono font-normal">
          <span className="truncate">
            {k}={v}
          </span>
        </Badge>
      ))}
    </div>
  )
}

export function JobLink({ name, className }: { name?: string; className?: string }) {
  if (!name) return <Dash />
  return (
    <a
      href={href("jobs", { job: name })}
      className={cn(
        "inline-flex max-w-full items-center gap-1 font-mono text-xs text-sky-700 hover:underline dark:text-sky-400",
        className,
      )}
    >
      <FileText className="size-3.5 shrink-0" />
      <span className="truncate">{name}</span>
    </a>
  )
}

export function Meter({ value, total, className }: { value: number; total: number; className?: string }) {
  const pct = total ? Math.round((value / total) * 100) : 0
  const color =
    total === 0
      ? "bg-muted-foreground/30"
      : value === total
        ? "bg-emerald-500"
        : value === 0
          ? "bg-red-500"
          : "bg-amber-500"
  return (
    <div className={cn("h-1.5 w-full overflow-hidden rounded-full bg-muted", className)}>
      <div className={cn("h-full rounded-full transition-all", color)} style={{ width: `${total ? pct : 100}%` }} />
    </div>
  )
}

export function Field({
  label,
  children,
  className,
}: {
  label: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn("min-w-0", className)}>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 truncate text-sm">{children}</dd>
    </div>
  )
}

export function Message({ text, tone = "bad" }: { text?: string; tone?: "bad" | "warn" | "neutral" }) {
  if (!text) return null
  const colors = {
    bad: "border-red-500/25 bg-red-500/5 text-red-700 dark:text-red-400",
    warn: "border-amber-500/30 bg-amber-500/5 text-amber-800 dark:text-amber-300",
    neutral: "bg-muted/50 text-muted-foreground",
  }
  return <p className={cn("rounded-md border px-3 py-2 text-xs break-words", colors[tone])}>{text}</p>
}

export function Empty({ icon: Icon = Inbox, children }: { icon?: React.ElementType; children: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 py-10 text-center text-sm text-muted-foreground">
      <Icon className="size-6 opacity-60" />
      {children}
    </div>
  )
}

export function PageSkeleton() {
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {Array.from({ length: 4 }, (_, i) => (
          <Skeleton key={i} className="h-28 rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-72 rounded-xl" />
    </div>
  )
}
