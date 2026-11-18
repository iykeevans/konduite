package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
)

func get(t *testing.T, query string) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/items?"+query, nil)
}

func TestParsePageDefaultsTo50(t *testing.T) {
	p, err := httpx.ParsePage(get(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if p.Limit != 50 || p.Cursor != "" {
		t.Errorf("page = %+v, want limit 50 and no cursor", p)
	}
}

func TestParsePageRejectsBadLimits(t *testing.T) {
	for _, q := range []string{"limit=0", "limit=101", "limit=-5", "limit=ten"} {
		t.Run(q, func(t *testing.T) {
			_, err := httpx.ParsePage(get(t, q))
			e, ok := errs.As(err)
			if !ok || e.Kind != errs.KindInvalid || e.Fields["limit"] == "" {
				t.Errorf("err = %v, want invalid with a limit field error", err)
			}
		})
	}
}

func TestCursorRoundTrips(t *testing.T) {
	type position struct {
		Seq int64 `json:"seq"`
	}
	cursor := httpx.EncodeCursor(position{Seq: 42})

	p, err := httpx.ParsePage(get(t, "limit=10&cursor="+url.QueryEscape(cursor)))
	if err != nil {
		t.Fatal(err)
	}
	var got position
	if err := p.DecodeCursor(&got); err != nil {
		t.Fatal(err)
	}
	if p.Limit != 10 || got.Seq != 42 {
		t.Errorf("limit=%d seq=%d, want 10 and 42", p.Limit, got.Seq)
	}
}

func TestParsePageRejectsMalformedCursor(t *testing.T) {
	_, err := httpx.ParsePage(get(t, "cursor=not-a-cursor!!"))
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.KindInvalid || e.Fields["cursor"] == "" {
		t.Errorf("err = %v, want invalid with a cursor field error", err)
	}
}

func TestJSONPageWritesMeta(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.JSONPage(rec, []string{"a", "b"}, "next123")

	var body struct {
		Data []string `json:"data"`
		Meta struct {
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Data) != 2 || body.Meta.NextCursor != "next123" || !body.Meta.HasMore {
		t.Errorf("got %d %+v, want 200 with 2 items, next_cursor next123, has_more true", rec.Code, body)
	}

	rec = httptest.NewRecorder()
	httpx.JSONPage(rec, []string{}, "")
	var last struct {
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil {
		t.Fatal(err)
	}
	if _, present := last.Meta["next_cursor"]; present || last.Meta["has_more"] != false {
		t.Errorf("last page meta = %v, want has_more false and no next_cursor", last.Meta)
	}
}
