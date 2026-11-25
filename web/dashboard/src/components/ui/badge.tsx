import { cva, type VariantProps } from "class-variance-authority"
import type * as React from "react"
import { cn } from "@/lib/utils"

const badgeVariants = cva(
  "inline-flex w-fit shrink-0 items-center justify-center gap-1.5 overflow-hidden whitespace-nowrap rounded-md border px-2 py-0.5 text-xs font-medium [&>svg]:pointer-events-none [&>svg]:size-3",
  {
    variants: {
      variant: {
        default: "border-transparent bg-primary text-primary-foreground",
        secondary: "border-transparent bg-secondary text-secondary-foreground",
        outline: "text-foreground",
        ok: "border-emerald-500/25 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
        bad: "border-red-500/25 bg-red-500/10 text-red-700 dark:text-red-400",
        warn: "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-400",
        info: "border-sky-500/25 bg-sky-500/10 text-sky-700 dark:text-sky-400",
        neutral: "border-border bg-muted text-muted-foreground",
      },
    },
    defaultVariants: { variant: "default" },
  },
)

export type BadgeVariant = NonNullable<VariantProps<typeof badgeVariants>["variant"]>

function Badge({ className, variant, ...props }: React.ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return <span data-slot="badge" className={cn(badgeVariants({ variant }), className)} {...props} />
}

export { Badge }
