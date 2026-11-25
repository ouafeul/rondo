import { ArrowRight, Boxes } from "lucide-react"
import { Dash, Empty, Field, PageSkeleton, StatusBadge } from "@/components/common"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useApi } from "@/hooks/use-api"
import { lastSegment } from "@/lib/format"
import { archetypeOf } from "@/lib/status"
import type { ComponentView } from "@/lib/types"

function ComponentCard({ c }: { c: ComponentView }) {
  return (
    <Card className="gap-4">
      <CardHeader>
        <CardTitle className="truncate">{c.name}</CardTitle>
        <CardDescription className="truncate font-mono text-xs" title={c.chart}>
          {c.chart ? `${lastSegment(c.chart)}:${c.version ?? "?"}` : `groups: ${(c.groups ?? []).join(", ") || "—"}`}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <dl className="grid grid-cols-2 gap-3">
          <Field label="Archetype">{archetypeOf(c.archetype, c.groups)}</Field>
          <Field label="Namespace">{c.namespace || <Dash />}</Field>
        </dl>
        {!!c.dependsOn?.length && (
          <div className="flex flex-wrap items-center gap-1 text-xs">
            <span className="mr-1 text-muted-foreground">Depends on</span>
            {c.dependsOn.map((d) => (
              <Badge key={d} variant="outline">
                {d}
              </Badge>
            ))}
          </div>
        )}
        <div className="space-y-2 border-t pt-3">
          {c.enrollments.length ? (
            c.enrollments.map((e) => (
              <div key={e.name} className="flex items-center gap-2 text-sm">
                <span className="font-medium">{e.name}</span>
                <ArrowRight className="size-3.5 text-muted-foreground" />
                {e.target ? <Badge variant="secondary">{e.target}</Badge> : <Dash />}
                <StatusBadge value={e.ready} className="ml-auto" />
              </div>
            ))
          ) : (
            <p className="text-xs text-muted-foreground">No Enrollment: this Component is deployed nowhere.</p>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

export function Components() {
  const { data } = useApi<ComponentView[]>("api/components")
  if (!data) return <PageSkeleton />
  if (!data.length) return <Empty icon={Boxes}>No Component declared.</Empty>
  return (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-2 2xl:grid-cols-3">
      {data.map((c) => (
        <ComponentCard key={c.name} c={c} />
      ))}
    </div>
  )
}
