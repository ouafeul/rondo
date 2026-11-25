import { ListChecks } from "lucide-react"
import { Empty, PageSkeleton, StatusBadge, Time } from "@/components/common"
import { LogPanel } from "@/components/log-panel"
import { Badge } from "@/components/ui/badge"
import { Card, CardAction, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useApi } from "@/hooks/use-api"
import { href } from "@/hooks/use-route"
import { duration } from "@/lib/format"
import type { JobView } from "@/lib/types"
import { cn } from "@/lib/utils"

export function Jobs({ params }: { params: URLSearchParams }) {
  const { data } = useApi<JobView[]>("api/jobs")
  const selected = params.get("job")
  if (!data) return <PageSkeleton />

  const job = data.find((j) => j.name === selected)
  const running = data.filter((j) => j.state === "Running").length
  const failed = data.filter((j) => j.state === "Failed").length

  return (
    <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
      <Card className="gap-0 overflow-hidden py-0 xl:h-[calc(100svh-12rem)]">
        <CardHeader className="border-b py-4">
          <CardTitle className="text-sm">{data.length} Jobs</CardTitle>
          <CardDescription className="text-xs">
            {running} running · {failed} failed
          </CardDescription>
          <CardAction className="text-xs text-muted-foreground">logs: 500 lines</CardAction>
        </CardHeader>
        {data.length ? (
          <ul className="max-h-[28rem] divide-y overflow-y-auto xl:max-h-none xl:flex-1">
            {data.map((j) => {
              const active = j.name === selected
              return (
                <li key={j.name}>
                  <a
                    href={href("jobs", active ? undefined : { job: j.name })}
                    className={cn(
                      "block space-y-1.5 px-4 py-3 transition-colors hover:bg-muted/50",
                      active && "bg-muted hover:bg-muted",
                    )}
                  >
                    <div className="flex items-center gap-2">
                      <span className="min-w-0 flex-1 truncate font-mono text-xs font-medium" title={j.name}>
                        {j.name}
                      </span>
                      <StatusBadge value={j.state} />
                    </div>
                    <div className="flex items-center gap-2 text-xs text-muted-foreground">
                      {j.verb && (
                        <Badge variant="outline" className="font-mono font-normal">
                          {j.verb}
                        </Badge>
                      )}
                      <span className="min-w-0 flex-1 truncate">{j.owner}</span>
                      <Time iso={j.startedAt} />
                      {(j.endedAt || j.state === "Running") && (
                        <span className="tabular-nums">· {duration(j.startedAt, j.endedAt)}</span>
                      )}
                    </div>
                  </a>
                </li>
              )
            })}
          </ul>
        ) : (
          <Empty icon={ListChecks}>No Job managed by rondo-controller.</Empty>
        )}
      </Card>
      <LogPanel
        className="h-[32rem] xl:sticky xl:top-20 xl:h-[calc(100svh-12rem)]"
        path={selected ? `api/logs?job=${encodeURIComponent(selected)}&tail=500` : null}
        title={selected ?? undefined}
        subtitle={job ? [job.verb, job.owner, job.state].filter(Boolean).join(" · ") : undefined}
        onClose={() => (window.location.hash = href("jobs"))}
        placeholder="Select a Job to show its logs."
      />
    </div>
  )
}
