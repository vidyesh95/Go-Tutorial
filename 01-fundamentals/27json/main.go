// Lesson 27: JSON with encoding/json
//
// Core functions:
//   - json.Marshal(v)          Go value -> []byte (JSON)
//   - json.MarshalIndent(v..)  same, pretty-printed (for humans / logs, not for wire)
//   - json.Unmarshal(b, &v)    []byte -> Go value (pass a POINTER)
//   - json.NewEncoder(w) / json.NewDecoder(r)  stream to/from an io.Writer / io.Reader
//
// Struct tags control the JSON shape: `json:"name,option,option"`
//   - `json:"user_id"`         rename the key
//   - `json:"-"`              never include (e.g. password hash)
//   - `json:"age,omitempty"`   skip when empty: false, 0, "", nil pointer/slice/map, len-0
//   - `json:"t,omitzero"`      skip when the Go zero value (or IsZero() true); added in Go 1.24.
//     This is the right choice for time.Time and structs,
//     where omitempty never works (a struct is never "empty").
//   - `json:"id,string"`       encode a number as a JSON string ("id":"42"); handy for int64 IDs
//     that JavaScript clients cannot represent exactly.
//
// Rules and gotchas:
//   - Only EXPORTED (capitalized) fields are encoded. Lowercase fields are silently skipped.
//   - Unmarshal into `any` gives map[string]any, []any, float64 (ALL numbers!), string, bool, nil.
//   - v1 Unmarshal matches keys case-insensitively and ignores unknown keys by default.
//     Use Decoder.DisallowUnknownFields() to reject typos in request bodies.
//   - A nil slice marshals to null, an empty slice to []. Initialise slices if clients expect [].
//   - Implement MarshalJSON / UnmarshalJSON to customise a type (enums, money, durations).
//   - json.RawMessage delays decoding part of a document (e.g. choose a type by "kind" field).
//   - time.Time is encoded as an RFC 3339 string automatically.
//   - ALWAYS check the error from Unmarshal and from Decode in HTTP handlers; return 400.
//
// In industry: every REST/gRPC-gateway service speaks JSON. Teams define separate
// request/response structs (DTOs) with explicit tags instead of serialising database
// models directly, decode request bodies with json.NewDecoder(r.Body) plus
// DisallowUnknownFields and a size limit (http.MaxBytesReader), and store timestamps
// as UTC RFC 3339 strings.
//
// encoding/json/v2: Go 1.27 ships a redesigned package (encoding/json/v2 plus
// encoding/json/jsontext), importable without any GOEXPERIMENT flag. It is faster, stricter
// (case-sensitive names, rejects duplicate keys) and has options-based configuration.
// Go 1.27's encoding/json (v1) is now implemented on top of v2 but keeps its old behaviour.
// New code can try v2; most libraries and codebases still use v1 today. See the end of main.
package main

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---- Struct tags -----------------------------------------------------------

type User struct {
	ID        int64     `json:"id,string"` // "id":"42"
	Name      string    `json:"name"`
	Email     string    `json:"email,omitempty"`     // dropped when ""
	Password  string    `json:"-"`                   // never serialised
	Nickname  *string   `json:"nickname"`            // nil pointer -> null
	Tags      []string  `json:"tags,omitempty"`      // dropped when nil or empty
	CreatedAt time.Time `json:"created_at"`          // RFC 3339 string
	DeletedAt time.Time `json:"deleted_at,omitzero"` // omitted when zero; omitempty would NOT work
	internal  int       // unexported: ignored
}

// ---- Custom enum encoding --------------------------------------------------

// Status is an int in Go but a readable string in JSON.
type Status int

const (
	StatusPending Status = iota
	StatusActive
	StatusClosed
)

var statusNames = []string{"pending", "active", "closed"}

func (s Status) String() string { return statusNames[s] }

// MarshalJSON uses a value receiver so it works on both values and pointers.
func (s Status) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// UnmarshalJSON needs a pointer receiver: it mutates the value.
func (s *Status) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	for i, name := range statusNames {
		if name == str {
			*s = Status(i)
			return nil
		}
	}
	return fmt.Errorf("unknown status %q", str)
}

type Order struct {
	ID     int    `json:"id"`
	Status Status `json:"status"`
}

// ---- RawMessage: decode in two steps --------------------------------------

type Event struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"` // kept as raw bytes until we know the type
}

type Click struct {
	X, Y int
}

func main() {
	fmt.Println("--- Marshal with tags ---")
	nick := "ace"
	u := User{
		ID: 42, Name: "Ada", Password: "secret", Nickname: &nick,
		CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	b, err := json.Marshal(u)
	fmt.Println(string(b), err)
	pretty, _ := json.MarshalIndent(map[string]any{"ok": true, "n": 1}, "", "  ")
	fmt.Println(string(pretty))

	fmt.Println("\n--- Unmarshal into a struct ---")
	var u2 User
	in := `{"id":"7","name":"Bob","created_at":"2024-06-01T10:00:00Z","unknown":123}`
	err = json.Unmarshal([]byte(in), &u2) // unknown key ignored by default
	fmt.Printf("name=%q err=%v\n", u2.Name, err)
	fmt.Println("ID:", u2.ID, "| CreatedAt:", u2.CreatedAt.Format(time.DateOnly))

	fmt.Println("\n--- Unmarshal into map[string]any ---")
	var generic map[string]any
	_ = json.Unmarshal([]byte(`{"a":1,"b":[true,"x"],"c":{"d":null}}`), &generic)
	fmt.Printf("%v\n", generic)
	fmt.Printf("a is %T (every JSON number becomes float64)\n", generic["a"])
	if arr, ok := generic["b"].([]any); ok { // always use the comma-ok form
		fmt.Println("b has", len(arr), "items")
	}

	fmt.Println("\n--- Bad input errors ---")
	err = json.Unmarshal([]byte(`{"name": 5}`), &u2)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		fmt.Printf("type error on field %q: got JSON %s\n", typeErr.Field, typeErr.Value)
	}
	err = json.Unmarshal([]byte(`{"name": `), &u2)
	fmt.Println("syntax problem:", err)

	fmt.Println("\n--- Decoder: streaming + DisallowUnknownFields ---")
	dec := json.NewDecoder(strings.NewReader(`{"id":1,"status":"active","oops":true}`))
	dec.DisallowUnknownFields() // turn typos in client payloads into errors
	var o Order
	fmt.Println("strict decode:", dec.Decode(&o))

	// A Decoder can read a stream of concatenated values (e.g. NDJSON logs).
	stream := json.NewDecoder(strings.NewReader(`{"id":1,"status":"pending"} {"id":2,"status":"closed"}`))
	for stream.More() {
		var o Order
		if err := stream.Decode(&o); err != nil {
			fmt.Println("stream error:", err)
			break
		}
		fmt.Printf("streamed order %d is %s\n", o.ID, o.Status)
	}

	fmt.Println("\n--- Custom MarshalJSON / UnmarshalJSON ---")
	out, _ := json.Marshal(Order{ID: 9, Status: StatusActive})
	fmt.Println(string(out))
	var o2 Order
	fmt.Println("decode:", json.Unmarshal([]byte(`{"id":10,"status":"closed"}`), &o2), o2.Status)
	fmt.Println("bad enum:", json.Unmarshal([]byte(`{"id":11,"status":"nope"}`), &o2))

	fmt.Println("\n--- json.RawMessage ---")
	var ev Event
	_ = json.Unmarshal([]byte(`{"kind":"click","payload":{"X":3,"Y":4}}`), &ev)
	switch ev.Kind {
	case "click":
		var c Click
		_ = json.Unmarshal(ev.Payload, &c) // second pass, now that we know the type
		fmt.Printf("click at (%d,%d)\n", c.X, c.Y)
	}

	fmt.Println("\n--- nil vs empty slice (v1) ---")
	type List struct {
		Items []string `json:"items"`
	}
	nilList, _ := json.Marshal(List{})
	emptyList, _ := json.Marshal(List{Items: []string{}})
	fmt.Println("nil slice:  ", string(nilList))
	fmt.Println("empty slice:", string(emptyList))

	v2Demo()
}

// v2Demo shows encoding/json/v2. Both differences below were verified by running this file.
func v2Demo() {
	fmt.Println("\n--- encoding/json/v2 (Go 1.27) ---")
	type List struct {
		Name  string   `json:"name"`
		Items []string `json:"items"`
	}

	// Difference 1: v2 encodes a nil slice as [] (v1 gives null). API shape is the same:
	// Marshal and Unmarshal, plus optional Options at the end.
	b, err := jsonv2.Marshal(List{Name: "demo"})
	fmt.Println("v2 nil slice  :", string(b), err) // v1 prints "items":null

	b1, _ := json.Marshal(List{Name: "demo"})
	fmt.Println("v1 nil slice  :", string(b1))

	// Difference 2: v2 matches member names case-SENSITIVELY. v1 was case-insensitive.
	var l1, l2 List
	_ = json.Unmarshal([]byte(`{"NAME":"x"}`), &l1)
	_ = jsonv2.Unmarshal([]byte(`{"NAME":"x"}`), &l2)
	fmt.Printf("v1 matched NAME: %q | v2 matched NAME: %q\n", l1.Name, l2.Name)

	// Options replace Decoder methods: here, reject unknown members.
	var l3 List
	err = jsonv2.Unmarshal([]byte(`{"name":"x","extra":1}`), &l3, jsonv2.RejectUnknownMembers(true))
	fmt.Println("v2 unknown member:", err)
}
