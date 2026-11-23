package httpapi

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"uuid"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// Keyset ("cursor") pagination, as opposed to OFFSET pagination:
//
//	OFFSET: WHERE ... ORDER BY id LIMIT 20 OFFSET 4000  -- scans and throws away 4000 rows, and
//	        skips or repeats rows if anything is inserted/deleted between page requests.
//	KEYSET: WHERE ... AND id > :last_seen ORDER BY id LIMIT 20  -- jumps straight to the right
//	        place in the index; stable under concurrent writes.
//
// The cursor is the last id of the previous page, base64-encoded so clients treat it as opaque
// (and we stay free to change what is inside later, e.g. add a sort key, without breaking them).
// It carries no authority: the query still filters by owner/project, so a forged or stolen
// cursor can only ever point somewhere inside data the caller may already see.

func encodeCursor(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

// decodeCursor returns the id inside a cursor; "" means "first page".
func decodeCursor(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		// Ids are UUIDs. Checking the shape means arbitrary client bytes never reach the query.
		_, err = uuid.Parse(string(raw))
	}
	if err != nil {
		return "", domain.NewValidationError("cursor", "is not a valid cursor")
	}
	return string(raw), nil
}

// pageParams reads ?cursor= and ?limit=. limit 0 means "default"; values above the maximum are
// clamped by the service rather than rejected (the client asked for "as many as you'll give").
func pageParams(r *http.Request) (afterID string, limit int, err error) {
	q := r.URL.Query()
	if afterID, err = decodeCursor(q.Get("cursor")); err != nil {
		return "", 0, err
	}
	if v := q.Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 {
			return "", 0, domain.NewValidationError("limit", "must be a positive integer")
		}
	}
	return afterID, limit, nil
}

// pageResponse is the JSON envelope for list endpoints. NextCursor is null on the last page.
type pageResponse[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func newPageResponse[T any](p domain.Page[T], idOf func(T) string) pageResponse[T] {
	items := p.Items
	if items == nil {
		items = []T{} // encode as [] not null, so clients can always iterate
	}
	resp := pageResponse[T]{Items: items}
	if p.HasMore && len(items) > 0 {
		c := encodeCursor(idOf(items[len(items)-1]))
		resp.NextCursor = &c
	}
	return resp
}
