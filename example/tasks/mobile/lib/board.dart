/// What the generated client looks like in use.
///
/// Every call here goes through the same URL grammar the Go server parses, and
/// every column, operator and sort term is checked by the analyser. Run
/// `dart analyze` to see that; break one on purpose to see it fail, or read
/// `refusals/refusals.dart` for the ones that are asserted not to compile.
library;

import 'api/client.gen.dart';
import 'http.dart';

/// The board's main query: open, urgent-ish work in one list, newest first.
///
/// [TaskWhere] has one property per filterable column, and the operators each
/// accepts come from its type — `isIn` over the enum's own members, `notNull`
/// only because `assignee_id` is nullable, `contains` only because `title` is
/// text. None of that is expressible in an OpenAPI document, which is the whole
/// argument for generating from the schema rather than from the document.
Future<Page<Task>> openWork(Transport request, String listId, String search) {
  return listTasks(
    request,
    params: TaskListParams(
      where: TaskWhere(
        listId: Cond(eq: listId),
        status: Cond(isIn: [TaskStatus.todo, TaskStatus.inProgress]),
        priority: Cond(isIn: [TaskPriority.high, TaskPriority.urgent]),
        dueAt: NullableCond(lte: DateTime.now(), notNull: true),
        title: search.isEmpty ? null : TextCond(contains: search),
      ),
      sort: [TaskSort.priority.desc, TaskSort.position.asc],
      perPage: 50,
    ),
  );
}

/// A projection sends less over a mobile connection, and the row says which
/// columns it has.
///
/// Dart cannot narrow a type by a runtime `select`, so a column that was not
/// asked for throws [MissingColumn] when it is read, naming the column and the
/// fix. The primary key comes back whether or not it was named — the server
/// adds it, and the pager needs it.
Future<List<String>> taskTitles(Transport request, String listId) async {
  final page = await listTasks(
    request,
    params: TaskListParams(
      where: TaskWhere(listId: Cond(eq: listId)),
      select: [TaskColumn.title, TaskColumn.status],
      sort: [TaskSort.position.asc],
    ),
  );
  return page.items
      .map((task) => '${task.title} (${task.status.wire})')
      .toList();
}

/// An expansion goes the other way: `expand` fills in the relation.
Future<String> taskWithList(Transport request, String id) async {
  final task = await getTask(
    request,
    id,
    params: const TaskGetParams(expand: [TaskExpand.list]),
  );
  return '${task.title} — ${task.list!.name}';
}

/// The reverse direction of an expansion, and the reason it is not a bare list.
///
/// A list's tasks are capped at twenty by the schema, so the envelope carries
/// [Collection.hasMore] and the screen can say "and 43 more" instead of quietly
/// showing a fifth of the work.
Future<String> listOverview(Transport request, String id) async {
  final list = await getList(
    request,
    id,
    params: const ListGetParams(expand: [ListExpand.tasks]),
  );
  final tasks = list.tasks!;
  final suffix = tasks.hasMore ? ' and more' : '';
  return '${list.name}: ${tasks.items.length} tasks$suffix';
}

/// A list that loads as it is scrolled.
///
/// The cursor is the pager's, not the screen's. `next_cursor` names the
/// position of the last row rather than counting to it, so the hundredth page
/// costs what the first costs and a task created mid-scroll cannot make the
/// list show a row twice.
CursorPager<Task> overdueFeed(Transport request) {
  return taskPager(
    request,
    params: TaskListParams(
      where: TaskWhere(
        dueAt: NullableCond(lt: DateTime.now()),
        status: Cond(ne: TaskStatus.done),
      ),
      sort: [TaskSort.dueAt.asc],
      perPage: 100,
    ),
  );
}

/// A write. The patch carries the columns it named and no others, which is what
/// lets a screen save one field without sending back a row it may have read
/// minutes ago.
///
/// There is no `completedAt` method to call beside it: the column is read-only,
/// a BeforeUpdate hook sets it, and the pair is kept consistent by a database
/// constraint. A client that could set the two independently is a client that
/// can violate it.
Future<Task> completeTask(Transport request, String id) {
  final patch = TaskPatch()..status(TaskStatus.done);
  return updateTask(request, id, patch);
}

/// Clearing a nullable column. Passing null writes NULL; not calling the method
/// at all leaves the column alone, and those are different requests.
Future<Task> unassign(Transport request, String id) {
  final patch = TaskPatch()..assigneeId(null);
  return updateTask(request, id, patch);
}

/// `workspace_id` and `author_id` are absent from the create body: the hooks
/// own them, so there is no property to send and nothing for the server to
/// ignore.
Future<Task> addTask(Transport request, String listId, String title) {
  return createTask(
    request,
    TaskCreate(listId: listId, title: title, description: ''),
  );
}

/// A change-feed event carries a table name and a row key. [TableName.byWire]
/// turns the first into something a switch can be exhaustive over, so a table
/// added to the schema shows up as a missing case rather than as a string
/// nothing matches.
String? describeChange(String table, String id) {
  return switch (TableName.byWire(table)) {
    TableName.tasks => 'task $id changed',
    TableName.lists => 'list $id changed',
    null => null, // a table this client does not read
    _ => 'row $id of $table changed',
  };
}

/// A rejection says what would have been accepted, and that survives to here.
///
/// `?sort=body` comes back as a 400 whose problem document carries the sortable
/// columns, so a filter UI can offer the alternatives instead of a dead end.
List<String> sortFallback(Object error) {
  if (error is ApiException && error.problem != null) {
    return error.problem!.allowedFor('query.sort');
  }
  return const [];
}
