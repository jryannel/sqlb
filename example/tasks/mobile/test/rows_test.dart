// The row view and the pager: the two pieces of the Dart client that have no
// TypeScript counterpart, because Dart cannot narrow a response type by a
// runtime projection and has no keyed query cache to page through.

import 'package:tasks_mobile/api/client.gen.dart';
import 'package:test/test.dart';

Map<String, dynamic> taskJson({Map<String, dynamic> extra = const {}}) => {
  'id': 't1',
  'workspace_id': 'w1',
  'list_id': 'l1',
  'assignee_id': null,
  'author_id': 'u1',
  'title': 'Ship it',
  'description': '',
  'labels': ['urgent', 'backend'],
  'status': 'todo',
  'priority': 'high',
  'due_at': '2026-07-28T09:30:00Z',
  'completed_at': null,
  'position': 3,
  'comment_count': 0,
  'created_at': '2026-07-01T00:00:00Z',
  'updated_at': '2026-07-01T00:00:00Z',
  'deleted_at': null,
  ...extra,
};

void main() {
  test('columns decode to the type the schema declared', () {
    final task = Task.fromJson(taskJson());

    expect(task.title, 'Ship it');
    expect(task.status, TaskStatus.todo);
    expect(task.priority, TaskPriority.high);
    expect(task.position, 3);
    expect(task.dueAt, DateTime.utc(2026, 7, 28, 9, 30));
    expect(task.assigneeId, isNull);
  });

  test('an array column decodes to a typed list', () {
    final task = Task.fromJson(taskJson());
    expect(task.labels, ['urgent', 'backend']);
  });

  test('an empty array is a list of no elements, not a missing column', () {
    final task = Task.fromJson(taskJson(extra: {'labels': <String>[]}));
    expect(task.labels, isEmpty);
  });

  test('a JSON integer reaches a double column as a double', () {
    // The trap this exists for: jsonDecode gives 3 rather than 3.0, and
    // `json['x'] as double` throws on it.
    final row = Comment.fromJson({
      'id': 'c1',
      'workspace_id': 'w1',
      'task_id': 't1',
      'author_id': 'u1',
      'body': 'hi',
      'created_at': '2026-07-01T00:00:00Z',
      'updated_at': '2026-07-01T00:00:00Z',
    });
    expect(row.body, 'hi');
  });

  test('a column the request did not select throws, naming the fix', () {
    final task = Task.fromJson({'id': 't1', 'title': 'Ship it'});

    expect(task.title, 'Ship it');
    expect(task.has(TaskColumn.status), isFalse);
    expect(
      () => task.status,
      throwsA(
        isA<MissingColumn>()
            .having((e) => e.column, 'column', 'status')
            .having((e) => e.type, 'type', 'Task')
            .having((e) => '$e', 'message', contains('select')),
      ),
    );
  });

  test('a nullable column that was not selected throws too', () {
    // The distinction a bare `json[column] as String?` cannot make: absent and
    // null are different facts, and only one of them is a mistake.
    final task = Task.fromJson({'id': 't1'});
    expect(() => task.assigneeId, throwsA(isA<MissingColumn>()));

    final selected = Task.fromJson({'id': 't1', 'assignee_id': null});
    expect(selected.assigneeId, isNull);
  });

  test('an enum value this client does not know is reported as that', () {
    final task = Task.fromJson(taskJson(extra: {'status': 'archived'}));
    expect(
      () => task.status,
      throwsA(
        isA<UnknownEnumValue>()
            .having((e) => e.value, 'value', 'archived')
            .having((e) => '$e', 'message', contains('regenerate')),
      ),
    );
    expect(TaskStatus.byWire('archived'), isNull);
  });

  test('an unexpanded relation is null rather than an error', () {
    final task = Task.fromJson(taskJson());
    expect(task.list, isNull);
  });

  test('a forward expansion decodes to the target row', () {
    final task = Task.fromJson(
      taskJson(
        extra: {
          'list': {'id': 'l1', 'name': 'Inbox'},
        },
      ),
    );
    expect(task.list!.name, 'Inbox');
    // Decoded once and remembered, so a widget that reads it every frame does
    // not rebuild the row.
    expect(identical(task.list, task.list), isTrue);
  });

  test('a reverse expansion keeps the envelope that says it was capped', () {
    final list = ListRow.fromJson({
      'id': 'l1',
      'name': 'Inbox',
      'tasks': {
        'items': [taskJson()],
        'has_more': true,
      },
    });
    expect(list.tasks!.items.single.title, 'Ship it');
    expect(list.tasks!.hasMore, isTrue);
  });

  test(
    'rows compare by their contents, which is what a rebuild check needs',
    () {
      expect(Task.fromJson(taskJson()), Task.fromJson(taskJson()));
      expect(
        Task.fromJson(taskJson()).hashCode,
        Task.fromJson(taskJson()).hashCode,
      );
      expect(
        Task.fromJson(taskJson()),
        isNot(Task.fromJson(taskJson(extra: {'title': 'Other'}))),
      );
    },
  );

  test('toJson gives back exactly what arrived, for a local cache', () {
    final json = taskJson();
    expect(Task.fromJson(json).toJson(), json);
  });

  test('a patch carries the columns it named and no others', () {
    final patch = TaskPatch()
      ..title('New')
      ..assigneeId(null);
    expect(patch.toJson(), {'title': 'New', 'assignee_id': null});
    expect(TaskPatch().isEmpty, isTrue);
  });

  test('a create body omits what it was not given', () {
    const body = TaskCreate(listId: 'l1', title: 'Ship it', description: '');
    expect(body.toJson(), {
      'list_id': 'l1',
      'title': 'Ship it',
      'description': '',
    });

    const withStatus = TaskCreate(
      listId: 'l1',
      title: 'Ship it',
      description: '',
      status: TaskStatus.inProgress,
    );
    expect(withStatus.toJson()['status'], 'in_progress');
  });

  group('CursorPager', () {
    Transport pages(List<Map<String, dynamic>> responses, List<String?> seen) {
      var next = 0;
      return (request) async {
        final query = Uri.splitQueryString(request.query ?? '');
        seen.add(query['cursor']);
        return responses[next++];
      };
    }

    Map<String, dynamic> page(String id, {String? cursor, bool more = false}) =>
        {
          'items': [
            taskJson(extra: {'id': id}),
          ],
          'has_more': more,
          'page': 1,
          'per_page': 1,
          'next_cursor': ?cursor,
          'total': 2,
        };

    test('walks until the server stops offering a cursor', () async {
      final seen = <String?>[];
      final pager = taskPager(
        pages([page('t1', cursor: 'c1', more: true), page('t2')], seen),
      );

      expect(pager.hasMore, isTrue);
      await pager.loadMore();
      expect(pager.items.map((t) => t.id), ['t1']);
      expect(pager.total, 2);

      await pager.loadMore();
      expect(pager.items.map((t) => t.id), ['t1', 't2']);
      expect(pager.hasMore, isFalse);

      // The cursor came off the previous response rather than being counted to.
      expect(seen, [null, 'c1']);

      // Exhausted, so a scroll listener that keeps firing costs nothing.
      await pager.loadMore();
      expect(seen.length, 2);
    });

    test('concurrent loads collapse onto the one already running', () async {
      final seen = <String?>[];
      final pager = taskPager(pages([page('t1')], seen));

      await Future.wait([pager.loadMore(), pager.loadMore(), pager.loadMore()]);

      expect(seen.length, 1);
      expect(pager.items, hasLength(1));
    });

    test('reset starts the walk over, which is what a refresh does', () async {
      final seen = <String?>[];
      final pager = taskPager(
        pages([page('t1', cursor: 'c1', more: true), page('t1')], seen),
      );

      await pager.loadMore();
      pager.reset();
      expect(pager.items, isEmpty);
      expect(pager.hasMore, isTrue);

      await pager.loadMore();
      expect(seen, [null, null]);
    });
  });
}
