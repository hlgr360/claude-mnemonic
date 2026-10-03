// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  DECISIONS, confidenceRank, conflictTitle, decisionForKey, decisionLabel, hiddenSide, moveSelection, nextSelection,
  relationLabel, relationShort, resolutionSummary, restorableText
} from '../src/utils/conflictText.ts'

test('the three decisions have the wording and keys of the panel', () => {
  assert.deepEqual(DECISIONS.map(d => [d.decision, d.label, d.key]), [
    ['supersede_older', 'Newer replaces older', '1'],
    ['supersede_newer', 'Older replaces newer', '2'],
    ['keep_both', 'Keep both', '3']
  ])
  assert.equal(decisionForKey('2').decision, 'supersede_newer')
  assert.equal(decisionForKey('x'), undefined)
  assert.equal(decisionLabel('keep_both'), 'Keep both')
  assert.equal(decisionLabel(undefined), '')
})

test('relations are described and shortened, unknown ones too', () => {
  for (const r of ['supersedes', 'contradicts', 'duplicate', 'manual', undefined, 'other']) {
    assert.ok(relationLabel(r).length > 0)
    assert.ok(relationShort(r).length > 0)
  }
  assert.equal(relationShort('supersedes'), 'Outdated')
  assert.equal(relationShort('other'), 'Conflict')
})

test('confidence ranks high above medium above low above unknown', () => {
  assert.ok(confidenceRank('high') > confidenceRank('medium'))
  assert.ok(confidenceRank('medium') > confidenceRank('low'))
  assert.ok(confidenceRank('low') > confidenceRank(undefined))
})

const decided = (decision, hidden) => ({
  id: 1, older_obs_id: 10, newer_obs_id: 20, resolved: true, decision, superseded_obs_id: hidden,
  older: { id: 10 }, newer: { id: 20, title: ' A title ' }
})

test('the hidden side and the summary follow the decision', () => {
  assert.equal(hiddenSide(decided('supersede_older', 10)), 'older')
  assert.equal(hiddenSide(decided('supersede_newer', 20)), 'newer')
  assert.equal(hiddenSide(decided('keep_both', 0)), null)
  assert.equal(hiddenSide({ ...decided('supersede_older', 10), resolved: false }), null)
  assert.equal(resolutionSummary(decided('supersede_older', 10)), 'The older note is hidden.')
  assert.equal(resolutionSummary(decided('supersede_newer', 20)), 'The newer note is hidden.')
  assert.equal(resolutionSummary(decided('keep_both', 0)), 'Both notes are kept.')
})

test('the time left to restore is described in days', () => {
  const now = Date.UTC(2026, 9, 3)
  const day = 24 * 60 * 60 * 1000
  assert.equal(restorableText(undefined, now), '')
  assert.equal(restorableText(0, now), '')
  assert.equal(restorableText(now - 1, now), 'Will be deleted at the next cleanup.')
  assert.equal(restorableText(now + 3 * 60 * 60 * 1000, now), 'Will be deleted within a day.')
  assert.equal(restorableText(now + day, now), 'Will be deleted within a day.')
  assert.equal(restorableText(now + 2 * day + 1, now), 'Will be deleted in 3 days.')
})

test('after a decision the next proposal moves into its place', () => {
  assert.equal(nextSelection([1, 2, 3], 2), 3)
  assert.equal(nextSelection([1, 2, 3], 3), 2, 'the last one falls back to the one before')
  assert.equal(nextSelection([1], 1), null)
  assert.equal(nextSelection([1, 2], 9), 1, 'an unknown id selects the first')
  assert.equal(nextSelection([], 9), null)
})

test('moving the selection stays inside the list', () => {
  assert.equal(moveSelection([1, 2, 3], 1, 1), 2)
  assert.equal(moveSelection([1, 2, 3], 3, 1), 3)
  assert.equal(moveSelection([1, 2, 3], 1, -1), 1)
  assert.equal(moveSelection([1, 2, 3], null, 1), 1)
  assert.equal(moveSelection([1, 2, 3], null, -1), 3)
  assert.equal(moveSelection([1, 2, 3], 99, 1), 1)
  assert.equal(moveSelection([], 1, 1), null)
})

test('the title falls back to the id', () => {
  assert.equal(conflictTitle(decided('keep_both', 0)), 'A title')
  assert.equal(conflictTitle({ newer: { id: 7, title: '  ' } }), 'Note #7')
})
