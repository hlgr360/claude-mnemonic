// What the project dropdown shows for each project. Free of Vue and of path aliases so it can be
// exercised directly with `node --test` (see tests/).
import type { ProjectSummary } from '../types/project.ts'

export interface ProjectOption {
  /** The project id: what the rest of the dashboard filters by. */
  id: string
  /** The name without its hash. */
  name: string
  /** What to show: the name, or for projects that share a name the name with what tells them apart. */
  text: string
  /** Several projects share this name, so the id is shown as well. */
  namesake: boolean
  /** A short remark such as "summaries only" or "alias of x"; empty when there is nothing to add. */
  note: string
  observations: number
}

/** One option per project, alphabetical by name. The hash is only part of what is shown for namesakes. */
export function buildProjectOptions(rows: ProjectSummary[]): ProjectOption[] {
  const nameOf = (id: string) => rows.find(r => r.project === id)?.display_name || id
  const options = rows.map<ProjectOption>(r => {
    const name = r.display_name || r.project
    const text = r.label || name
    let note = ''
    if (r.alias_of) {
      note = `alias of ${nameOf(r.alias_of)}`
    } else if (r.observations === 0 && r.sessions === 0 && (r.summaries ?? 0) > 0) {
      note = 'summaries only'
    }
    return { id: r.project, name, text, namesake: text !== name, note, observations: r.observations }
  })
  return options.sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()) || a.id.localeCompare(b.id))
}

/** Search matches the name, the displayed text and the id, so a hash can still be pasted in. */
export function optionMatches(option: ProjectOption, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  return [option.name, option.text, option.id].some(v => v.toLowerCase().includes(q))
}

/** What the filter button says for the selected project id (or "All Projects"). */
export function selectedText(options: ProjectOption[], id: string | null): string {
  if (!id) return 'All Projects'
  const found = options.find(o => o.id === id)
  return found ? found.text : id.split('/').pop() || id
}
