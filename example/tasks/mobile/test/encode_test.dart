// The encoder, checked against the grammar the server parses.
//
// The types next door are checked by the analyser; this is the other half —
// the strings that actually go over the wire. Every expectation below is a
// query the Go parser in filter/filter.go accepts, and the TypeScript client's
// `web/src/api/encode.test.ts` asserts the same strings, so the two clients
// cannot drift into disagreeing about the grammar.
//
//   dart test

import 'package:tasks_mobile/api/client.gen.dart';
import 'package:test/test.dart';

String listQuery(TaskWhere where) => TaskListParams(where: where).toQuery();

void main() {
  test('a single operator is the operator prefix and the value', () {
    expect(
      listQuery(const TaskWhere(status: Cond(eq: TaskStatus.todo))),
      'status=eq.todo',
    );
  });

  test('two operators on one column conjoin as repeated parameters', () {
    expect(
      listQuery(const TaskWhere(commentCount: Cond(gte: 1, lt: 10))),
      'comment_count=gte.1&comment_count=lt.10',
    );
  });

  test('a value list is comma-separated after the operator', () {
    expect(
      listQuery(
        const TaskWhere(
          priority: Cond(isIn: [TaskPriority.high, TaskPriority.urgent]),
        ),
      ),
      'priority=in.high%2Curgent',
    );
  });

  test('a member carrying a comma is quoted, so the parser reads it whole', () {
    expect(
      listQuery(const TaskWhere(title: TextCond(isIn: ['a,b', 'c']))),
      'title=in.%22a%2Cb%22%2Cc',
    );
  });

  test('a null test is the bare operator, with no value', () {
    expect(
      listQuery(const TaskWhere(assigneeId: NullableCond(isNull: true))),
      'assignee_id=isnull',
    );
  });

  test('a false null test is absent rather than sent as "false"', () {
    expect(
      listQuery(const TaskWhere(assigneeId: NullableCond(isNull: false))),
      '',
    );
  });

  test('between takes two values', () {
    expect(
      listQuery(const TaskWhere(commentCount: Cond(between: (1, 5)))),
      'comment_count=between.1%2C5',
    );
  });

  test(
    'a DateTime is sent as RFC 3339 UTC, which is what the column parses',
    () {
      expect(
        listQuery(
          TaskWhere(dueAt: NullableCond(lt: DateTime.utc(2026, 7, 28))),
        ),
        'due_at=lt.2026-07-28T00%3A00%3A00.000Z',
      );
    },
  );

  test('a local DateTime is converted rather than sent with an offset', () {
    final local = DateTime.utc(2026, 7, 28, 12).toLocal();
    expect(
      listQuery(TaskWhere(dueAt: NullableCond(lt: local))),
      'due_at=lt.2026-07-28T12%3A00%3A00.000Z',
    );
  });

  test('sort joins its terms, and a descending one keeps its dash', () {
    expect(
      TaskListParams(
        sort: [TaskSort.priority.desc, TaskSort.position.asc],
      ).toQuery(),
      'sort=-priority%2Cposition',
    );
  });

  test('select and expand are comma-separated single parameters', () {
    expect(
      const TaskListParams(
        select: [TaskColumn.title, TaskColumn.status],
        expand: [TaskExpand.list],
      ).toQuery(),
      'expand=list&select=title%2Cstatus',
    );
  });

  test('paging parameters pass through', () {
    expect(
      const TaskListParams(perPage: 50, cursor: 'abc').toQuery(),
      'cursor=abc&per_page=50',
    );
  });

  test('an empty request is an empty string, not a stray question mark', () {
    expect(const TaskListParams().toQuery(), '');
    expect(const TaskListParams(where: TaskWhere()).toQuery(), '');
  });

  test(
    'null conditions are dropped, so a conditional filter needs no branch',
    () {
      expect(
        const TaskListParams(
          search: null,
          where: TaskWhere(status: null, listId: Cond(eq: 'x')),
        ).toQuery(),
        'list_id=eq.x',
      );
    },
  );

  test('the same parameters always produce the same string', () {
    final a = TaskListParams(
      sort: [TaskSort.position.asc],
      where: const TaskWhere(
        status: Cond(eq: TaskStatus.todo),
        listId: Cond(eq: 'x'),
      ),
    ).toQuery();
    final b = TaskListParams(
      where: const TaskWhere(
        listId: Cond(eq: 'x'),
        status: Cond(eq: TaskStatus.todo),
      ),
      sort: [TaskSort.position.asc],
    ).toQuery();
    expect(a, b);
  });

  test('countExact is the only spelling of count the server accepts', () {
    expect(const TaskListParams(countExact: true).toQuery(), 'count=exact');
    expect(const TaskListParams().toQuery(), '');
  });

  test('the escape hatch appends parameters verbatim', () {
    expect(
      const TaskListParams(
        params: {
          'or': ['(status.eq.todo,position.lt.3)'],
        },
      ).toQuery(),
      'or=%28status.eq.todo%2Cposition.lt.3%29',
    );
  });

  test('withCursor keeps the filter and drops the page number', () {
    final params = TaskListParams(
      where: const TaskWhere(listId: Cond(eq: 'x')),
      page: 4,
      perPage: 20,
    );
    expect(params.toQuery(), 'list_id=eq.x&page=4&per_page=20');
    expect(
      params.withCursor('abc').toQuery(),
      'cursor=abc&list_id=eq.x&per_page=20',
    );
  });

  test('the item endpoint encodes expand and nothing else', () {
    expect(
      const TaskGetParams(expand: [TaskExpand.list]).toQuery(),
      'expand=list',
    );
    expect(const TaskGetParams().toQuery(), '');
  });
}
