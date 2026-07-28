// The encoder, checked against the grammar the server parses.
//
// The types next door are checked by tsc; this is the other half — the strings
// that actually go over the wire. Every expectation below is a query the Go
// parser in filter/filter.go accepts, and `../../../app/client_test.go` sends
// the same ones at a running server, so a change here that tsc cannot see is
// still caught somewhere.
//
//   node --test --experimental-strip-types src/api/encode.test.ts

import assert from 'node:assert/strict';
import test from 'node:test';

import { encodeItemQuery, encodeListQuery } from './client.gen.ts';

test('a bare value is equality', () => {
  assert.equal(encodeListQuery({ where: { status: 'todo' } }), 'status=eq.todo');
});

test('two operators on one column conjoin as repeated parameters', () => {
  assert.equal(
    encodeListQuery({ where: { position: { gte: 1, lt: 10 } } }),
    'position=gte.1&position=lt.10',
  );
});

test('a value list is comma-separated after the operator', () => {
  assert.equal(
    encodeListQuery({ where: { priority: { in: ['high', 'urgent'] } } }),
    'priority=in.high%2Curgent',
  );
});

test('a list member carrying a comma is quoted, so the parser reads it whole', () => {
  assert.equal(
    encodeListQuery({ where: { title: { in: ['a,b', 'c'] } } }),
    'title=in.%22a%2Cb%22%2Cc',
  );
});

test('a null test is the bare operator, with no value', () => {
  assert.equal(
    encodeListQuery({ where: { assignee_id: { isnull: true } } }),
    'assignee_id=isnull',
  );
});

test('a false null test is absent rather than sent as "false"', () => {
  assert.equal(encodeListQuery({ where: { assignee_id: { isnull: false } } }), '');
});

test('between takes two values', () => {
  assert.equal(
    encodeListQuery({ where: { position: { between: [1, 5] } } }),
    'position=between.1%2C5',
  );
});

test('a Date is sent as RFC 3339, which is what the column parses', () => {
  assert.equal(
    encodeListQuery({ where: { due_at: { lt: new Date('2026-07-28T00:00:00Z') } } }),
    'due_at=lt.2026-07-28T00%3A00%3A00.000Z',
  );
});

test('sort takes an array and joins it, descending terms keep their dash', () => {
  assert.equal(encodeListQuery({ sort: ['-priority', 'position'] }), 'sort=-priority%2Cposition');
});

test('select and expand are comma-separated single parameters', () => {
  assert.equal(
    encodeListQuery({ select: ['title', 'status'], expand: ['list'] }),
    'expand=list&select=title%2Cstatus',
  );
});

test('paging parameters pass through', () => {
  assert.equal(encodeListQuery({ per_page: 50, cursor: 'abc' }), 'cursor=abc&per_page=50');
});

test('an empty query is an empty string, not a stray question mark', () => {
  assert.equal(encodeListQuery(), '');
  assert.equal(encodeListQuery({ where: {} }), '');
});

test('undefined conditions are dropped, so a conditional filter needs no branch', () => {
  assert.equal(
    encodeListQuery({ search: undefined, where: { status: undefined, list_id: 'x' } }),
    'list_id=eq.x',
  );
});

test('the same parameters always produce the same string', () => {
  const a = encodeListQuery({ sort: 'position', where: { status: 'todo', list_id: 'x' } });
  const b = encodeListQuery({ where: { list_id: 'x', status: 'todo' }, sort: 'position' });
  assert.equal(a, b);
});

test('the escape hatch appends parameters verbatim', () => {
  assert.equal(encodeListQuery({ params: { or: '(status.eq.todo,position.lt.3)' } }),
    'or=%28status.eq.todo%2Cposition.lt.3%29');
});

test('the item endpoint encodes expand and nothing else', () => {
  assert.equal(encodeItemQuery({ expand: ['list'] }), 'expand=list');
  assert.equal(encodeItemQuery(), '');
});
