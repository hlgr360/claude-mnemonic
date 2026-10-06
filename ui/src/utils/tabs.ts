// The tabs of the dashboard's timeline, and the one it opens on. Free of Vue and of path aliases so it can be
// exercised directly with `node --test` (see tests/).
import type { FilterType } from '../types/api.ts'

export interface FilterTab {
  key: FilterType
  label: string
  icon: string
}

export const FILTER_TABS: FilterTab[] = [
  { key: 'all', label: 'All', icon: 'fa-layer-group' },
  { key: 'observations', label: 'Observations', icon: 'fa-brain' },
  { key: 'summaries', label: 'Summaries', icon: 'fa-clipboard-list' },
  { key: 'prompts', label: 'Prompts', icon: 'fa-comment' },
  { key: 'graph', label: 'Graph', icon: 'fa-diagram-project' },
  { key: 'conflicts', label: 'Conflicts', icon: 'fa-code-compare' },
  { key: 'folds', label: 'Roll-ups', icon: 'fa-boxes-stacked' }
]

/** The tab the dashboard opens on: the session summaries, the shortest way to see what has been going on. */
export const DEFAULT_FILTER: FilterType = 'summaries'
