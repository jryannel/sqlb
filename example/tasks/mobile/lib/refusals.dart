// What does *not* compile, asserted.
//
// Each case below is a request the server would answer 400 to, written out and
// suppressed with `// ignore:`. The `unnecessary_ignore` lint is on in
// `analysis_options.yaml`, so if the generator ever widened one of these types
// the analyser would report the suppression as unnecessary and the gate would
// fail. The guard fails when it stops guarding — which is the property the
// TypeScript client gets from `@ts-expect-error`, in the language that has no
// such annotation.
//
// Nothing here is meant to be called. It exists to be analysed.
//
// ignore_for_file: unused_element

library;

import 'api/client.gen.dart';

// A column that never declared .Filterable() has no spelling in `where`.
// `tasks.author_id` is a reference the schema chose not to make filterable.
void _unfilterableColumn() {
  // ignore: undefined_named_parameter
  const TaskWhere(authorId: Cond(eq: 'u1'));
}

// Pattern operators need a text column. `comment_count` is an int, so `Cond`
// has no `contains`, and the server would refuse it.
void _patternOnANumber() {
  // ignore: undefined_named_parameter
  const TaskWhere(commentCount: Cond(contains: '3'));
}

// A null test on a column that cannot be null is not a question SQL answers.
// `list_id` is required, so its condition type has no `isNull`.
void _nullTestOnARequiredColumn() {
  // ignore: undefined_named_parameter
  const TaskWhere(listId: Cond(isNull: true));
}

// An enum column compares against its own value set, not against any string.
void _bareStringAgainstAnEnum() {
  // ignore: const_constructor_param_type_mismatch, argument_type_not_assignable
  const TaskWhere(status: Cond(eq: 'todo'));
}

// And not against another column's enum either.
void _theWrongEnum() {
  // ignore: const_constructor_param_type_mismatch, argument_type_not_assignable
  const TaskWhere(status: Cond(eq: TaskPriority.high));
}

// A value type the column does not have.
void _theWrongValueType() {
  // ignore: const_constructor_param_type_mismatch, argument_type_not_assignable
  const TaskWhere(commentCount: Cond(eq: 'three'));
}

// A hidden column has no spelling anywhere: not in the row, not in `select`,
// not as a filter. `users.password_hash` never leaves the process.
void _hiddenColumn(User user) {
  // ignore: undefined_getter
  user.passwordHash;
}

// A column that did not declare .Sortable() is not in the sort vocabulary.
// `tasks.list_id` is filterable and not sortable.
void _unsortableColumn() {
  // ignore: undefined_enum_constant
  TaskListParams(sort: [TaskSort.listId.asc]);
}

// `select` names this resource's columns. Another resource's are a different
// enum, so they do not typecheck.
void _anotherResourcesColumn() {
  // ignore: list_element_type_not_assignable
  const TaskListParams(select: [CommentColumn.body]);
}

// `expand` offers the relations the schema marked expandable, in the direction
// they are served. A task has no `comments` expansion.
void _anExpansionTheResourceDoesNotOffer() {
  // ignore: undefined_enum_constant
  TaskListParams(expand: [TaskExpand.comments]);
}

// The item endpoint declares no `select`: `rest` registers it with
// RejectUnknownQueryParameters, so offering one would generate requests the
// server refuses.
void _selectOnTheItemEndpoint() {
  // ignore: undefined_named_parameter
  const TaskGetParams(select: [TaskColumn.title]);
}

// Read-only columns are the database's or a hook's. `comment_count` is
// maintained by a trigger, so a create body has no property for it.
void _writingAReadOnlyColumn() {
  // ignore: undefined_named_parameter
  const TaskCreate(listId: 'l1', title: 't', description: '', commentCount: 0);
}

// A create body cannot omit a column the database has no default for.
void _omittingARequiredColumn() {
  // ignore: missing_required_argument
  const TaskCreate(listId: 'l1');
}

// An immutable column is settable once, at create. `workspace_id` is
// ReadOnly on every table here, so a patch has no method to write it.
void _patchingAnImmutableColumn() {
  // ignore: undefined_method
  TaskPatch().workspaceId('w2');
}

// Passing null to a column that is not nullable writes a NULL the database
// would reject, so the patch method does not accept one.
void _clearingARequiredColumn() {
  // ignore: argument_type_not_assignable
  TaskPatch().title(null);
}

// A patch writes columns, not relations: the expansion is a response shape.
void _patchingARelation() {
  // ignore: undefined_method
  TaskPatch().list('l2');
}
