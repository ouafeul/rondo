import { Activity } from "lucide-react"
import { Dash, Empty, Field, Hint, Message, PageSkeleton, StatusBadge, Time } from "@/components/common"
import { LogPanel } from "@/components/log-panel"
import { Badge, type BadgeVariant } from "@/components/ui/badge"
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useApi } from "@/hooks/use-api"
import { href } from "@/hooks/use-route"
import type { PodView, ServiceView } from "@/lib/types"
import { cn } from "@/lib/utils"

function podTone(p: PodView): BadgeVariant | undefined {
  if (p.reason) return undefined
  if (p.phase === "Running") return p.ready ? "ok" : "warn"
  return undefined
}

function ServiceCard({ s, selected }: { s: ServiceView; selected: string | null }) {
  const restarts = s.pods.reduce((n, p) => n + p.restarts, 0)
  return (
    <Card className="gap-4">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          {s.name}
          <StatusBadge value={s.state} />
        </CardTitle>
        <CardDescription className="truncate font-mono text-xs" title={s.image}>
          {s.image}
        </CardDescription>
        <CardAction>
          <Badge variant="outline" className="font-mono font-normal">
            {s.version || "?"}
          </Badge>
        </CardAction>
      </CardHeader>
      <CardContent className="space-y-4">
        <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Field label="Ready">
            <span className={cn("font-semibold tabular-nums", s.ready < s.desired && "text-red-600 dark:text-red-400")}>
              {s.ready}/{s.desired}
            </span>
          </Field>
          <Field label="Up to date">
            <span className="tabular-nums">{s.updated}</span>
          </Field>
          <Field label="Available">
            <span className="tabular-nums">{s.available}</span>
          </Field>
          <Field label="Restarts">
            <span className={cn("tabular-nums", restarts > 0 && "font-semibold text-amber-600 dark:text-amber-400")}>
              {restarts}
            </span>
          </Field>
        </dl>
        <Message text={s.message} tone={s.state === "Progressing" ? "warn" : "bad"} />
        {s.pods.length ? (
          <div className="rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Pod</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead className="text-right">Restarts</TableHead>
                  <TableHead>Node</TableHead>
                  <TableHead>Started</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {s.pods.map((p) => {
                  const active = p.name === selected
                  return (
                    <TableRow
                      key={p.name}
                      data-state={active ? "selected" : undefined}
                      className="cursor-pointer"
                      onClick={() => (window.location.hash = href("services", active ? undefined : { pod: p.name }))}
                    >
                      <TableCell className="font-mono text-xs">{p.name}</TableCell>
                      <TableCell>
                        <StatusBadge value={p.reason || p.phase} tone={podTone(p)} />
                      </TableCell>
                      <TableCell
                        className={cn(
                          "text-right tabular-nums",
                          p.restarts > 0 && "font-semibold text-amber-600 dark:text-amber-400",
                        )}
                      >
                        {p.restarts}
                      </TableCell>
                      <TableCell className="text-muted-foreground">{p.node || <Dash />}</TableCell>
                      <TableCell>
                        <Time iso={p.startedAt} />
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">No pod.</p>
        )}
      </CardContent>
    </Card>
  )
}

export function Services({ params }: { params: URLSearchParams }) {
  const { data } = useApi<ServiceView[]>("api/services")
  const selected = params.get("pod")
  if (!data) return <PageSkeleton />
  if (!data.length) return <Empty icon={Activity}>No RONDO service found.</Empty>

  const healthy = data.filter((s) => s.state === "Healthy").length
  const service = data.find((s) => s.pods.some((p) => p.name === selected))

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap gap-2">
        {data.map((s) => (
          <Hint key={s.name} label={`${s.ready}/${s.desired} ready · ${s.version}`}>
            <span>
              <StatusBadge value={s.state} label={s.name} className="text-sm" />
            </span>
          </Hint>
        ))}
        <span className="ml-auto self-center text-sm text-muted-foreground tabular-nums">
          {healthy}/{data.length} services healthy
        </span>
      </div>
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <div className="space-y-4">
          {data.map((s) => (
            <ServiceCard key={s.name} s={s} selected={selected} />
          ))}
        </div>
        <LogPanel
          className="h-[32rem] xl:sticky xl:top-20 xl:h-[calc(100svh-12rem)]"
          path={selected ? `api/logs?pod=${encodeURIComponent(selected)}&tail=500` : null}
          title={selected ?? undefined}
          subtitle={service ? `${service.name} · ${service.version}` : undefined}
          onClose={() => (window.location.hash = href("services"))}
          placeholder="Select a pod to show its logs."
        />
      </div>
    </div>
  )
}
