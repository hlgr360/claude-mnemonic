// The query string of the dashboard's request for observations. Free of Vue and of path aliases so it can be exercised
// directly with `node --test` (see tests/).
//
// The timeline is chronological, so it asks the worker for the NEWEST observations. Without a sort the worker picks
// the most important ones: a note saved a moment ago starts at importance 1 and is behind every older, higher-scored
// note, so on a page of 50 (all projects) or 100 (one project) it can be missing altogether, although it is saved and a
// search finds it. The panels that want the most important notes use their own endpoints (/observations/top).

export type ObservationSort = 'date' | 'importance'

export function observationsQuery(limit: number, project?: string, sort: ObservationSort = 'date'): string {
  const params = new URLSearchParams({ limit: String(limit) })
  if (project) params.append('project', project)
  params.append('sort', sort)
  return params.toString()
}
