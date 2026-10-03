// Client for the conflict review API. Free of Vue and of path aliases so it can be exercised directly
// with `node --test` (see tests/).
import type { Conflict, ConflictDecision, ConflictList, ConflictStatus } from '../types/conflict.ts'

type FetchFn = typeof fetch

/** An error answer from the worker, with its HTTP status. */
export class ConflictApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ConflictApiError'
    this.status = status
  }
}

const defaultFetch: FetchFn = (...args) => globalThis.fetch(...args)

async function request<T>(fetchFn: FetchFn, url: string, init?: RequestInit): Promise<T> {
  const response = await fetchFn(url, init)
  if (!response.ok) {
    const text = (await response.text()).trim()
    throw new ConflictApiError(response.status, text || response.statusText)
  }
  return response.json() as Promise<T>
}

export interface ListOptions {
  status?: ConflictStatus
  project?: string | null
  limit?: number
  offset?: number
}

/** Builds the query string of a listing; empty values are left out. */
export function conflictQuery(opts: ListOptions = {}): string {
  const params = new URLSearchParams()
  if (opts.status) params.set('status', opts.status)
  if (opts.project) params.set('project', opts.project)
  if (opts.limit) params.set('limit', String(opts.limit))
  if (opts.offset) params.set('offset', String(opts.offset))
  const q = params.toString()
  return q ? `?${q}` : ''
}

export function listConflicts(opts: ListOptions = {}, fetchFn: FetchFn = defaultFetch): Promise<ConflictList> {
  return request(fetchFn, `/api/conflicts${conflictQuery(opts)}`, { cache: 'no-store' })
}

/** How many proposals wait for a decision. */
export async function countOpenConflicts(project?: string | null, fetchFn: FetchFn = defaultFetch): Promise<number> {
  const body = await request<{ open: number }>(fetchFn, `/api/conflicts/count${conflictQuery({ project })}`, { cache: 'no-store' })
  return body.open
}

const post = (body?: unknown): RequestInit => ({
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: body === undefined ? undefined : JSON.stringify(body)
})

export function resolveConflict(id: number, decision: ConflictDecision, fetchFn: FetchFn = defaultFetch): Promise<Conflict> {
  return request(fetchFn, `/api/conflicts/${id}/resolve`, post({ decision }))
}

export function undoConflict(id: number, fetchFn: FetchFn = defaultFetch): Promise<Conflict> {
  return request(fetchFn, `/api/conflicts/${id}/undo`, post())
}

/** Proposes a pair by hand; the worker puts the two in order by age. */
export function proposeConflict(olderId: number, newerId: number, reason = '', fetchFn: FetchFn = defaultFetch): Promise<Conflict> {
  return request(fetchFn, '/api/conflicts', post({ older_id: olderId, newer_id: newerId, reason }))
}

/** Turns a worker error into a sentence for the person using the dashboard. */
export function describeConflictError(err: unknown): string {
  if (!(err instanceof ConflictApiError)) {
    return err instanceof Error ? err.message : String(err)
  }
  switch (err.status) {
    case 404:
      return 'That proposal no longer exists.'
    case 409:
      return 'Someone already decided this proposal. The list has been refreshed.'
    case 410:
      return 'One of the two notes has been deleted, so this proposal is gone.'
    case 422:
      return err.message
    case 500:
      return `The worker could not complete the action: ${err.message}`
    default:
      return err.message
  }
}
