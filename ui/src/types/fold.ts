// Types for roll-ups and consolidations (/api/folds, /api/projects/{id}/rollup, /api/observations/consolidate).
import type { Observation } from './observation'

/** A roll-up condenses old notes into a new one; a consolidation folds near-duplicates into one of them. */
export type FoldKind = 'rollup' | 'consolidation'

/** One roll-up or consolidation that happened: the note that stands for the others, and the notes it replaced. */
export interface Fold {
  id: number
  kind: FoldKind
  project: string
  /** A roll-up's group, for example "2026-03". */
  label: string
  /** The roll-up note, or the surviving note of a consolidation. */
  survivor: number
  survivor_title: string
  sources: number[]
  created_epoch: number
  undone: boolean
  undone_epoch?: number
}

export interface FoldList {
  folds: Fold[]
}

/** What restoring a fold did. */
export interface RestoreReport {
  kind: FoldKind
  fold_id: number
  survivor: number
  /** A roll-up is archived again when its notes come back; a consolidation's survivor stays. */
  survivor_archived: boolean
  restored: number[]
  /** Notes that stay archived because they were archived for another reason in the meantime. */
  kept: number[]
}

export interface RollupGroup {
  label: string
  from: string
  to: string
  ids: number[]
  /** Why the group was not rolled up, when it was not. */
  error?: string
  rollup_id?: number
  fold_id?: number
  archived: number
}

/** The answer to a roll-up request: a preview (dry_run) lists the groups, a run says what happened to each. */
export interface RollupReport {
  project: string
  dry_run: boolean
  groups: RollupGroup[]
  candidates: number
  remaining: number
}

export interface ConsolidationNote {
  id: number
  title: string
  type: string
  created_epoch: number
  importance: number
  /** A decision, a rated note or one saved on purpose: never consolidated automatically. */
  protected: boolean
}

/** What consolidating a group would do, and (applied) what it did. */
export interface ConsolidationPlan {
  project: string
  /** Confirms the plan: sent back to apply exactly this. */
  token: string
  survivor: ConsolidationNote
  duplicates: ConsolidationNote[]
  added_facts: string[]
  added_concepts: string[]
  added_files: string[]
  relations_to_copy: number
  applied: boolean
  fold_id?: number
}

/** A group of near-duplicate notes as the duplicates endpoint lists them. */
export interface DuplicateGroup {
  observations: Observation[]
  /** The lowest similarity within the group (1 is identical). */
  similarity: number
}

export interface DuplicateGroupList {
  duplicate_groups: DuplicateGroup[]
  total_checked: number
}

export interface ArchivedList {
  observations: Observation[]
  total: number
}
