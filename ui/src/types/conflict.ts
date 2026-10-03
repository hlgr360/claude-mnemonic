// Types for the conflict review API (/api/conflicts/*).
import type { Observation } from './observation'

/** What a person can decide about a proposed pair. */
export type ConflictDecision = 'supersede_older' | 'supersede_newer' | 'keep_both'

export type ConflictStatus = 'open' | 'resolved' | 'all'

export type ConflictConfidence = 'low' | 'medium' | 'high'

/** A proposed pair: an older note that a newer one may have made out of date. */
export interface Conflict {
  id: number
  older_obs_id: number
  newer_obs_id: number
  older: Observation
  newer: Observation
  conflict_type: string
  /** What the proposer called the pair: supersedes, contradicts, duplicate or manual. */
  relation?: string
  confidence?: ConflictConfidence
  /** Who proposed it: "llm" or "manual". */
  proposer?: string
  reason?: string
  resolved: boolean
  /** Set once decided. */
  decision?: ConflictDecision
  /** The note the decision hid, when it hid one. */
  superseded_obs_id?: number
  detected_at_epoch: number
  resolved_at_epoch?: number
  /** When the hidden note will be deleted, only present when a retention is configured. */
  restorable_until_epoch?: number
}

export interface ConflictList {
  conflicts: Conflict[]
  /** How many match the filter. */
  total: number
  /** How many wait for a decision, within the same project filter. */
  open_count: number
}
