package main

import (
	"maps"
	"slices"
	"sync"
	"unicode/utf8"
)

// store.go is IDENTICAL in stdlib/, chi/, gin/ and fiber/ on purpose: the only thing that
// changes between the four folders is the web layer in main.go, so you can diff them.

// Note is the one resource of this API.
type Note struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// Store is a concurrency-safe in-memory repository. Every HTTP server runs each request in
// its own goroutine, so shared state needs a lock (see 02-concurrency).
type Store struct {
	mu    sync.RWMutex
	next  int
	notes map[int]Note
}

func NewStore() *Store {
	return &Store{next: 1, notes: make(map[int]Note)}
}

// List returns all notes ordered by id (map iteration order is random, so we sort).
func (s *Store) List() []Note {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Note, 0, len(s.notes)) // non-nil: JSON encodes [] instead of null
	for _, id := range slices.Sorted(maps.Keys(s.notes)) {
		out = append(out, s.notes[id])
	}
	return out
}

func (s *Store) Create(title, body string) Note {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := Note{ID: s.next, Title: title, Body: body}
	s.notes[n.ID] = n
	s.next++
	return n
}

func (s *Store) Get(id int) (Note, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.notes[id]
	return n, ok
}

func (s *Store) Delete(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.notes[id]
	delete(s.notes, id)
	return ok
}

// maxTitleRunes is the validation limit. We count runes (characters), not bytes, so
// "héllo" is 5, not 6.
const maxTitleRunes = 100

// validateTitle returns "" when the title is fine, otherwise a human readable reason.
func validateTitle(title string) string {
	switch {
	case title == "":
		return "title is required"
	case utf8.RuneCountInString(title) > maxTitleRunes:
		return "title must be at most 100 characters"
	}
	return ""
}
