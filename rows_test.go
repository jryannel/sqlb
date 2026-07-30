package sqlb

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// The refactor that introduced rowSource claims two things at once, and
// ADR-0016 asks that a guard be proven in both directions rather than in the
// one that happens to be convenient:
//
//   - a result set that is not *sql.Rows scans, which is the new capability and
//     the whole reason the interface exists;
//   - *sql.Rows still satisfies it, which is the thing that must not have
//     regressed, since it is the only row source anything in production uses.
//
// The second is a compile-time assertion because that is what it is. Widening
// rowSource with a method *sql.Rows lacks would break every real caller, and
// this line says so at the point of the mistake rather than fifty files away.
var _ rowSource = (*sql.Rows)(nil)

// fakeRows is a row source with no driver, no connection and no database/sql
// anywhere underneath it. If the scanners can read it, they are not written
// against the standard library's concrete type — which is exactly what a pgx
// adapter would need to be true.
type fakeRows struct {
	cols   []string
	data   [][]any
	pos    int
	err    error // reported by Err after iteration, as a mid-stream failure is
	closed bool
}

func (r *fakeRows) Columns() ([]string, error) { return r.cols, nil }

func (r *fakeRows) Next() bool {
	if r.err != nil || r.pos >= len(r.data) {
		return false
	}
	r.pos++
	return true
}

func (r *fakeRows) Err() error { return r.err }

func (r *fakeRows) Close() error {
	r.closed = true
	return nil
}

// Scan follows database/sql's convention closely enough for the scanners to be
// exercised honestly: sql.Scanner destinations are offered the raw value, which
// is the path array columns take, and everything else is assigned by reflection.
func (r *fakeRows) Scan(dest ...any) error {
	if r.pos == 0 || r.pos > len(r.data) {
		return errors.New("fakeRows: Scan called outside a row")
	}
	row := r.data[r.pos-1]
	if len(dest) != len(row) {
		return fmt.Errorf("fakeRows: %d destinations for %d columns", len(dest), len(row))
	}
	for i, d := range dest {
		if s, ok := d.(sql.Scanner); ok {
			if err := s.Scan(row[i]); err != nil {
				return err
			}
			continue
		}
		dv := reflect.ValueOf(d)
		if dv.Kind() != reflect.Pointer || dv.IsNil() {
			return fmt.Errorf("fakeRows: destination %d is not a non-nil pointer", i)
		}
		elem := dv.Elem()
		if row[i] == nil {
			elem.Set(reflect.Zero(elem.Type()))
			continue
		}
		sv := reflect.ValueOf(row[i])
		switch {
		// The discard target for an unmapped column is a *any.
		case elem.Kind() == reflect.Interface:
			elem.Set(sv)
		case sv.Type().AssignableTo(elem.Type()):
			elem.Set(sv)
		default:
			return fmt.Errorf("fakeRows: cannot assign %s to %s", sv.Type(), elem.Type())
		}
	}
	return nil
}

type rowSourceRow struct {
	ID   int64    `db:"id"`
	Name string   `db:"name"`
	Tags []string `db:"tags"`
}

func TestScanReadsARowSourceThatIsNotSQLRows(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"id", "name", "tags"},
		data: [][]any{
			{int64(1), "first", []byte("{a,b}")},
			{int64(2), "second", []byte("{}")},
		},
	}

	got, err := scanAll[rowSourceRow](rows, ModelOf[rowSourceRow]())
	if err != nil {
		t.Fatalf("scanAll: %v", err)
	}

	want := []rowSourceRow{
		{ID: 1, Name: "first", Tags: []string{"a", "b"}},
		{ID: 2, Name: "second", Tags: []string{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scanAll = %#v, want %#v", got, want)
	}
}

// The array column above is the part worth stating separately: it reaches the
// destination as an arrayScanner rather than as the field's own address, so a
// row source that never produces a *sql.Rows still has to satisfy sql.Scanner
// destinations. Anything claiming to be a pgx adapter has to do the same.
func TestScanDiscardsUnmappedColumnsFromARowSource(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"id", "name", "tags", "computed_rank"},
		data: [][]any{{int64(7), "kept", []byte("{}"), int64(99)}},
	}

	got, err := scanAll[rowSourceRow](rows, ModelOf[rowSourceRow]())
	if err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	if len(got) != 1 || got[0].ID != 7 || got[0].Name != "kept" {
		t.Fatalf("scanAll = %#v, want the row with the extra column discarded", got)
	}
}

func TestScanPropagatesRowSourceErr(t *testing.T) {
	boom := errors.New("connection reset mid-stream")
	rows := &fakeRows{cols: []string{"id", "name", "tags"}, err: boom}

	if _, err := scanAll[rowSourceRow](rows, ModelOf[rowSourceRow]()); !errors.Is(err, boom) {
		t.Errorf("scanAll error = %v, want it to carry %v", err, boom)
	}
}

func TestScanCountFromARowSource(t *testing.T) {
	t.Run("a row", func(t *testing.T) {
		rows := &fakeRows{cols: []string{"count"}, data: [][]any{{int64(42)}}}
		n, err := scanCount(rows)
		if err != nil {
			t.Fatalf("scanCount: %v", err)
		}
		if n != 42 {
			t.Errorf("scanCount = %d, want 42", n)
		}
	})

	// A grouped count over no groups returns no rows rather than a zero.
	t.Run("no rows", func(t *testing.T) {
		rows := &fakeRows{cols: []string{"count"}}
		n, err := scanCount(rows)
		if err != nil {
			t.Fatalf("scanCount: %v", err)
		}
		if n != 0 {
			t.Errorf("scanCount = %d, want 0", n)
		}
	})

	// An empty result set and a failed one are indistinguishable through Next
	// alone, which is why scanCount reads Err before reporting nought.
	t.Run("failure is not zero", func(t *testing.T) {
		boom := errors.New("statement timeout")
		rows := &fakeRows{cols: []string{"count"}, err: boom}
		if _, err := scanCount(rows); !errors.Is(err, boom) {
			t.Errorf("scanCount error = %v, want it to carry %v", err, boom)
		}
	})
}

func TestScanExistsFromARowSource(t *testing.T) {
	t.Run("a row", func(t *testing.T) {
		rows := &fakeRows{cols: []string{"?column?"}, data: [][]any{{int64(1)}}}
		found, err := scanExists(rows)
		if err != nil {
			t.Fatalf("scanExists: %v", err)
		}
		if !found {
			t.Error("scanExists = false, want true")
		}
	})

	t.Run("no rows", func(t *testing.T) {
		rows := &fakeRows{cols: []string{"?column?"}}
		found, err := scanExists(rows)
		if err != nil {
			t.Fatalf("scanExists: %v", err)
		}
		if found {
			t.Error("scanExists = true, want false")
		}
	})

	// The failure mode this ordering exists for: a query that errored before
	// its first row must not be reported as "no such row".
	t.Run("failure is not absence", func(t *testing.T) {
		boom := errors.New("deadlock detected")
		rows := &fakeRows{cols: []string{"?column?"}, err: boom}
		if _, err := scanExists(rows); !errors.Is(err, boom) {
			t.Errorf("scanExists error = %v, want it to carry %v", err, boom)
		}
	})
}
