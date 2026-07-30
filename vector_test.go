package sqlb

import (
	"math"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// The codec is the piece ADR-0040 unlocked, so it is worth testing without a
// database as well as with one: pgtest proves Postgres accepts what this
// writes, and these prove the two directions are inverses — including for the
// values that break a naive float codec.
//
// Both formats are covered because both are reachable. Binary is what a
// registered codec sends; text is what an unregistered one falls back to, and a
// fallback nobody tests is a fallback that stops working quietly.
func TestVectorRoundTripsInBothFormats(t *testing.T) {
	cases := []struct {
		name string
		in   Vector
	}{
		{"ordinary", Vector{1, -2.5, 3.25}},
		{"empty", Vector{}},
		{"zero", Vector{0, 0, 0}},
		// A float32 whose decimal form is not exact, which is what catches a
		// codec rendering through float64 and back.
		{"not exactly representable", Vector{0.1, 0.2, 0.3}},
		{"tiny and huge", Vector{math.SmallestNonzeroFloat32, math.MaxFloat32}},
		{"negative zero", Vector{float32(math.Copysign(0, -1))}},
	}

	codec := vectorCodec{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, format := range []struct {
				name string
				code int16
			}{
				{"binary", pgtype.BinaryFormatCode},
				{"text", pgtype.TextFormatCode},
			} {
				enc := codec.PlanEncode(nil, 0, format.code, tc.in)
				if enc == nil {
					t.Fatalf("%s: no encode plan for a Vector", format.name)
				}
				buf, err := enc.Encode(tc.in, nil)
				if err != nil {
					t.Fatalf("%s: encode: %v", format.name, err)
				}

				var got Vector
				scan := codec.PlanScan(nil, 0, format.code, &got)
				if scan == nil {
					t.Fatalf("%s: no scan plan for a *Vector", format.name)
				}
				if err := scan.Scan(buf, &got); err != nil {
					t.Fatalf("%s: scan: %v", format.name, err)
				}
				if !reflect.DeepEqual(got, tc.in) {
					t.Errorf("%s: round trip = %v, want %v", format.name, got, tc.in)
				}
			}
		})
	}
}

// The binary header is what a hand-written codec gets wrong, so it is asserted
// rather than inferred from a round trip that would agree with itself.
func TestVectorBinaryLayout(t *testing.T) {
	buf, err := encodeVectorBinary{}.Encode(Vector{1, 2}, nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := []byte{
		0, 2, // two dimensions
		0, 0, // pgvector's reserved word
		0x3f, 0x80, 0x00, 0x00, // 1.0, big-endian
		0x40, 0x00, 0x00, 0x00, // 2.0
	}
	if !reflect.DeepEqual(buf, want) {
		t.Errorf("binary layout = % x, want % x", buf, want)
	}
}

// A truncated or mislabelled payload is refused rather than read past. The
// dimension count arrives from the wire, so trusting it would be an
// out-of-bounds read driven by whatever the connection said.
func TestVectorBinaryRefusesAMalformedPayload(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  []byte
	}{
		{"no header", []byte{0, 1}},
		{"claims more components than it carries", []byte{0, 4, 0, 0, 0x3f, 0x80, 0, 0}},
		{"carries more than it claims", []byte{0, 1, 0, 0, 0x3f, 0x80, 0, 0, 0x40, 0, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v Vector
			if err := decodeVectorBinary(tc.src, &v); err == nil {
				t.Errorf("decoded %v from a malformed payload, want an error", v)
			}
		})
	}
}

func TestVectorTextRefusesNonsense(t *testing.T) {
	for _, src := range []string{"", "1,2,3", "[1,2", "[a,b]", "[1,,2]"} {
		var v Vector
		if err := decodeVectorText([]byte(src), &v); err == nil {
			t.Errorf("decoded %v from %q, want an error", v, src)
		}
	}
}

// NULL is not the empty vector, and the difference survives. A column that
// holds no embedding yet and one that holds a zero-length one are different
// facts, and pgvector will not store the second anyway.
func TestVectorScansNullAsNil(t *testing.T) {
	got := Vector{1, 2, 3}
	if err := (scanVectorBinary{}).Scan(nil, &got); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != nil {
		t.Errorf("NULL scanned as %v, want nil", got)
	}
}

func TestVectorStringIsThePgvectorLiteral(t *testing.T) {
	if got := (Vector{1, -2.5, 0.1}).String(); got != "[1,-2.5,0.1]" {
		t.Errorf("String() = %q", got)
	}
	if got := (Vector{}).String(); got != "[]" {
		t.Errorf("empty String() = %q", got)
	}
}
