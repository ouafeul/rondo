import {
  Activity,
  Boxes,
  LayoutDashboard,
  Lock,
  Monitor,
  Moon,
  Orbit,
  RefreshCw,
  Rocket,
  Server,
  SquareTerminal,
  Sun,
  TriangleAlert,
  type LucideIcon,
} from "lucide-react"
import { useCallback, useMemo, useState, type ComponentType } from "react"
import { Hint } from "@/components/common"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { TooltipProvider } from "@/components/ui/tooltip"
import { POLL_MS, RefreshContext, type FetchResult } from "@/hooks/use-api"
import { href, useRoute } from "@/hooks/use-route"
import { useTheme, type Theme } from "@/hooks/use-theme"
import { cn } from "@/lib/utils"
import { Components } from "@/views/components"
import { Jobs } from "@/views/jobs"
import { Overview } from "@/views/overview"
import { Services } from "@/views/services"
import { Enrollments } from "@/views/enrollments"
import { Targets } from "@/views/targets"

interface View {
  id: string
  label: string
  description: string
  icon: LucideIcon
  group: string
  Page: ComponentType<{ params: URLSearchParams }>
}

const VIEWS: View[] = [
  {
    id: "overview",
    label: "Overview",
    description: "Readiness of every object of the platform.",
    icon: LayoutDashboard,
    group: "Platform",
    Page: Overview,
  },
  {
    id: "components",
    label: "Components",
    description: "What is deployed (Component) and where (Enrollment).",
    icon: Boxes,
    group: "Platform",
    Page: Components,
  },
  {
    id: "enrollments",
    label: "Enrollments",
    description: "A Component on a Target: health, attempts and history.",
    icon: Rocket,
    group: "Platform",
    Page: Enrollments,
  },
  {
    id: "targets",
    label: "Targets",
    description: "The deployment Targets: clusters and vSphere environments.",
    icon: Server,
    group: "Platform",
    Page: Targets,
  },
  {
    id: "jobs",
    label: "Jobs & logs",
    description: "The Jobs rondo-controller launched, and their logs.",
    icon: SquareTerminal,
    group: "Runtime",
    Page: Jobs,
  },
  {
    id: "services",
    label: "Services",
    description: "The RONDO services of the management cluster.",
    icon: Activity,
    group: "Runtime",
    Page: Services,
  },
]

const THEMES: { value: Theme; label: string; icon: LucideIcon }[] = [
  { value: "system", label: "System", icon: Monitor },
  { value: "light", label: "Light", icon: Sun },
  { value: "dark", label: "Dark", icon: Moon },
]

function Brand() {
  return (
    <div className="flex items-center gap-2.5">
      <div className="flex size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
        <Orbit className="size-4" />
      </div>
      <div className="leading-tight">
        <div className="text-sm font-semibold">RONDO</div>
        <div className="text-xs text-muted-foreground">Dashboard</div>
      </div>
    </div>
  )
}

function ThemeSwitch() {
  const [theme, setTheme] = useTheme()
  return (
    <div className="flex rounded-md border bg-background p-0.5">
      {THEMES.map(({ value, label, icon: Icon }) => (
        <Hint key={value} label={`${label} theme`}>
          <button
            type="button"
            onClick={() => setTheme(value)}
            aria-label={`${label} theme`}
            aria-pressed={theme === value}
            className={cn(
              "flex flex-1 items-center justify-center rounded-sm py-1 text-muted-foreground transition-colors hover:text-foreground",
              theme === value && "bg-accent text-foreground",
            )}
          >
            <Icon className="size-3.5" />
          </button>
        </Hint>
      ))}
    </div>
  )
}

function Sidebar({ current }: { current: string }) {
  const groups = [...new Set(VIEWS.map((v) => v.group))]
  return (
    <aside className="sticky top-0 hidden h-svh w-60 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground md:flex">
      <div className="flex h-14 items-center border-b px-4">
        <Brand />
      </div>
      <nav className="flex-1 space-y-5 overflow-y-auto p-3">
        {groups.map((g) => (
          <div key={g} className="space-y-1">
            <div className="px-2 pb-1 text-xs font-medium text-muted-foreground">{g}</div>
            {VIEWS.filter((v) => v.group === g).map((v) => (
              <a
                key={v.id}
                href={href(v.id)}
                aria-current={v.id === current ? "page" : undefined}
                className={cn(
                  "flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm text-muted-foreground transition-colors hover:bg-sidebar-accent/70 hover:text-sidebar-foreground",
                  v.id === current && "bg-sidebar-accent font-medium text-sidebar-accent-foreground",
                )}
              >
                <v.icon className="size-4" />
                {v.label}
              </a>
            ))}
          </div>
        ))}
      </nav>
      <div className="space-y-3 border-t p-3">
        <ThemeSwitch />
        <div className="flex items-center gap-1.5 px-1 text-xs text-muted-foreground">
          <Lock className="size-3" />
          Read-only
        </div>
      </div>
    </aside>
  )
}

export default function App() {
  const route = useRoute()
  const view = VIEWS.find((v) => v.id === route.view) ?? VIEWS[0]
  const [auto, setAuto] = useState(true)
  const [nonce, setNonce] = useState(0)
  const [status, setStatus] = useState<FetchResult & { view?: string }>()
  const onResult = useCallback((r: FetchResult) => setStatus({ ...r, view: view.id }), [view.id])
  const refresh = useMemo(() => ({ auto, nonce, onResult }), [auto, nonce, onResult])
  const current = status?.view === view.id ? status : undefined

  return (
    <RefreshContext.Provider value={refresh}>
      <TooltipProvider>
        <div className="flex min-h-svh">
          <Sidebar current={view.id} />
          <div className="flex min-w-0 flex-1 flex-col">
            <header className="sticky top-0 z-20 border-b bg-background/85 backdrop-blur">
              <div className="flex h-14 items-center gap-3 px-4 md:px-6">
                <div className="md:hidden">
                  <Brand />
                </div>
                <div className="hidden items-center gap-2 text-sm md:flex">
                  <span className="text-muted-foreground">{view.group}</span>
                  <span className="text-muted-foreground/50">/</span>
                  <span className="font-medium">{view.label}</span>
                </div>
                <div className="ml-auto flex items-center gap-3">
                  <span className="hidden text-xs text-muted-foreground tabular-nums sm:inline">
                    {current ? `Updated at ${current.at.toLocaleTimeString("en-GB")}` : "Loading…"}
                  </span>
                  <Hint label={auto ? `Refresh every ${POLL_MS / 1000} s` : "Refresh paused"}>
                    <label className="flex cursor-pointer items-center gap-2 text-sm">
                      <Switch checked={auto} onCheckedChange={setAuto} aria-label="Automatic refresh" />
                      <span className="hidden sm:inline">Auto</span>
                    </label>
                  </Hint>
                  <Hint label="Refresh now">
                    <Button
                      variant="outline"
                      size="icon-sm"
                      onClick={() => setNonce((n) => n + 1)}
                      aria-label="Refresh"
                    >
                      <RefreshCw />
                    </Button>
                  </Hint>
                </div>
              </div>
              <nav className="flex gap-1 overflow-x-auto px-4 pb-2 [scrollbar-width:none] md:hidden">
                {VIEWS.map((v) => (
                  <a
                    key={v.id}
                    href={href(v.id)}
                    className={cn(
                      "flex shrink-0 items-center gap-1.5 rounded-md px-2.5 py-1 text-sm text-muted-foreground",
                      v.id === view.id && "bg-accent font-medium text-foreground",
                    )}
                  >
                    <v.icon className="size-4" />
                    {v.label}
                  </a>
                ))}
              </nav>
            </header>
            <main className="mx-auto w-full max-w-[1600px] flex-1 space-y-6 p-4 md:p-6">
              <div>
                <h1 className="text-2xl font-semibold tracking-tight">{view.label}</h1>
                <p className="mt-1 text-sm text-muted-foreground">{view.description}</p>
              </div>
              {current?.error && (
                <div className="flex items-start gap-3 rounded-lg border border-red-500/30 bg-red-500/5 px-4 py-3 text-sm text-red-700 dark:text-red-400">
                  <TriangleAlert className="mt-0.5 size-4 shrink-0" />
                  <div>
                    <div className="font-medium">Cannot read</div>
                    <div className="text-xs break-words opacity-90">{current.error}</div>
                  </div>
                </div>
              )}
              <view.Page key={view.id} params={route.params} />
            </main>
          </div>
        </div>
      </TooltipProvider>
    </RefreshContext.Provider>
  )
}
