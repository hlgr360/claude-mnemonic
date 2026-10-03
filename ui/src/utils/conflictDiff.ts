// Word-level and field-level comparison of two observations, for the conflict review's side-by-side view.
// Free of Vue and of path aliases so it can be exercised directly with `node --test` (see tests/).
import type { Observation } from '../types/observation.ts'

/** A run of text; `changed` says it is not in the other note. */
export interface Segment {
  text: string
  changed: boolean
}

export interface ListItem {
  text: string
  changed: boolean
}

interface RowBase {
  key: string
  label: string
  /** The two sides say the same. */
  same: boolean
}

export interface TextRow extends RowBase {
  kind: 'text'
  left: Segment[]
  right: Segment[]
}

export interface ListRow extends RowBase {
  kind: 'list'
  left: ListItem[]
  right: ListItem[]
}

export type FieldRow = TextRow | ListRow

/** Past this many table cells the word diff is not worth the time: the whole text is shown as changed. */
const MAX_CELLS = 250_000

const isSpace = (token: string) => token.trim() === ''

/** Splits text into words and the whitespace between them, so joining the tokens gives the text back. */
export function tokenize(text: string): string[] {
  return text.split(/(\s+)/).filter(t => t !== '')
}

// Tokens are compared by their word; any whitespace equals any other.
const key = (token: string) => (isSpace(token) ? ' ' : token)

function mergeSegments(tokens: string[], flags: boolean[]): Segment[] {
  // A space between two changed words belongs to the change; any other space does not.
  const marked = flags.map((changed, i) => {
    if (!isSpace(tokens[i])) return changed
    return i > 0 && i < tokens.length - 1 && flags[i - 1] && flags[i + 1]
  })
  const out: Segment[] = []
  tokens.forEach((text, i) => {
    const last = out[out.length - 1]
    if (last && last.changed === marked[i]) last.text += text
    else out.push({ text, changed: marked[i] })
  })
  return out
}

/** Compares two texts word by word. Words found in both (in the same order) are unchanged. */
export function diffWords(a: string, b: string): { left: Segment[]; right: Segment[]; same: boolean } {
  const left = a.trim()
  const right = b.trim()
  if (left === right) {
    const whole = left === '' ? [] : [{ text: left, changed: false }]
    return { left: whole, right: whole.map(s => ({ ...s })), same: true }
  }
  const ta = tokenize(left)
  const tb = tokenize(right)
  const n = ta.length
  const m = tb.length
  if (n === 0 || m === 0 || (n + 1) * (m + 1) > MAX_CELLS) {
    return {
      left: n ? [{ text: left, changed: true }] : [],
      right: m ? [{ text: right, changed: true }] : [],
      same: false
    }
  }

  // Longest common subsequence of the tokens.
  const width = m + 1
  const table = new Uint32Array((n + 1) * width)
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      table[i * width + j] = key(ta[i]) === key(tb[j])
        ? table[(i + 1) * width + j + 1] + 1
        : Math.max(table[(i + 1) * width + j], table[i * width + j + 1])
    }
  }
  const fa = new Array<boolean>(n).fill(true)
  const fb = new Array<boolean>(m).fill(true)
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (key(ta[i]) === key(tb[j])) {
      fa[i] = false
      fb[j] = false
      i++
      j++
    } else if (table[(i + 1) * width + j] >= table[i * width + j + 1]) {
      i++
    } else {
      j++
    }
  }
  return { left: mergeSegments(ta, fa), right: mergeSegments(tb, fb), same: false }
}

const norm = (s: string) => s.trim().toLowerCase()

/** Compares two lists of short strings: an item the other list does not have is changed. */
export function diffList(a: string[], b: string[]): { left: ListItem[]; right: ListItem[]; same: boolean } {
  const clean = (xs: string[]) => xs.map(x => x.trim()).filter(x => x !== '')
  const la = clean(a)
  const lb = clean(b)
  const inA = new Set(la.map(norm))
  const inB = new Set(lb.map(norm))
  const left = la.map(text => ({ text, changed: !inB.has(norm(text)) }))
  const right = lb.map(text => ({ text, changed: !inA.has(norm(text)) }))
  return { left, right, same: !left.some(x => x.changed) && !right.some(x => x.changed) }
}

const list = (xs: string[] | null | undefined) => xs ?? []

/** The day a note was saved, in UTC so that two people see the same text. Accepts seconds or milliseconds. */
export function formatSaved(epoch: number): string {
  if (!epoch) return ''
  const ms = epoch < 1e11 ? epoch * 1000 : epoch
  return new Date(ms).toISOString().slice(0, 10)
}

function textRow(key: string, label: string, a: string, b: string, highlight = true): TextRow | null {
  if (a.trim() === '' && b.trim() === '') return null
  if (!highlight) {
    const plain = (t: string) => (t.trim() ? [{ text: t.trim(), changed: false }] : [])
    return { key, label, kind: 'text', same: a.trim() === b.trim(), left: plain(a), right: plain(b) }
  }
  const d = diffWords(a, b)
  return { key, label, kind: 'text', same: d.same, left: d.left, right: d.right }
}

function listRow(key: string, label: string, a: string[], b: string[]): ListRow | null {
  const d = diffList(a, b)
  if (d.left.length === 0 && d.right.length === 0) return null
  return { key, label, kind: 'list', same: d.same, left: d.left, right: d.right }
}

const files = (o: Observation) => [...new Set([...list(o.files_modified), ...list(o.files_read)])]

/**
 * The rows of the side-by-side view: what is different between an older and a newer note, field by field.
 * A field both notes leave empty is left out.
 */
export function buildRows(older: Observation, newer: Observation): FieldRow[] {
  const rows = [
    textRow('title', 'Title', older.title ?? '', newer.title ?? ''),
    textRow('subtitle', 'Summary', older.subtitle ?? '', newer.subtitle ?? ''),
    textRow('narrative', 'Narrative', older.narrative ?? '', newer.narrative ?? ''),
    listRow('facts', 'Key facts', list(older.facts), list(newer.facts)),
    listRow('concepts', 'Concepts', list(older.concepts), list(newer.concepts)),
    listRow('files', 'Files', files(older), files(newer)),
    textRow('type', 'Type', older.type ?? '', newer.type ?? '', false),
    textRow('saved', 'Saved', formatSaved(older.created_at_epoch), formatSaved(newer.created_at_epoch), false)
  ]
  return rows.filter((r): r is FieldRow => r !== null)
}
