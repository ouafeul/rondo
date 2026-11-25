import { readFileSync } from "node:fs"
import type { ServerResponse } from "node:http"
import type { Plugin } from "vite"
import { api, EXPIRED_JOBS, GROWING_LOGS, jobs, LOG_FILES, services } from "./data.ts"

const started = Date.now()
const TEXT = "text/plain; charset=utf-8"

function send(res: ServerResponse, status: number, type: string, body: string) {
  res.statusCode = status
  res.setHeader("Content-Type", type)
  res.setHeader("Cache-Control", "no-store")
  res.end(body)
}

function genericLog(name: string): string {
  const job = jobs().find((j) => j.name === name)
  if (job?.verb === "helm-apply") {
    const release = api.enrollments().find((r) => `Enrollment/${r.name}` === job.owner)
    return [
      " INFO archive downloaded  bytes=41872",
      " INFO secrets injected into the values  tokens=1",
      `Release "${release?.component}" has been upgraded. Happy Helming!`,
      "STATUS: deployed",
      `REVISION: ${release?.attempts ?? 1}`,
      ` INFO status published  objects=${release?.inventoryCount ?? 0} health=${release?.health ?? "Unknown"}`,
    ].join("\n")
  }
  if (job) {
    return [
      " INFO archive downloaded  bytes=9312",
      "OpenTofu has been successfully initialized!",
      "Apply complete! Resources: 3 added, 0 changed, 0 destroyed.",
    ].join("\n")
  }
  return [
    ` INFO ${name.split("-").slice(0, 2).join("-")} started  namespace=rondo-system`,
    " INFO reconciliation done  kind=Enrollment name=cert-manager-rke2-a",
    " INFO reconciliation done  kind=Target name=rke2-a",
  ].join("\n")
}

function logs(query: URLSearchParams): [number, string] {
  const job = query.get("job")
  const pod = query.get("pod")
  const tail = Math.min(Number(query.get("tail")) || 500, 2000)
  if (!job && !pod) return [400, "parameter `job` or `pod` required"]
  if (job) {
    const j = jobs().find((x) => x.name === job)
    if (!j) return [404, `Job "${job}" unknown in rondo-system`]
    if (EXPIRED_JOBS.has(job)) return [502, `no pod for Job ${job} (TTL expired?)`]
    if (j.state === "Pending")
      return [502, `container "exec" in pod "${job}-7xk2p" is waiting to start: ContainerCreating`]
  } else {
    const p = services()
      .flatMap((s) => s.pods)
      .find((x) => x.name === pod)
    if (!p) return [404, `pod "${pod}" unknown in rondo-system`]
    if (p.phase === "Pending") return [502, `container "${pod}" is waiting to start: ${p.reason}`]
  }
  const name = (job ?? pod)!
  const file = LOG_FILES[name]
  let lines = (file ? readFileSync(new URL(`./logs/${file}`, import.meta.url), "utf8") : genericLog(name)).split("\n")
  if (GROWING_LOGS.has(name)) lines = lines.slice(0, 12 + Math.floor((Date.now() - started) / 2000))
  return [200, lines.slice(-tail).join("\n")]
}

// Dev server only: answers /api/* from the fixtures in data.ts.
export function mockApi(): Plugin {
  return {
    name: "rondo-mock-api",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use("/api", (req, res) => {
        if (req.method !== "GET") return send(res, 405, TEXT, "Method Not Allowed\n")
        const url = new URL(req.url ?? "/", "http://mock")
        const route = url.pathname.replace(/^\/|\/$/g, "")
        if (route === "logs") {
          const [status, body] = logs(url.searchParams)
          return send(res, status, TEXT, `${body}\n`)
        }
        const handler = api[route as keyof typeof api]
        if (!handler) return send(res, 404, TEXT, "404 page not found\n")
        send(res, 200, "application/json", JSON.stringify(handler()))
      })
    },
  }
}
