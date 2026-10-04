// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { autoMergedMessage, mergeSentence, notesText, projectLine, strengthHint, strengthLabel, swapped } from '../src/utils/duplicates.ts'
import { dismissDuplicate, listDuplicates, restoreDuplicate } from '../src/utils/projectAdmin.ts'

const sg = {
  survivor: { project: 'shop_aaaaaa', label: 'shop', display_name: 'shop', observations: 40, last_active_epoch: 1 },
  other: { project: 'webshop_bbbbbb', label: '', display_name: 'webshop', observations: 1, last_active_epoch: 1 },
  strength: 'strong',
  reasons: [{ code: 'same_remote', text: 'both were cloned from git.example.org/team/shop' }],
  auto_mergeable: false
}

function fakeFetch(...answers) {
  const calls = []
  const fn = async (url, init = {}) => {
    calls.push({ url, method: init.method ?? 'GET', body: init.body, cache: init.cache })
    const a = answers.shift() ?? { status: 200, body: {} }
    return { ok: a.status >= 200 && a.status < 300, status: a.status, statusText: '', json: async () => a.body, text: async () => JSON.stringify(a.body) }
  }
  fn.calls = calls
  return fn
}

test('strengths have a label and a hint, unknown ones a neutral label', () => {
  assert.equal(strengthLabel('strong'), 'Same repository')
  assert.equal(strengthLabel('medium'), 'Looks the same')
  assert.equal(strengthLabel('weak'), 'Only a hint')
  assert.equal(strengthLabel('other'), 'Possible')
  assert.match(strengthHint('strong'), /git remote/)
  assert.equal(strengthHint('other'), '')
})

test('counts and lines read naturally', () => {
  assert.equal(notesText(1), '1 note')
  assert.equal(notesText(0), '0 notes')
  assert.equal(notesText(40), '40 notes')
  assert.equal(projectLine(sg.survivor), 'shop (40 notes)')
  assert.equal(projectLine(sg.other), 'webshop_bbbbbb (1 note)', 'the id stands in for a missing label')
})

test('the merge sentence moves the smaller into the bigger, and swapping reverses it', () => {
  assert.equal(mergeSentence(sg), 'Move webshop_bbbbbb into shop_aaaaaa')
  const s = swapped(sg)
  assert.equal(s.survivor.project, 'webshop_bbbbbb')
  assert.equal(mergeSentence(s), 'Move shop_aaaaaa into webshop_bbbbbb')
  assert.equal(sg.survivor.project, 'shop_aaaaaa', 'the original is untouched')
})

test('the automatic merge notice names both projects and the backup', () => {
  const m = autoMergedMessage({ project: 'webshop_bbbbbb', into: 'shop_aaaaaa', backup: '/tmp/backup.db' })
  assert.match(m, /webshop_bbbbbb was merged into shop_aaaaaa automatically/)
  assert.match(m, /same git remote/)
  assert.match(m, /backup of the database before the merge is at \/tmp\/backup\.db/)
  assert.doesNotMatch(autoMergedMessage({ project: 'a', into: 'b' }), /backup/)
})

test('listDuplicates reads the suggestions uncached', async () => {
  const f = fakeFetch({ status: 200, body: { suggestions: [sg], dismissed: [], auto_merge: false } })
  const r = await listDuplicates(f)
  assert.equal(r.suggestions.length, 1)
  assert.equal(f.calls[0].url, '/api/projects/duplicates')
  assert.equal(f.calls[0].cache, 'no-store')
})

test('dismiss and restore post the pair', async () => {
  const f = fakeFetch({ status: 200, body: {} }, { status: 200, body: {} })
  await dismissDuplicate('a_111111', 'b_222222', f)
  await restoreDuplicate('a_111111', 'b_222222', f)
  assert.equal(f.calls[0].url, '/api/projects/duplicates/dismiss')
  assert.equal(f.calls[0].method, 'POST')
  assert.deepEqual(JSON.parse(f.calls[0].body), { a: 'a_111111', b: 'b_222222' })
  assert.equal(f.calls[1].url, '/api/projects/duplicates/restore')
})
