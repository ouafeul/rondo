import { Loader2, SquareTerminal, X } from "lucide-react"
import type * as React from "react"
import { useEffect, useMemo, useRef } from "react"
import { Empty } from "@/components/common"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { useText } from "@/hooks/use-api"
import { cn } from "@/lib/utils"

function lineClass(line: string): string | undefined {
  if (/\b(ERROR|FAILED|fatal|panic)\b|Error:|level=error/.test(line)) return "text-red-400"
  if (/\bWARN(ING)?\b|Warning:|level=warn/.test(line)) return "text-amber-300"
  if (/^(PLAY|TASK|RUNNING HANDLER) \[|^(Plan|Apply complete):|^STATUS:/.test(line)) return "font-semibold text-sky-300"
  return undefined
}

export function LogPanel({
  title,
  subtitle,
  path,
  onClose,
  placeholder,
  className,
}: {
  title?: string
  subtitle?: React.ReactNode
  path: string | null
  onClose: () => void
  placeholder: string
  className?: string
}) {
  const { data, error, loading } = useText(path)
  const pre = useRef<HTMLPreElement>(null)
  const follow = useRef(true)
  const lines = useMemo(() => (data ? data.replace(/\n$/, "").split("\n") : []), [data])

  useEffect(() => {
    follow.current = true
  }, [path])

  useEffect(() => {
    const el = pre.current
    if (el && follow.current) el.scrollTop = el.scrollHeight
  }, [data])

  if (!path) {
    return (
      <Card className={cn("justify-center", className)}>
        <Empty icon={SquareTerminal}>{placeholder}</Empty>
      </Card>
    )
  }

  return (
    <Card className={cn("gap-0 overflow-hidden py-0", className)}>
      <div className="flex items-center gap-3 border-b px-4 py-3">
        <SquareTerminal className="size-4 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1">
          <div className="truncate font-mono text-sm font-medium">{title}</div>
          {subtitle && <div className="truncate text-xs text-muted-foreground">{subtitle}</div>}
        </div>
        {loading && <Loader2 className="size-4 shrink-0 animate-spin text-muted-foreground" />}
        {data !== undefined && (
          <span className="shrink-0 text-xs text-muted-foreground tabular-nums">{lines.length} lines</span>
        )}
        <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label="Close the logs">
          <X />
        </Button>
      </div>
      {error && <p className="border-b bg-red-500/10 px-4 py-2 text-xs text-red-700 dark:text-red-400">{error}</p>}
      <pre
        ref={pre}
        onScroll={(e) => {
          const el = e.currentTarget
          follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
        }}
        className="min-h-64 flex-1 overflow-auto bg-zinc-950 p-4 font-mono text-xs leading-5 whitespace-pre-wrap break-words text-zinc-200"
      >
        {data === undefined ? (
          <span className="text-zinc-500">{error ? "logs unavailable" : "loading…"}</span>
        ) : data === "" ? (
          <span className="text-zinc-500">(empty)</span>
        ) : (
          lines.map((line, i) => (
            <div key={i} className={lineClass(line)}>
              {line || " "}
            </div>
          ))
        )}
      </pre>
    </Card>
  )
}
