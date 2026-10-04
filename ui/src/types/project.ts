// Types for the project management API (/api/projects/*).

export interface ProjectSummary {
  project: string
  display_name: string
  /** How to show the project to a person: the name when it is unique, otherwise the name with what tells namesakes apart. */
  label?: string
  sessions: number
  observations: number
  /** Session summaries; a project can hold only these. */
  summaries?: number
  last_active_epoch: number
  /** Set when this id has been declared an alias of another project. */
  alias_of?: string
  /** Ids that resolve to this project. */
  aliases?: string[]
}

export interface ProjectAlias {
  alias: string
  canonical: string
  source: string
  created_at: string
  created_at_epoch: number
}

export interface ProjectStats {
  project: string
  aliases?: string[]
  sessions: number
  observations: number
  archived_observations: number
  summaries: number
  prompts: number
  relations: number
  conflicts: number
  vectors: number
  patterns: number
}

/** Answer to a delete or merge: a preview (dry_run, with a confirm token) or the executed result (with a backup path). */
export interface ProjectActionResult {
  action: 'delete' | 'merge'
  project: string
  into?: string
  dry_run: boolean
  confirm?: string
  backup?: string
  message: string
  stats: ProjectStats
}

/** One side of a suggested duplicate. */
export interface DuplicateProject {
  project: string
  label: string
  display_name: string
  observations: number
  last_active_epoch: number
}

export interface DuplicateReason {
  code: 'same_remote' | 'same_titles' | 'path_gone' | 'few_notes' | string
  text: string
}

/** Two projects that are probably one. `survivor` has more data; merging moves `other` into it. */
export interface DuplicateSuggestion {
  survivor: DuplicateProject
  other: DuplicateProject
  strength: 'strong' | 'medium' | 'weak'
  reasons: DuplicateReason[]
  /** The strongest evidence: with automatic merging on, the worker merges this pair by itself. */
  auto_mergeable: boolean
}

/** A pair a person said is not the same project. */
export interface DismissedPair {
  a: DuplicateProject
  b: DuplicateProject
}

export interface DuplicatesReply {
  suggestions: DuplicateSuggestion[]
  dismissed: DismissedPair[]
  /** Whether the worker merges the strongest pairs by itself (off by default). */
  auto_merge: boolean
}
