// The scope of a note and the re-scope of an archive. Free of Vue and of path aliases so it can be exercised
// directly with `node --test` (see tests/).

type FetchFn = typeof fetch

/** Where a note is injected: only into its own project, or into every project. */
export type Scope = 'project' | 'global'

export type ScopeFilter = 'all' | Scope

export interface ScopeChange {
  id: number
  project: string
  title: string
  from: Scope
  to: Scope
}

/** The answer of GET /api/scope/preview and POST /api/scope/apply. */
export interface ScopePlan {
  total: number
  /** Notes whose scope a person or a client chose: never changed. */
  protected: number
  unchanged: number
  to_project: number
  to_global: number
  changed: number
  dry_run: boolean
  confirm?: string
  backup?: string
  message: string
  sample: ScopeChange[]
}

const defaultFetch: FetchFn = (...args) => globalThis.fetch(...args)

async function request<T>(fetchFn: FetchFn, url: string, init?: RequestInit): Promise<T> {
  const response = await fetchFn(url, init)
  if (!response.ok) {
    const text = (await response.text()).trim()
    const error = new Error(text || `HTTP ${response.status}: ${response.statusText}`) as Error & { status: number }
    error.status = response.status
    throw error
  }
  return response.json() as Promise<T>
}

/** What applying the current scope rule to the archive would change. Changes nothing. */
export function fetchScopePreview(fetchFn: FetchFn = defaultFetch): Promise<ScopePlan> {
  return request(fetchFn, '/api/scope/preview', { cache: 'no-store' })
}

/** Applies the rule; the token is the one a preview returned. A backup of the database is taken first. */
export function applyScope(confirm: string, fetchFn: FetchFn = defaultFetch): Promise<ScopePlan> {
  return request(fetchFn, '/api/scope/apply', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ confirm })
  })
}

/** Sets one note's scope by hand. A scope chosen this way is never changed by a re-scope. */
export function setObservationScope(id: number, scope: Scope, fetchFn: FetchFn = defaultFetch): Promise<unknown> {
  return request(fetchFn, `/api/observations/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ scope })
  })
}

export const otherScope = (scope: Scope): Scope => (scope === 'global' ? 'project' : 'global')

export const scopeLabel = (scope: Scope | undefined): string => (scope === 'global' ? 'Global' : 'Project')

/** What the scope means, for a tooltip. */
export function scopeHint(scope: Scope | undefined): string {
  return scope === 'global'
    ? 'Global: shown to sessions of every project. Click to keep it in its own project.'
    : 'Project: only shown to sessions of its own project. Click to share it with every project.'
}

/** Keeps the items of the chosen scope. Items that have no scope (prompts, summaries) are left out when a scope is chosen. */
export function filterByScope<T extends { itemType: string; scope?: Scope }>(items: T[], filter: ScopeFilter): T[] {
  if (filter === 'all') return items
  return items.filter(i => i.itemType === 'observation' && (i.scope ?? 'project') === filter)
}

/** A sentence about a preview, for the top of the review dialog. */
export function describePlan(plan: ScopePlan): string {
  const changes = plan.to_project + plan.to_global
  if (changes === 0) return `Nothing to change: every one of the ${plan.total} notes already has the scope the rule gives it.`
  const parts: string[] = []
  if (plan.to_project) parts.push(`${plan.to_project} to project`)
  if (plan.to_global) parts.push(`${plan.to_global} to global`)
  const kept = plan.protected ? ` ${plan.protected} keep a scope that was chosen by hand.` : ''
  return `${changes} of ${plan.total} notes would change scope (${parts.join(', ')}).${kept}`
}
