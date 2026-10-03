// Client for the project management API. Free of Vue and of path aliases so it
// can be exercised directly with `node --test` (see tests/).
import type { ProjectActionResult, ProjectAlias, ProjectSummary } from '../types/project.ts'

type FetchFn = typeof fetch

/** An error answer from the worker, with its HTTP status. */
export class AdminError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'AdminError'
    this.status = status
  }
}

const defaultFetch: FetchFn = (...args) => globalThis.fetch(...args)

async function request<T>(fetchFn: FetchFn, url: string, init?: RequestInit): Promise<T> {
  const response = await fetchFn(url, init)
  if (!response.ok) {
    const text = (await response.text()).trim()
    throw new AdminError(response.status, text || response.statusText)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

const projectUrl = (id: string) => `/api/projects/${encodeURIComponent(id)}`

const jsonPost = (body: unknown): RequestInit => ({
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body)
})

export function listProjects(fetchFn: FetchFn = defaultFetch): Promise<ProjectSummary[]> {
  return request(fetchFn, '/api/projects/summary', { cache: 'no-store' })
}

export function listAliases(fetchFn: FetchFn = defaultFetch): Promise<ProjectAlias[]> {
  return request(fetchFn, '/api/projects/aliases', { cache: 'no-store' })
}

export function removeAlias(alias: string, fetchFn: FetchFn = defaultFetch): Promise<void> {
  return request(fetchFn, `/api/projects/aliases/${encodeURIComponent(alias)}`, { method: 'DELETE' })
}

/** Without a confirm token the worker only previews and returns the token to send back. */
export function deleteProject(id: string, confirm?: string, fetchFn: FetchFn = defaultFetch): Promise<ProjectActionResult> {
  const query = confirm ? `?confirm=${encodeURIComponent(confirm)}` : ''
  return request(fetchFn, `${projectUrl(id)}${query}`, { method: 'DELETE' })
}

/** Without a confirm token the worker only previews and returns the token to send back. */
export function mergeProject(id: string, into: string, confirm?: string, fetchFn: FetchFn = defaultFetch): Promise<ProjectActionResult> {
  return request(fetchFn, `${projectUrl(id)}/merge`, jsonPost({ into, confirm: confirm ?? '' }))
}

/** Turns a worker error into a sentence for the person using the dashboard. */
export function describeError(err: unknown): string {
  if (!(err instanceof AdminError)) {
    return err instanceof Error ? err.message : String(err)
  }
  switch (err.status) {
    case 409:
      if (/confirmation does not match/i.test(err.message)) {
        return 'The project changed since the preview. Review the new numbers and confirm again.'
      }
      return err.message
    case 404:
      return 'That project no longer exists.'
    case 422:
      return err.message
    case 500:
      return `The worker could not complete the action: ${err.message}`
    default:
      return err.message
  }
}
