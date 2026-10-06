// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  FoldApiError, applyConsolidation, archivedReasonText, consolidationAdds, consolidationSummary, describeFoldError, findDuplicateGroups,
  foldHeadline, foldKindLabel, foldsQuery, listArchived, listFolds, notesText, previewConsolidation, previewRollup, restoreFold,
  restoreSentence, rollupGroupLine, rollupOutcome, runRollup, similarityPercent, unarchiveNote
} from '../src/utils/folds.ts'

// A fetch that records the request and answers with the given body or status.
function fakeFetch(status = 200, body = {}) {
  const calls = []
  const fn = async (url, init) => {
    calls.push({ url, init })
    return { ok: status < 400, status, statusText: 'x', json: async () => body, text: async () => (typeof body === 'string' ? body : JSON.stringify(body)) }
  }
  fn.calls = calls
  return fn
}

test('foldsQuery leaves empty values out', () => {
  assert.equal(foldsQuery(), '')
  assert.equal(foldsQuery({ project: null, kind: null }), '')
  assert.equal(foldsQuery({ project: 'shop_ab12cd', kind: 'rollup', includeUndone: true, limit: 20 }), '?project=shop_ab12cd&kind=rollup&include_undone=true&limit=20')
  assert.equal(foldsQuery({ includeUndone: false }), '')
})

test('the calls go to the worker endpoints with the right method and body', async () => {
  let f = fakeFetch(200, { folds: [] })
  await listFolds({ project: 'a_b' }, f)
  assert.equal(f.calls[0].url, '/api/folds?project=a_b')
  assert.equal(f.calls[0].init.cache, 'no-store')

  f = fakeFetch(200, {})
  await restoreFold(7, f)
  assert.equal(f.calls[0].url, '/api/folds/7/restore')
  assert.equal(f.calls[0].init.method, 'POST')

  f = fakeFetch(200, {})
  await previewRollup('shop/x_ab12cd', f)
  assert.equal(f.calls[0].url, '/api/projects/shop%2Fx_ab12cd/rollup', 'the project is escaped')
  assert.deepEqual(JSON.parse(f.calls[0].init.body), { dry_run: true }, 'a preview is a dry run')

  f = fakeFetch(200, {})
  await runRollup('shop_ab12cd', f)
  assert.deepEqual(JSON.parse(f.calls[0].init.body), { dry_run: false })

  f = fakeFetch(200, {})
  await findDuplicateGroups('shop_ab12cd', 0.9, 120, f)
  assert.equal(f.calls[0].url, '/api/observations/duplicates?project=shop_ab12cd&threshold=0.9&limit=120')

  f = fakeFetch(200, {})
  await previewConsolidation([4, 9], f)
  assert.deepEqual(JSON.parse(f.calls[0].init.body), { ids: [4, 9] }, 'a preview has no token')

  f = fakeFetch(200, {})
  await applyConsolidation([4, 9], 'tok', f)
  assert.deepEqual(JSON.parse(f.calls[0].init.body), { ids: [4, 9], confirm: 'tok' })

  f = fakeFetch(200, {})
  await listArchived({ project: 'a_b', limit: 20, offset: 40 }, f)
  assert.equal(f.calls[0].url, '/api/observations?archived_only=true&limit=20&offset=40&project=a_b')

  f = fakeFetch(200, {})
  await unarchiveNote(12, f)
  assert.equal(f.calls[0].url, '/api/observations/12/unarchive')
  assert.equal(f.calls[0].init.method, 'POST')
})

test('an error answer becomes a FoldApiError with its status and text', async () => {
  const f = fakeFetch(409, 'the notes changed since the preview: preview again')
  await assert.rejects(() => applyConsolidation([1, 2], 'old', f), (err) => err instanceof FoldApiError && err.status === 409 && err.message.includes('preview again'))
})

test('wording of folds', () => {
  assert.equal(notesText(1), '1 note')
  assert.equal(notesText(0), '0 notes')
  assert.equal(foldKindLabel('rollup'), 'Roll-up')
  assert.equal(foldKindLabel('consolidation'), 'Consolidation')
  assert.equal(foldHeadline({ kind: 'rollup', label: '2026-03', sources: [1, 2, 3] }), '3 notes rolled up (2026-03)')
  assert.equal(foldHeadline({ kind: 'rollup', label: '', sources: [1] }), '1 note rolled up')
  assert.equal(foldHeadline({ kind: 'consolidation', survivor: 12, sources: [1, 2] }), '2 notes folded into #12')
  assert.equal(similarityPercent(0.876), '88%')
})

test('the sentence under a restore says what came back and what stayed', () => {
  assert.equal(restoreSentence({ kind: 'rollup', restored: [1, 2], kept: [], survivor_archived: true }), '2 notes live again. The roll-up was archived.')
  assert.equal(restoreSentence({ kind: 'consolidation', restored: [1], kept: [], survivor_archived: false }), '1 note live again.')
  assert.match(restoreSentence({ kind: 'rollup', restored: [1], kept: [2], survivor_archived: true }), /1 note stay archived: it was archived for another reason/)
  assert.match(restoreSentence({ kind: 'rollup', restored: [1], kept: [2, 3], survivor_archived: true }), /2 notes stay archived: they were archived for another reason/)
  assert.match(restoreSentence({ kind: 'rollup', restored: [1], kept: [], survivor_archived: false }), /could not be archived and is still live/)
})

test('roll-up groups and outcomes read as sentences', () => {
  assert.equal(rollupGroupLine({ label: '2026-03', ids: [1, 2, 3], from: '2026-03-02', to: '2026-03-28' }), '2026-03: 3 notes, 2026-03-02 to 2026-03-28')
  assert.equal(rollupGroupLine({ label: 'x', ids: [1], from: '2026-03-02', to: '2026-03-02' }), 'x: 1 note, 2026-03-02')
  assert.equal(rollupOutcome({ groups: [], remaining: 0 }), 'Nothing to roll up.')
  assert.equal(rollupOutcome({ groups: [{ archived: 10 }, { archived: 8 }], remaining: 0 }), '2 roll-ups written, 18 notes archived.')
  assert.equal(rollupOutcome({ groups: [{ archived: 10 }, { archived: 0, error: 'down' }], remaining: 2 }),
    '1 roll-up written, 10 notes archived. 1 group failed; their notes are still live. 2 more groups are left for a later run.')
  assert.equal(rollupOutcome({ groups: [{ archived: 0, error: 'down' }], remaining: 0 }), 'No roll-up was written. 1 group failed; their notes are still live.')
})

test('a consolidation plan is summarised, and what the survivor takes over is counted without the note of the merge', () => {
  const plan = {
    survivor: { id: 12, title: 'Crane board' }, duplicates: [{ id: 3 }, { id: 4 }],
    added_facts: ['a', 'b', 'Consolidated with #3, #4 (near-duplicate notes)'], added_concepts: ['x'], added_files: [], relations_to_copy: 2
  }
  assert.equal(consolidationSummary(plan), 'Keep #12 “Crane board” and archive 2 duplicates.')
  assert.equal(consolidationSummary({ survivor: { id: 1, title: '' }, duplicates: [{ id: 2 }] }), 'Keep #1 “untitled” and archive 1 duplicate.')
  assert.deepEqual(consolidationAdds(plan), ['2 facts', '1 concept', '2 relations'])
  assert.deepEqual(consolidationAdds({ added_facts: ['Consolidated with #3 (near-duplicate notes)'], added_concepts: [], added_files: [], relations_to_copy: 0 }), [])
})

test('archive reasons are short phrases', () => {
  assert.equal(archivedReasonText('rolled-up into #12'), 'Rolled up into #12')
  assert.equal(archivedReasonText('consolidated into #7'), 'Consolidated into #7')
  assert.equal(archivedReasonText('cap: only the newest notes of a project stay live'), 'Archived by the cap on notes per project')
  assert.equal(archivedReasonText('roll-up restored: its notes are live again'), 'A roll-up that was restored')
  assert.equal(archivedReasonText('my own reason'), 'my own reason')
  assert.equal(archivedReasonText(''), 'Archived')
  assert.equal(archivedReasonText(undefined), 'Archived')
})

test('errors become sentences for the person', () => {
  assert.match(describeFoldError(new FoldApiError(404, 'x')), /no longer exists/)
  assert.match(describeFoldError(new FoldApiError(409, 'the notes changed since the preview: preview again')), /Review them again/)
  assert.match(describeFoldError(new FoldApiError(409, 'already restored')), /already done/)
  assert.equal(describeFoldError(new FoldApiError(422, 'at least two different notes are needed')), 'at least two different notes are needed')
  assert.match(describeFoldError(new FoldApiError(503, 'x')), /No model is available.*Nothing was changed/)
  assert.match(describeFoldError(new FoldApiError(502, 'boom')), /could not write the roll-up: boom/)
  assert.equal(describeFoldError(new Error('network down')), 'network down')
})
