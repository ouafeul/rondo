import { Server } from "lucide-react"
import { Dash, Empty, Field, Message, PageSkeleton, StatusBadge } from "@/components/common"
import { Badge } from "@/components/ui/badge"
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useApi } from "@/hooks/use-api"
import type { TargetView } from "@/lib/types"

function TargetCard({ t }: { t: TargetView }) {
  const values = Object.entries(t.values ?? {})
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Server className="size-4 text-muted-foreground" />
          {t.name}
        </CardTitle>
        <CardDescription className="truncate font-mono text-xs" title={t.server}>
          {t.inCluster ? "in-cluster" : t.server || "—"}
        </CardDescription>
        <CardAction>
          <StatusBadge value={t.ready} />
        </CardAction>
      </CardHeader>
      <CardContent className="space-y-4">
        <Message text={t.ready !== "True" ? t.message : undefined} />
        <dl className="grid grid-cols-3 gap-3">
          <Field label="Type">
            <Badge variant="outline">{t.type}</Badge>
          </Field>
          <Field label="Access">{t.inCluster ? "local" : "remote"}</Field>
          <Field label="Roles">{t.roles?.length ? t.roles.join(", ") : <Dash />}</Field>
        </dl>
        <div>
          <h4 className="mb-1.5 text-xs text-muted-foreground">Values</h4>
          {values.length ? (
            <dl className="divide-y rounded-md border text-xs">
              {values.map(([k, v]) => (
                <div key={k} className="grid grid-cols-[minmax(8rem,auto)_1fr] gap-3 px-3 py-1.5">
                  <dt className="text-muted-foreground">{k}</dt>
                  <dd className="truncate font-mono" title={v}>
                    {v}
                  </dd>
                </div>
              ))}
            </dl>
          ) : (
            <Dash />
          )}
        </div>
      </CardContent>
    </Card>
  )
}

export function Targets() {
  const { data } = useApi<TargetView[]>("api/targets")
  if (!data) return <PageSkeleton />
  if (!data.length) return <Empty icon={Server}>No Target.</Empty>
  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2 2xl:grid-cols-3">
      {data.map((t) => (
        <TargetCard key={t.name} t={t} />
      ))}
    </div>
  )
}
