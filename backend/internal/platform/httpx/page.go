package httpx

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

const (
	defaultLimit = 50
	maxLimit     = 100
)

// Page is a validated ?limit=&cursor= request.
type Page struct {
	Limit  int
	Cursor string
}

// ParsePage reads limit (1-100, default 50) and an opaque cursor from the query.
func ParsePage(r *http.Request) (Page, error) {
	q := r.URL.Query()
	p := Page{Limit: defaultLimit, Cursor: q.Get("cursor")}
	fields := map[string]string{}

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLimit {
			fields["limit"] = "must be an integer between 1 and 100"
		} else {
			p.Limit = n
		}
	}
	if p.Cursor != "" {
		var v json.RawMessage
		if err := decodeCursor(p.Cursor, &v); err != nil {
			fields["cursor"] = "is not a valid cursor"
		}
	}
	if len(fields) > 0 {
		return Page{}, errs.Invalid("invalid pagination parameters", fields)
	}
	return p, nil
}

// DecodeCursor unpacks the cursor into v. An empty cursor leaves v untouched.
func (p Page) DecodeCursor(v any) error {
	if p.Cursor == "" {
		return nil
	}
	return decodeCursor(p.Cursor, v)
}

// EncodeCursor packs v (any JSON-encodable position) into an opaque cursor.
func EncodeCursor(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

type pageMeta struct {
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

type pageEnvelope struct {
	Data any      `json:"data"`
	Meta pageMeta `json:"meta"`
}

// JSONPage writes a 200 {"data": items, "meta": {next_cursor, has_more}}.
// An empty next cursor means this is the last page.
func JSONPage(w http.ResponseWriter, items any, next string) {
	write(w, http.StatusOK, pageEnvelope{Data: items, Meta: pageMeta{NextCursor: next, HasMore: next != ""}})
}
