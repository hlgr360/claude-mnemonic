// Wording and small decisions of the conflict review panel. Free of Vue and of path aliases so it can be
// exercised directly with `node --test` (see tests/).
import type { Conflict, ConflictConfidence, ConflictDecision } from '../types/conflict.ts'

export interface DecisionOption {
  decision: ConflictDecision
  label: string
  /** What it does, in a sentence. */
  hint: string
  /** The keyboard shortcut. */
  key: string
}

export const DECISIONS: DecisionOption[] = [
  {
    decision: 'supersede_older',
    label: 'Newer replaces older',
    hint: 'The older note is hidden from sessions and search. It stays in the dashboard and can be restored.',
    key: '1'
  },
  {
    decision: 'supersede_newer',
    label: 'Older replaces newer',
    hint: 'The newer note is hidden from sessions and search. It stays in the dashboard and can be restored.',
    key: '2'
  },
  {
    decision: 'keep_both',
    label: 'Keep both',
    hint: 'The two do not conflict. Nothing is hidden and this pair is not proposed again.',
    key: '3'
  }
]

export function decisionForKey(key: string): DecisionOption | undefined {
  return DECISIONS.find(d => d.key === key)
}

export function decisionLabel(decision: ConflictDecision | undefined): string {
  return DECISIONS.find(d => d.decision === decision)?.label ?? ''
}

export function relationLabel(relation: string | undefined): string {
  switch (relation) {
    case 'supersedes':
      return 'The newer note seems to replace the older one'
    case 'contradicts':
      return 'The two notes contradict each other'
    case 'duplicate':
      return 'The two notes say the same thing'
    case 'manual':
      return 'Proposed by you'
    default:
      return 'A possible conflict'
  }
}

/** A short word for the list. */
export function relationShort(relation: string | undefined): string {
  switch (relation) {
    case 'supersedes':
      return 'Outdated'
    case 'contradicts':
      return 'Contradiction'
    case 'duplicate':
      return 'Duplicate'
    case 'manual':
      return 'Manual'
    default:
      return 'Conflict'
  }
}

const CONFIDENCE_ORDER: Record<string, number> = { high: 3, medium: 2, low: 1 }

export function confidenceRank(confidence: ConflictConfidence | string | undefined): number {
  return CONFIDENCE_ORDER[confidence ?? ''] ?? 0
}

/** Which note a decided proposal hid, if any. */
export function hiddenSide(c: Conflict): 'older' | 'newer' | null {
  if (!c.resolved || !c.superseded_obs_id) return null
  if (c.superseded_obs_id === c.older_obs_id) return 'older'
  if (c.superseded_obs_id === c.newer_obs_id) return 'newer'
  return null
}

/** A sentence about what a decided proposal did. */
export function resolutionSummary(c: Conflict): string {
  switch (hiddenSide(c)) {
    case 'older':
      return 'The older note is hidden.'
    case 'newer':
      return 'The newer note is hidden.'
    default:
      return c.decision === 'keep_both' ? 'Both notes are kept.' : ''
  }
}

const DAY = 24 * 60 * 60 * 1000

/** When a hidden note will be deleted, in words; empty when nothing will be deleted. */
export function restorableText(untilEpoch: number | undefined, now: number): string {
  if (!untilEpoch) return ''
  const left = untilEpoch - now
  if (left <= 0) return 'Will be deleted at the next cleanup.'
  const days = Math.ceil(left / DAY)
  if (days <= 1) return 'Will be deleted within a day.'
  return `Will be deleted in ${days} days.`
}

/**
 * Which proposal to show after the one with `removedId` left the list: the one that moves into its place, or
 * the last one when it was the last. `ids` is the list as it was before.
 */
export function nextSelection(ids: number[], removedId: number): number | null {
  const at = ids.indexOf(removedId)
  if (at < 0) return ids[0] ?? null
  return ids[at + 1] ?? ids[at - 1] ?? null
}

/** Moves a selection through the list by `delta`, staying inside it. */
export function moveSelection(ids: number[], current: number | null, delta: number): number | null {
  if (ids.length === 0) return null
  const at = current === null ? -1 : ids.indexOf(current)
  if (at < 0) return ids[delta < 0 ? ids.length - 1 : 0]
  return ids[Math.min(ids.length - 1, Math.max(0, at + delta))]
}

/** The list entry for a proposal: the newer note's title, or its id when it has none. */
export function conflictTitle(c: Conflict): string {
  return c.newer.title?.trim() || `Note #${c.newer.id}`
}
