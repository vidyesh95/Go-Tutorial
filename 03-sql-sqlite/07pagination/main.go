// Lesson 07: pagination, OFFSET vs keyset (cursor) pagination.
//
// Never return an unbounded list from an API. Two ways to page through results:
//
//  1. OFFSET / LIMIT      SELECT ... ORDER BY priority, id LIMIT 5 OFFSET 10
//     + trivial, supports "jump to page 7"
//     - the database still reads and throws away all skipped rows, so page 1000 is slow: O(offset)
//     - unstable when data changes between requests: an insert before your page shifts everything,
//     so you see a row twice; a delete makes you skip a row
//
//  2. KEYSET (cursor)     SELECT ... WHERE (priority, id) > (?, ?) ORDER BY priority, id LIMIT 5
//     "give me the rows AFTER the last one I saw"
//     + uses an index to jump straight there: O(log n) no matter how deep
//     + stable under inserts and deletes
//     - only next/previous, no "jump to page N"
//     - the ORDER BY must be a total order: add a unique tie-breaker (id) or rows can be skipped
//
// The client gets an opaque cursor (base64 of the last row's sort key) instead of raw ids, so
// the server is free to change the format later and clients cannot depend on its internals.
// Treat cursors as untrusted input: decode defensively and return 400 on garbage.
//
// Row-value comparison (a, b) > (?, ?) is supported by SQLite and PostgreSQL. It means
// a > ? OR (a = ? AND b > ?). Fetch limit+1 rows to learn whether a next page exists without
// running a COUNT.
//
// In industry: public APIs (GitHub, Stripe, Slack) all use cursors for large collections;
// admin screens with small tables often keep OFFSET because "page 3 of 12" is a product need.
package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/vidyesh95/Go-Tutorial/03-sql-sqlite/internal/sqldb"
)

type Item struct {
	ID       int64
	Title    string
	Priority int
}

func (i Item) String() string { return fmt.Sprintf("#%d(p%d)", i.ID, i.Priority) }

// ---- Cursor encoding ----

// cursor is the sort key of the last row on the previous page.
type cursor struct {
	Priority int
	ID       int64
}

// encodeCursor produces an opaque, URL-safe token.
func encodeCursor(c cursor) string {
	raw := fmt.Sprintf("%d:%d", c.Priority, c.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

var errBadCursor = errors.New("invalid cursor")

func decodeCursor(s string) (cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, errBadCursor
	}
	prio, id, ok := strings.Cut(string(raw), ":")
	if !ok {
		return cursor{}, errBadCursor
	}
	p, err1 := strconv.Atoi(prio)
	i, err2 := strconv.ParseInt(id, 10, 64)
	if err1 != nil || err2 != nil {
		return cursor{}, errBadCursor
	}
	return cursor{Priority: p, ID: i}, nil
}

// ---- Queries ----

func pageOffset(ctx context.Context, db *sql.DB, limit, offset int) []Item {
	return queryItems(ctx, db,
		`SELECT id, title, priority FROM tasks ORDER BY priority, id LIMIT ? OFFSET ?`, limit, offset)
}

// pageKeyset returns up to limit items after the cursor, plus the cursor for the next page
// ("" when there are no more).
func pageKeyset(ctx context.Context, db *sql.DB, limit int, after string) ([]Item, string, error) {
	var items []Item
	if after == "" {
		items = queryItems(ctx, db,
			`SELECT id, title, priority FROM tasks ORDER BY priority, id LIMIT ?`, limit+1)
	} else {
		c, err := decodeCursor(after)
		if err != nil {
			return nil, "", err
		}
		items = queryItems(ctx, db,
			`SELECT id, title, priority FROM tasks
WHERE (priority, id) > (?, ?)
ORDER BY priority, id
LIMIT ?`, c.Priority, c.ID, limit+1)
	}
	next := ""
	if len(items) > limit { // we fetched one extra row: it proves a next page exists
		items = items[:limit]
		last := items[len(items)-1]
		next = encodeCursor(cursor{Priority: last.Priority, ID: last.ID})
	}
	return items, next, nil
}

func queryItems(ctx context.Context, db *sql.DB, q string, args ...any) []Item {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.Title, &it.Priority); err != nil {
			log.Fatal(err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
	return out
}

func main() {
	ctx := context.Background()
	db, cleanup, err := sqldb.OpenTemp(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	const pageSize = 5

	fmt.Println("== OFFSET / LIMIT ==")
	for page := range 3 {
		fmt.Printf("page %d (OFFSET %2d): %v\n", page+1, page*pageSize, pageOffset(ctx, db, pageSize, page*pageSize))
	}

	fmt.Println("\n== Keyset with an opaque cursor ==")
	cur := ""
	for page := 1; ; page++ {
		items, next, err := pageKeyset(ctx, db, pageSize, cur)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("page %d (cursor=%-8q): %v -> next=%q\n", page, cur, items, next)
		if next == "" {
			break
		}
		cur = next
	}

	fmt.Println("\n== Cursor round trip and bad input ==")
	tok := encodeCursor(cursor{Priority: 2, ID: 7})
	c, err := decodeCursor(tok)
	fmt.Printf("encode -> %q, decode -> %+v, err=%v\n", tok, c, err)
	for _, bad := range []string{"%%%", "bm90LWEtY3Vyc29y", ""} {
		_, err := decodeCursor(bad)
		fmt.Printf("decode(%q) -> %v\n", bad, err)
	}

	fmt.Println("\n== Data changes between page requests ==")
	// A new URGENT task (priority 1) appears after the client already fetched page 1.
	// Both clients have read page 1 and are about to ask for page 2.
	_, next, _ := pageKeyset(ctx, db, pageSize, "")
	page1 := pageOffset(ctx, db, pageSize, 0)
	lastOfPage1 := page1[len(page1)-1]
	fmt.Println("page 1 (seen by both clients):", page1)

	if _, err := db.ExecContext(ctx, `INSERT INTO tasks (id, project_id, title, priority) VALUES (100, 1, 'Fresh urgent task', 1)`); err != nil {
		log.Fatal(err)
	}
	fmt.Println("-- inserted new task #100 with priority 1 (sorts into page 1's range) --")

	offsetPage2 := pageOffset(ctx, db, pageSize, pageSize)
	keysetPage2, _, _ := pageKeyset(ctx, db, pageSize, next)
	fmt.Println("OFFSET page 2:", offsetPage2)
	fmt.Println("KEYSET page 2:", keysetPage2)
	fmt.Printf("last item of page 1 was %v. ", lastOfPage1)
	if len(offsetPage2) > 0 && offsetPage2[0].ID == lastOfPage1.ID {
		fmt.Println("OFFSET shows it AGAIN at the top of page 2 (duplicate).")
	}
	fmt.Println("KEYSET continues exactly after it, no duplicate.")

	fmt.Println("\n== Why OFFSET gets slow: the rows skipped are still read ==")
	fmt.Println("OFFSET 100000 LIMIT 5 reads 100005 index entries and returns 5. Keyset reads ~5.")
	fmt.Println("Run the EXPLAIN from lesson 06 on both to compare plans.")
}
