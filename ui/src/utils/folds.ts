// Client and wording for roll-ups and consolidations. Free of Vue and of path aliases so it can be exercised
// directly with `node --test` (see tests/).
import type { Observation } from '../types/observation.ts'
import type {
  ArchivedList, ConsolidationPlan, DuplicateGroupList, FoldKind, FoldList, RestoreReport, RollupGroup, RollupReport, Fold
} from '../types/fold.ts'

type FetchFn = typeof fetch

/** An error answer from the worker, with its HTTP status. */
export class FoldApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'FoldApiError'
    this.status = status
  }
}

const defaultFetch: FetchFn = (...args) => globalThis.fetch(...args)

async function request<T>(fetchFn: FetchFn, url: string, init?: RequestInit): Promise<T> {
  const response = await fetchFn(url, init)
  if (!response.ok) {
    const text = (await response.text()).trim()
    throw new FoldApiError(response.status, text || response.statusText)
  }
  return response.json() as Promise<T>
}

const post = (body?: unknown): RequestInit => ({
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: body === undefined ? undefined : JSON.stringify(body)
})

export interface FoldListOptions {
  project?: string | null
  kind?: FoldKind | null
  includeUndone?: boolean
  limit?: number
}

/** The query string of a listing; empty values are left out. */
export function foldsQuery(opts: FoldListOptions = {}): string {
  const params = new URLSearchParams()
  if (opts.project) params.set('project', opts.project)
  if (opts.kind) params.set('kind', opts.kind)
  if (opts.includeUndone) params.set('include_undone', 'true')
  if (opts.limit) params.set('limit', String(opts.limit))
  const q = params.toString()
  return q ? `?${q}` : ''
}

export function listFolds(opts: FoldListOptions = {}, fetchFn: FetchFn = defaultFetch): Promise<FoldList> {
  return request(fetchFn, `/api/folds${foldsQuery(opts)}`, { cache: 'no-store' })
}

/** Takes a roll-up or consolidation back: the notes it replaced are live again. */
export function restoreFold(id: number, fetchFn: FetchFn = defaultFetch): Promise<RestoreReport> {
  return request(fetchFn, `/api/folds/${id}/restore`, post())
}

/** Lists the groups of old notes a roll-up of the project would condense. Nothing is written and no model is asked. */
export function previewRollup(project: string, fetchFn: FetchFn = defaultFetch): Promise<RollupReport> {
  return request(fetchFn, `/api/projects/${encodeURIComponent(project)}/rollup`, post({ dry_run: true }))
}

/** Rolls up the project's old notes now: a model condenses each group and the originals are archived. */
export function runRollup(project: string, fetchFn: FetchFn = defaultFetch): Promise<RollupReport> {
  return request(fetchFn, `/api/projects/${encodeURIComponent(project)}/rollup`, post({ dry_run: false }))
}

/** Finds groups of near-duplicate notes in a project (0.5-1.0: how alike by their terms). */
export function findDuplicateGroups(project: string, threshold = 0.85, limit = 200, fetchFn: FetchFn = defaultFetch): Promise<DuplicateGroupList> {
  const params = new URLSearchParams({ project, threshold: String(threshold), limit: String(limit) })
  return request(fetchFn, `/api/observations/duplicates?${params}`, { cache: 'no-store' })
}

/** What consolidating the notes would do, and the token that confirms it. Nothing changes. */
export function previewConsolidation(ids: number[], fetchFn: FetchFn = defaultFetch): Promise<ConsolidationPlan> {
  return request(fetchFn, '/api/observations/consolidate', post({ ids }))
}

/** Applies exactly the previewed consolidation; the worker refuses if the notes changed since. */
export function applyConsolidation(ids: number[], token: string, fetchFn: FetchFn = defaultFetch): Promise<ConsolidationPlan> {
  return request(fetchFn, '/api/observations/consolidate', post({ ids, confirm: token }))
}

export interface ArchivedOptions {
  project?: string | null
  limit?: number
  offset?: number
}

/** The archived notes, the most recently archived first. */
export function listArchived(opts: ArchivedOptions = {}, fetchFn: FetchFn = defaultFetch): Promise<ArchivedList> {
  const params = new URLSearchParams({ archived_only: 'true', limit: String(opts.limit ?? 50), offset: String(opts.offset ?? 0) })
  if (opts.project) params.set('project', opts.project)
  return request(fetchFn, `/api/observations?${params}`, { cache: 'no-store' })
}

/** One note by id, archived or not (it says which). */
export function getNote(id: number, fetchFn: FetchFn = defaultFetch): Promise<Observation> {
  return request(fetchFn, `/api/observations/${id}`, { cache: 'no-store' })
}

/** Puts one archived note back. */
export function unarchiveNote(id: number, fetchFn: FetchFn = defaultFetch): Promise<unknown> {
  return request(fetchFn, `/api/observations/${id}/unarchive`, post())
}

// ---- wording ----

export function notesText(n: number): string {
  return n === 1 ? '1 note' : `${n} notes`
}

export function foldKindLabel(kind: FoldKind): string {
  return kind === 'rollup' ? 'Roll-up' : 'Consolidation'
}

/** One line for the list: what the fold did. */
export function foldHeadline(f: Fold): string {
  const n = f.sources.length
  if (f.kind === 'rollup') return `${notesText(n)} rolled up${f.label ? ` (${f.label})` : ''}`
  return `${notesText(n)} folded into #${f.survivor}`
}

/** The sentence under a restore: what came back, what did not. */
export function restoreSentence(r: RestoreReport): string {
  const parts = [`${notesText(r.restored.length)} live again.`]
  if (r.kind === 'rollup') parts.push(r.survivor_archived ? 'The roll-up was archived.' : 'The roll-up note could not be archived and is still live.')
  if (r.kept.length > 0) {
    parts.push(`${notesText(r.kept.length)} stay archived: ${r.kept.length === 1 ? 'it was' : 'they were'} archived for another reason in the meantime.`)
  }
  return parts.join(' ')
}

/** "2026-03: 10 notes, 2026-03-02 to 2026-03-28" */
export function rollupGroupLine(g: RollupGroup): string {
  const range = g.from === g.to ? g.from : `${g.from} to ${g.to}`
  return `${g.label}: ${notesText(g.ids.length)}, ${range}`
}

/** What a roll-up run did, as a sentence. */
export function rollupOutcome(rep: RollupReport): string {
  const done = rep.groups.filter(g => !g.error)
  const failed = rep.groups.length - done.length
  const archived = done.reduce((sum, g) => sum + g.archived, 0)
  if (rep.groups.length === 0) return 'Nothing to roll up.'
  const parts = [done.length === 0 ? 'No roll-up was written.' : `${done.length === 1 ? '1 roll-up' : `${done.length} roll-ups`} written, ${notesText(archived)} archived.`]
  if (failed > 0) parts.push(`${failed === 1 ? '1 group' : `${failed} groups`} failed; their notes are still live.`)
  if (rep.remaining > 0) parts.push(`${rep.remaining === 1 ? '1 more group is' : `${rep.remaining} more groups are`} left for a later run.`)
  return parts.join(' ')
}

/** "keep #12 "Crane board", archive 2 duplicates" */
export function consolidationSummary(plan: ConsolidationPlan): string {
  const n = plan.duplicates.length
  return `Keep #${plan.survivor.id} “${plan.survivor.title || 'untitled'}” and archive ${n === 1 ? '1 duplicate' : `${n} duplicates`}.`
}

/** What the survivor takes over, one phrase per kind; empty when it takes over nothing but a note of the merge. */
export function consolidationAdds(plan: ConsolidationPlan): string[] {
  const out: string[] = []
  const facts = plan.added_facts.length > 0 ? plan.added_facts.length - 1 : 0 // the last one is the note of the merge
  if (facts > 0) out.push(facts === 1 ? '1 fact' : `${facts} facts`)
  if (plan.added_concepts.length > 0) out.push(plan.added_concepts.length === 1 ? '1 concept' : `${plan.added_concepts.length} concepts`)
  if (plan.added_files.length > 0) out.push(plan.added_files.length === 1 ? '1 file' : `${plan.added_files.length} files`)
  if (plan.relations_to_copy > 0) out.push(plan.relations_to_copy === 1 ? '1 relation' : `${plan.relations_to_copy} relations`)
  return out
}

export function similarityPercent(similarity: number): string {
  return `${Math.round(similarity * 100)}%`
}

/** An archive reason as a short phrase. */
export function archivedReasonText(reason?: string): string {
  if (!reason) return 'Archived'
  const rolled = /^rolled-up into #(\d+)$/.exec(reason)
  if (rolled) return `Rolled up into #${rolled[1]}`
  const merged = /^consolidated into #(\d+)$/.exec(reason)
  if (merged) return `Consolidated into #${merged[1]}`
  if (reason.startsWith('cap:')) return 'Archived by the cap on notes per project'
  if (reason.startsWith('roll-up restored')) return 'A roll-up that was restored'
  return reason
}

/** Turns a worker error into a sentence for the person using the dashboard. */
export function describeFoldError(err: unknown): string {
  if (!(err instanceof FoldApiError)) {
    return err instanceof Error ? err.message : String(err)
  }
  switch (err.status) {
    case 404:
      return 'That roll-up or consolidation no longer exists.'
    case 409:
      return err.message.includes('preview') ? 'The notes changed since the preview. Review them again.' : 'That was already done, or is being done right now. The list has been refreshed.'
    case 422:
      return err.message
    case 502:
      return `The model could not write the roll-up: ${err.message}`
    case 503:
      return 'No model is available to write a roll-up (no Claude CLI and no local model). Nothing was changed.'
    case 500:
      return `The worker could not complete the action: ${err.message}`
    default:
      return err.message
  }
}
