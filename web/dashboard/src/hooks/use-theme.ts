import { useEffect, useState } from "react"

export type Theme = "system" | "light" | "dark"

const KEY = "rondo-dashboard-theme"
const dark = window.matchMedia("(prefers-color-scheme: dark)")

export function storedTheme(): Theme {
  try {
    const v = localStorage.getItem(KEY)
    return v === "light" || v === "dark" ? v : "system"
  } catch {
    return "system"
  }
}

export function applyTheme(theme: Theme) {
  const isDark = theme === "dark" || (theme === "system" && dark.matches)
  document.documentElement.classList.toggle("dark", isDark)
  document.documentElement.style.colorScheme = isDark ? "dark" : "light"
}

function save(theme: Theme) {
  try {
    if (theme === "system") localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, theme)
  } catch {
    return
  }
}

export function useTheme() {
  const [theme, setTheme] = useState<Theme>(storedTheme)
  useEffect(() => {
    const apply = () => applyTheme(theme)
    apply()
    save(theme)
    dark.addEventListener("change", apply)
    return () => dark.removeEventListener("change", apply)
  }, [theme])
  return [theme, setTheme] as const
}
