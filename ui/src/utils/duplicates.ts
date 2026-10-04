// Wording of the "Possible duplicates" section of the project manager. Free of Vue and of path aliases so it can be
// exercised directly with `node --test` (see tests/).
import type { DuplicateProject, DuplicateSuggestion } from '../types/project.ts'

const STRENGTH: Record<string, { label: string; hint: string }> = {
  strong: { label: 'Same repository', hint: 'Both were cloned from the same git remote.' },
  medium: { label: 'Looks the same', hint: 'The same notes, or one of the folders is gone.' },
  weak: { label: 'Only a hint', hint: 'One of them holds very little. Check before merging.' }
}

export function strengthLabel(strength: string): string {
  return STRENGTH[strength]?.label ?? 'Possible'
}

export function strengthHint(strength: string): string {
  return STRENGTH[strength]?.hint ?? ''
}

export function notesText(n: number): string {
  return n === 1 ? '1 note' : `${n} notes`
}

/** "shop (40 notes)" */
export function projectLine(p: DuplicateProject): string {
  return `${p.label || p.project} (${notesText(p.observations)})`
}

/** What merging the suggestion does, as a sentence. */
export function mergeSentence(s: DuplicateSuggestion): string {
  return `Move ${s.other.project} into ${s.survivor.project}`
}

/** The same suggestion the other way round: keep the other project instead. */
export function swapped(s: DuplicateSuggestion): DuplicateSuggestion {
  return { ...s, survivor: s.other, other: s.survivor }
}

/** The notice for a pair the worker merged by itself. */
export function autoMergedMessage(ev: { project?: string; into?: string; backup?: string }): string {
  const from = ev.project ?? 'a project'
  const into = ev.into ?? 'another project'
  const backup = ev.backup ? ` A backup of the database before the merge is at ${ev.backup}.` : ''
  return `${from} was merged into ${into} automatically: both came from the same git remote and the old folder is gone. ${from} keeps resolving to ${into}.${backup}`
}
