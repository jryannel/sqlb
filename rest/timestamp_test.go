package rest_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A timestamp is the one column type the response path formats itself rather
// than handing to encoding/json, because time.Time.MarshalJSON allocates a
// slice per value; see rowWriter.timestamp. That makes the format this
// package's responsibility, so it is pinned here against the encoder it used
// to go through — the same standard this package's other output is held to.
//
// The spread is the shape of the disagreements there would be if there were
// any: fractional seconds are trimmed of trailing zeros and dropped entirely
// at zero, years are padded to four digits, and a zone renders as an offset
// rather than a name. The last two are outside what the fast path accepts and
// prove the fallback still produces what MarshalJSON would.
func TestListRendersTimestampsAsEncodingJSONWould(t *testing.T) {
	stamps := []time.Time{
		time.Unix(0, 0).UTC(),
		time.Date(2026, 8, 2, 6, 44, 8, 0, time.UTC),
		time.Date(2026, 8, 2, 6, 44, 8, 123456789, time.UTC),
		time.Date(2026, 8, 2, 6, 44, 8, 100000000, time.UTC),
		time.Date(2026, 8, 2, 6, 44, 8, 1, time.UTC),
		time.Date(2026, 8, 2, 6, 44, 8, 0, time.FixedZone("CEST", 2*3600)),
		time.Date(2026, 8, 2, 6, 44, 8, 0, time.FixedZone("odd", 3661)),
		time.Date(2026, 8, 2, 6, 44, 8, 0, time.FixedZone("west", -11*3600)),
		time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(999, 12, 31, 23, 59, 59, 0, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
	}

	rows := make([][]any, len(stamps))
	for i, at := range stamps {
		rows[i] = []any{"p1", "acme", "Title", "body", "excerpt", "draft", int64(3), at}
	}

	db := newFakeDB(t, reply{cols: postCols(), rows: rows})
	opts := postOptions()
	opts.DefaultPageSize = len(stamps)
	opts.MaxPageSize = len(stamps)
	api := mount(t, db.db, opts)

	resp := api.Get("/posts")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}

	// Compared as raw JSON rather than through a decoded time, which would
	// parse both sides back into the same value and hide a formatting
	// difference instead of reporting it.
	var body struct {
		Items []struct {
			CreatedAt json.RawMessage `json:"created_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", resp.Body, err)
	}
	if len(body.Items) != len(stamps) {
		t.Fatalf("got %d items, want %d", len(body.Items), len(stamps))
	}

	for i, at := range stamps {
		want, err := at.MarshalJSON()
		if err != nil {
			t.Fatalf("the case list should only hold timestamps encoding/json accepts: %v", err)
		}
		if got := string(body.Items[i].CreatedAt); got != string(want) {
			t.Errorf("%v rendered as %s, want %s", at, got, want)
		}
	}
}

// A timestamp the fast path refuses must raise the error encoding/json would
// have raised, rather than the malformed JSON AppendFormat would have written.
// A year past 9999 is the reachable case: RFC 3339 has no way to spell it, so
// encoding/json declines to, and the guard exists to keep declining.
//
// The failure arrives as a panic because huma raises one for any response it
// cannot marshal, which is how this behaved before the fast path existed too —
// what is pinned here is that the value is refused, not how far the refusal
// travels.
func TestListRefusesTimestampsRFC3339CannotSpell(t *testing.T) {
	at := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := at.MarshalJSON(); err == nil {
		t.Fatal("this test assumes encoding/json rejects a five-digit year")
	}

	db := newFakeDB(t, reply{cols: postCols(), rows: [][]any{
		{"p1", "acme", "Title", "body", "excerpt", "draft", int64(3), at},
	}})
	api := mount(t, db.db, postOptions())

	defer func() {
		raised := fmt.Sprint(recover())
		if !strings.Contains(raised, "year outside of range") {
			t.Errorf("expected the range error, got: %s", raised)
		}
		if strings.Contains(raised, "+10000") {
			t.Errorf("the unspellable year was rendered rather than refused: %s", raised)
		}
	}()

	resp := api.Get("/posts")
	t.Fatalf("expected marshalling to fail, got %d: %s", resp.Code, resp.Body)
}
