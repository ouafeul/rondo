import { ChevronRight, FileText, Rocket } from "lucide-react"
import { Fragment, useState } from "react"
import { Dash, Empty, Field, Hint, JobLink, Message, PageSkeleton, StatusBadge, Time } from "@/components/common"
import { Card, CardContent } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useApi } from "@/hooks/use-api"
import { href } from "@/hooks/use-route"
import { duration } from "@/lib/format"
import type { AttemptView, EnrollmentView } from "@/lib/types"
import { cn } from "@/lib/utils"

function Stat({ label, value, tone }: { label: string; value: number; tone?: string }) {
  return (
    <Card className="gap-1 px-4 py-3">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className={cn("text-2xl font-semibold tabular-nums", value > 0 && tone)}>{value}</span>
    </Card>
  )
}

function Attempts({ items }: { items: AttemptView[] }) {
  return (
    <ul className="divide-y rounded-md border bg-background">
      {items.map((a) => (
        <li key={a.jobName + a.startedAt} className="space-y-1 px-3 py-2">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
            <StatusBadge value={a.result} />
            <span className="font-mono">{a.version || "—"}</span>
            <Time iso={a.startedAt} />
            {a.finishedAt && (
              <span className="text-muted-foreground tabular-nums">{duration(a.startedAt, a.finishedAt)}</span>
            )}
            <JobLink name={a.jobName} className="ml-auto" />
          </div>
          {a.message && <p className="text-xs break-words text-muted-foreground">{a.message}</p>}
        </li>
      ))}
    </ul>
  )
}

function Details({ r }: { r: EnrollmentView }) {
  const history = r.history ?? []
  return (
    <div className="space-y-4 px-2 py-3">
      <Message text={r.message} tone={r.ready === "False" ? "bad" : "neutral"} />
      <Message text={r.healthMessage} tone={r.health === "Degraded" ? "bad" : "warn"} />
      <dl className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Field label="Component">{r.component}</Field>
        <Field label="Target">{r.target}</Field>
        <Field label="Phase (VM)">{r.phase || <Dash />}</Field>
        <Field label="Values hash">
          <span className="font-mono text-xs" title={r.valuesHash}>
            {r.valuesHash?.slice(0, 16) || "—"}
          </span>
        </Field>
      </dl>
      <div>
        <h4 className="mb-1 text-xs font-medium text-muted-foreground">Attempt history</h4>
        {history.length ? (
          <Attempts items={history} />
        ) : (
          <p className="text-xs text-muted-foreground">No history.</p>
        )}
      </div>
    </div>
  )
}

export function Enrollments() {
  const { data } = useApi<EnrollmentView[]>("api/enrollments")
  const [open, setOpen] = useState<Set<string>>(new Set())
  if (!data) return <PageSkeleton />
  if (!data.length) return <Empty icon={Rocket}>No Enrollment.</Empty>

  const toggle = (name: string) =>
    setOpen((s) => {
      const next = new Set(s)
      if (!next.delete(name)) next.add(name)
      return next
    })

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        <Stat label="Enrollments" value={data.length} />
        <Stat label="Healthy" value={data.filter((r) => r.health === "Healthy").length} />
        <Stat
          label="Degraded"
          value={data.filter((r) => r.health === "Degraded").length}
          tone="text-red-600 dark:text-red-400"
        />
        <Stat
          label="Failing"
          value={data.filter((r) => r.consecutiveFailures > 0).length}
          tone="text-red-600 dark:text-red-400"
        />
      </div>
      <Card className="py-2">
        <CardContent className="px-2">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-8" />
                <TableHead>Enrollment</TableHead>
                <TableHead>Ready</TableHead>
                <TableHead>Health</TableHead>
                <TableHead>Version</TableHead>
                <TableHead className="text-right">Objects</TableHead>
                <TableHead className="text-right">Attempts</TableHead>
                <TableHead className="text-right">Failures</TableHead>
                <TableHead>Last attempt</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.map((r) => {
                const expanded = open.has(r.name)
                return (
                  <Fragment key={r.name}>
                    <TableRow
                      className="cursor-pointer"
                      data-state={expanded ? "selected" : undefined}
                      onClick={() => toggle(r.name)}
                    >
                      <TableCell>
                        <ChevronRight
                          className={cn("size-4 text-muted-foreground transition-transform", expanded && "rotate-90")}
                        />
                      </TableCell>
                      <TableCell>
                        <div className="font-medium">{r.name}</div>
                        <div className="text-xs text-muted-foreground">
                          {r.component} → {r.target}
                        </div>
                      </TableCell>
                      <TableCell>
                        <StatusBadge value={r.ready} />
                      </TableCell>
                      <TableCell>
                        {r.health ? (
                          <StatusBadge value={r.health} />
                        ) : r.phase ? (
                          <StatusBadge value={r.phase} />
                        ) : (
                          <Dash />
                        )}
                      </TableCell>
                      <TableCell className="font-mono text-xs">{r.chartVersion || <Dash />}</TableCell>
                      <TableCell className="text-right tabular-nums">{r.inventoryCount}</TableCell>
                      <TableCell className="text-right tabular-nums">{r.attempts}</TableCell>
                      <TableCell
                        className={cn(
                          "text-right tabular-nums",
                          r.consecutiveFailures > 0 && "font-semibold text-red-600 dark:text-red-400",
                        )}
                      >
                        {r.consecutiveFailures}
                      </TableCell>
                      <TableCell onClick={(e) => e.stopPropagation()}>
                        {r.lastAttempt ? (
                          <div className="flex items-center gap-2">
                            <StatusBadge value={r.lastAttempt.result} />
                            <Time iso={r.lastAttempt.finishedAt || r.lastAttempt.startedAt} className="text-xs" />
                            <Hint label={`Logs of ${r.lastAttempt.jobName}`}>
                              <a
                                href={href("jobs", { job: r.lastAttempt.jobName })}
                                aria-label="Job logs"
                                className="text-muted-foreground hover:text-foreground"
                              >
                                <FileText className="size-4" />
                              </a>
                            </Hint>
                          </div>
                        ) : (
                          <Dash />
                        )}
                      </TableCell>
                    </TableRow>
                    {expanded && (
                      <TableRow className="bg-muted/30 hover:bg-muted/30">
                        <TableCell />
                        <TableCell colSpan={8} className="whitespace-normal">
                          <Details r={r} />
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                )
              })}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  )
}
