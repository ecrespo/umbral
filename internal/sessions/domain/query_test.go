package domain

import (
	"errors"
	"testing"
	"time"
)

func TestCursorRoundTrips(t *testing.T) {
	cases := []struct {
		name string
		want Cursor
	}{
		{"list", Cursor{StartedAt: time.UnixMilli(1_757_592_001_000).UTC(), BlockID: "blk_abc"}},
		{"search", SearchCursor(4242)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCursor(tc.want.String())
			if err != nil {
				t.Fatalf("ParseCursor: %v", err)
			}
			if got != tc.want {
				t.Errorf("round-tripped to %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCursorShapesAreDistinguishable(t *testing.T) {
	// A search cursor handed to block.list, or the reverse, names a position in an
	// ordering it does not belong to. The shape letter is what lets the caller tell.
	list := Cursor{StartedAt: time.UnixMilli(1000).UTC(), BlockID: "blk_abc"}
	search := SearchCursor(99)

	parsedList, err := ParseCursor(list.String())
	if err != nil {
		t.Fatal(err)
	}
	parsedSearch, err := ParseCursor(search.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsedList.Row != 0 {
		t.Errorf("a list cursor parsed with a row: %+v", parsedList)
	}
	if parsedSearch.BlockID != "" {
		t.Errorf("a search cursor parsed with a block id: %+v", parsedSearch)
	}
}

func TestParseCursorRejectsRubbish(t *testing.T) {
	for _, token := range []string{
		"not base64!!", "", "eA", // "x": no separator
		"dHwxMjN8",   // "t|123|": empty id
		"dHxub3B8eA", // "t|nop|x": unparseable time
		"cnwwMA",     // "r|00": a zero row is the first page, not a cursor
		"enwx",       // "z|1": unknown shape
	} {
		got, err := ParseCursor(token)
		if token == "" {
			if err != nil || !got.IsZero() {
				t.Errorf("an empty token gave %+v, %v; want the first page", got, err)
			}
			continue
		}
		if !errors.Is(err, ErrValidation) {
			t.Errorf("ParseCursor(%q) gave %+v, %v; want a validation error", token, got, err)
		}
	}
}

func TestEmptyCursorIsTheFirstPage(t *testing.T) {
	var zero Cursor
	if !zero.IsZero() || zero.String() != "" {
		t.Errorf("the zero cursor is %+v with token %q", zero, zero.String())
	}
}

func TestBlockFilterDefaultsAndBounds(t *testing.T) {
	filter := BlockFilter{}
	if err := filter.Validate(); err != nil {
		t.Fatalf("an empty filter was refused: %v", err)
	}
	if filter.Limit != DefaultPageLimit {
		t.Errorf("limit defaulted to %d, want %d", filter.Limit, DefaultPageLimit)
	}

	for _, limit := range []int{-1, MaxPageLimit + 1} {
		bad := BlockFilter{Limit: limit}
		if err := bad.Validate(); !errors.Is(err, ErrValidation) {
			t.Errorf("limit %d gave %v, want a validation error", limit, err)
		}
	}
	bad := BlockFilter{Origin: "nobody"}
	if err := bad.Validate(); !errors.Is(err, ErrValidation) {
		t.Errorf("an unknown origin gave %v", err)
	}
	bad = BlockFilter{State: "melted"}
	if err := bad.Validate(); !errors.Is(err, ErrValidation) {
		t.Errorf("an unknown state gave %v", err)
	}
}

func TestSearchQueryBounds(t *testing.T) {
	empty := SearchQuery{}
	if err := empty.Validate(); !errors.Is(err, ErrValidation) {
		t.Errorf("an empty query gave %v, want a validation error", err)
	}

	long := SearchQuery{Query: string(make([]byte, MaxSearchQuery+1))}
	if err := long.Validate(); !errors.Is(err, ErrValidation) {
		t.Errorf("an over-long query gave %v, want a validation error", err)
	}

	ok := SearchQuery{Query: "FAIL"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a valid query was refused: %v", err)
	}
	if ok.Limit != DefaultPageLimit {
		t.Errorf("limit defaulted to %d", ok.Limit)
	}
}
