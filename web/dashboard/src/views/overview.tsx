import { Boxes, CircleCheck, Link2, Server, type LucideIcon } from "lucide-react"
import { Dash, Empty, Meter, PageSkeleton, StatusBadge } from "@/components/common"
import { Badge } from "@/components/ui/badge"
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useApi } from "@/hooks/use-api"
import { href } from "@/hooks/use-route"
import type { Summary } from "@/lib/types"

const KINDS: Record<string, { label: string; icon: LucideIcon; view?: string }> = {
  Target: { label: "Targets", icon: Server, view: "targets" },
  Component: { label: "Components", icon: Boxes, view: "components" },
  Enrollment: { label: "Enrollments", icon: Link2, view: "enrollments" },
}

function KindCard({ s }: { s: Summary }) {
  const kind = KINDS[s.kind] ?? { label: s.kind, icon: Boxes }
  const Icon = kind.icon
  const failing = s.total - s.ready
  const card = (
    <Card className="h-full gap-3 px-5 py-4 transition-colors group-hover:border-foreground/20">
      <div className="flex items-center justify-between text-sm text-muted-foreground">
        <span>{kind.label}</span>
        <Icon className="size-4" />
      </div>
      <div className="flex items-baseline gap-1.5">
        <span className="text-3xl font-semibold tracking-tight tabular-nums">{s.ready}</span>
        <span className="text-sm text-muted-foreground tabular-nums">/ {s.total} ready</span>
        <span className="ml-auto">
          {s.total === 0 ? (
            <Badge variant="neutral">empty</Badge>
          ) : failing ? (
            <Badge variant="bad">
              {failing} not ready
            </Badge>
          ) : (
            <Badge variant="ok">OK</Badge>
          )}
        </span>
      </div>
      <Meter value={s.ready} total={s.total} />
    </Card>
  )
  return kind.view ? (
    <a href={href(kind.view)} className="group">
      {card}
    </a>
  ) : (
    card
  )
}

export function Overview() {
  const { data } = useApi<Summary[]>("api/overview")
  if (!data) return <PageSkeleton />

  const issues = data.flatMap((s) => s.rows.filter((r) => r.ready !== "True").map((r) => ({ kind: s.kind, ...r })))

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {data.map((s) => (
          <KindCard key={s.kind} s={s} />
        ))}
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Needs attention</CardTitle>
          <CardDescription>Objects whose Ready condition is not True.</CardDescription>
          <CardAction>
            <Badge variant={issues.length ? "bad" : "ok"}>{issues.length}</Badge>
          </CardAction>
        </CardHeader>
        <CardContent>
          {issues.length ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Kind</TableHead>
                  <TableHead>Name</TableHead>
                  <TableHead>Ready</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead>Message</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {issues.map((r) => (
                  <TableRow key={`${r.kind}/${r.name}`}>
                    <TableCell className="text-muted-foreground">{r.kind}</TableCell>
                    <TableCell className="font-medium">{r.name}</TableCell>
                    <TableCell>
                      <StatusBadge value={r.ready} />
                    </TableCell>
                    <TableCell>{r.phase || <Dash />}</TableCell>
                    <TableCell className="min-w-72 whitespace-normal text-muted-foreground">
                      {r.message || <Dash />}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : (
            <Empty icon={CircleCheck}>All objects are ready.</Empty>
          )}
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        {data.map((s) => (
          <Card key={s.kind} className="gap-2">
            <CardHeader>
              <CardTitle className="text-sm">{s.kind}</CardTitle>
              <CardAction className="text-xs text-muted-foreground tabular-nums">
                {s.ready}/{s.total} Ready
              </CardAction>
            </CardHeader>
            <CardContent className="px-2">
              {s.rows.length ? (
                <Table>
                  <TableBody>
                    {s.rows.map((r) => (
                      <TableRow key={r.name}>
                        <TableCell className="font-medium">{r.name}</TableCell>
                        <TableCell className="w-24">
                          <StatusBadge value={r.ready} />
                        </TableCell>
                        <TableCell
                          className="max-w-56 truncate font-mono text-xs text-muted-foreground"
                          title={r.phase}
                        >
                          {r.phase || ""}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              ) : (
                <Empty>No object.</Empty>
              )}
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  )
}
