import { useEffect, useState } from 'react'
import './ThemeToggle.css'

type Theme = 'dark' | 'light'
const STORAGE_KEY = 'vibed.theme'

/** initialTheme picks the user's saved choice, falling back to the OS
 *  preference. Runs at module load (not inside the component) so the first
 *  paint matches the eventual state — no white flash on a dark-preference
 *  reload. */
function initialTheme(): Theme {
  if (typeof window === 'undefined') return 'dark'
  const saved = window.localStorage.getItem(STORAGE_KEY) as Theme | null
  if (saved === 'light' || saved === 'dark') return saved
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}

// Apply the resolved theme immediately so the first paint is correct.
// React only takes over reactivity from here; the data attribute is the
// source of truth that the CSS reads.
if (typeof document !== 'undefined') {
  document.documentElement.dataset.theme = initialTheme()
}

/**
 * ThemeToggle is the dark/light switch in the header. A pill with sun on
 * the left and moon on the right; the active half lights up and the knob
 * slides to it. Click anywhere on the pill to flip.
 */
export default function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(() => initialTheme())

  useEffect(() => {
    document.documentElement.dataset.theme = theme
    window.localStorage.setItem(STORAGE_KEY, theme)
  }, [theme])

  const next: Theme = theme === 'dark' ? 'light' : 'dark'
  return (
    <button
      type="button"
      className={`theme-toggle theme-toggle-${theme}`}
      onClick={() => setTheme(next)}
      aria-label={`Switch to ${next} theme`}
      title={`Switch to ${next} theme`}
    >
      <span className="theme-toggle-icon theme-toggle-icon-light" aria-hidden="true">☀</span>
      <span className="theme-toggle-icon theme-toggle-icon-dark" aria-hidden="true">☾</span>
      <span className="theme-toggle-knob" aria-hidden="true" />
    </button>
  )
}
