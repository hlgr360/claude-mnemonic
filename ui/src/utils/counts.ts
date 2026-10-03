// The real totals behind the dashboard's lists. Free of Vue and of path aliases so it can be exercised directly
// with `node --test` (see tests/).

type FetchFn = typeof fetch

/** How many observations, prompts and summaries there are. */
export interface Totals {
  observations: number
  prompts: number
  summaries: number
}

const defaultFetch: FetchFn = (...args) => globalThis.fetch(...args)

/** Reads the totals for one project, or for all of them when project is empty. */
export async function fetchTotals(project?: string | null, fetchFn: FetchFn = defaultFetch): Promise<Totals> {
  const query = project ? `?project=${encodeURIComponent(project)}` : ''
  const response = await fetchFn(`/api/counts${query}`, { cache: 'no-store' })
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}: ${response.statusText}`)
  }
  return response.json() as Promise<Totals>
}

/**
 * The note under the filter tabs when a list holds only the newest part of what exists, for example
 * "Showing the newest 50 of 415 observations and 50 of 372 prompts." Empty when every list is complete, or
 * when the totals are not known yet.
 */
export function showingText(shown: Totals, totals: Totals | null): string {
  if (!totals) return ''
  const parts: string[] = []
  const add = (n: number, total: number, noun: string) => {
    if (total > n) parts.push(`${n} of ${total} ${noun}`)
  }
  add(shown.observations, totals.observations, 'observations')
  add(shown.prompts, totals.prompts, 'prompts')
  add(shown.summaries, totals.summaries, 'summaries')
  if (parts.length === 0) return ''
  const list = parts.length === 1 ? parts[0] : `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}`
  return `Showing the newest ${list}.`
}
