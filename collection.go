package sqlb

import (
	"reflect"
	"strings"
)

// Collection is what an expanded reverse relation arrives as: the children that
// fit under the relation's cap, and whether there were more.
//
// A bare slice would be the obvious choice and it is deliberately not used. A
// reverse expansion is capped — an uncapped one makes a single response's size
// a function of data nobody bounded — and a slice cannot say it was truncated,
// so a caller reading fifty of an author's two hundred posts would have no way
// to tell. `HasMore` is the difference between a preview and a wrong answer.
//
// The envelope is the one `rest` already returns for a collection, minus the
// fields a per-row subquery should not pay for: there is no total, because
// counting is a second aggregate on every base row and `?count=exact` on the
// child's own endpoint is where a caller asks for one deliberately.
//
// ADR-0022 records the reasoning, and the trigger that would replace this with
// an error rather than a bare slice if the envelope proves annoying.
type Collection[T any] struct {
	Items   []T  `json:"items"`
	HasMore bool `json:"has_more"`
}

// Len reports how many children were returned, which is at most the cap.
func (c Collection[T]) Len() int { return len(c.Items) }

// collectionPkgPath identifies Collection by the package that declares it, so
// an application type that happens to be called Collection is not mistaken for
// one. A generic instantiation reflects as `Collection[full/pkg.Task]`, which is
// why the name is matched by prefix rather than by equality.
var collectionPkgPath = reflect.TypeOf(Collection[struct{}]{}).PkgPath()

// collectionElem reports the child type behind a Collection field, following one
// pointer if there is one.
func collectionElem(t reflect.Type) (reflect.Type, bool) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, false
	}
	if t.PkgPath() != collectionPkgPath || !strings.HasPrefix(t.Name(), "Collection[") {
		return nil, false
	}
	items, ok := t.FieldByName("Items")
	if !ok || items.Type.Kind() != reflect.Slice {
		return nil, false
	}
	elem := items.Type.Elem()
	if elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}
	if elem.Kind() != reflect.Struct {
		return nil, false
	}
	return elem, true
}
