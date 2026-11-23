// Lesson 14: Strings, runes and bytes
//
// A Go string is an IMMUTABLE sequence of BYTES. By convention (and in source
// code literals) those bytes are UTF-8 encoded text.
//
//	byte  = uint8   one byte
//	rune  = int32   one Unicode code point (what a human may call a "character")
//
// Key rules:
//   - len(s) counts BYTES, not characters. "héllo" has len 6 but 5 runes.
//   - s[i] is a byte. To work with characters, convert to []rune or use
//     `for i, r := range s`, which decodes UTF-8 for you (lesson 13).
//   - Strings are immutable: s[0] = 'x' does not compile. "Modifying" a string
//     always creates a new one.
//   - `+` in a loop copies the string every time (quadratic). Use strings.Builder.
//   - Raw string literals use backticks and keep newlines and backslashes as-is.
//   - Strings compare with == and < (byte-wise), and can be map keys.
//   - Even a "character" a user sees can be several runes (e.g. "e" + combining
//     accent, or emoji sequences). For true user-perceived characters you need
//     a third-party library; for most backend work bytes or runes suffice.
//
// Packages used here:
//
//	strings        searching, splitting, joining, trimming, building
//	unicode/utf8   rune counting, decoding, validating
//	strconv        convert to and from numbers and bools
//
// Gotcha: slicing a string by byte index (s[:3]) can cut a multi-byte rune in
// half and produce invalid UTF-8. Truncate on rune boundaries when the text
// is user-facing.
//
// In industry: you parse and build strings constantly: query strings, log
// lines, headers, config. The practical toolkit is strings.Cut (split once),
// strings.Fields, strings.Builder, strconv.Atoi/ParseInt with error handling,
// and utf8.ValidString for untrusted input. Prefer strings.Cut over Index plus
// slicing, and never build SQL or shell commands by concatenating user input.
package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

func main() {
	fmt.Println("Strings in Go")

	// --- bytes vs runes -------------------------------------------------
	s := "héllo, 世界"
	fmt.Println("string:", s)
	fmt.Println("len (bytes):", len(s))
	fmt.Println("rune count:", utf8.RuneCountInString(s))

	// s[1] is the FIRST BYTE of 'é', not the letter.
	fmt.Printf("s[1] = %d (0x%x) -> not a full character\n", s[1], s[1])

	// Converting to []rune gives one element per code point.
	runes := []rune(s)
	fmt.Println("runes[1]:", string(runes[1]), "| len:", len(runes))

	// range decodes UTF-8: i is the byte offset, r the rune.
	for i, r := range "aé世" {
		fmt.Printf("  offset %d: %c (%d bytes)\n", i, r, utf8.RuneLen(r))
	}

	// DecodeRuneInString reads the first rune manually.
	r, size := utf8.DecodeRuneInString("世界")
	fmt.Printf("first rune: %c size: %d\n", r, size)

	// Validating untrusted input.
	bad := "abc\xff"
	fmt.Println("valid UTF-8:", utf8.ValidString(s), "| bad valid:", utf8.ValidString(bad))

	// []byte and string conversions copy the data.
	b := []byte("go")
	b[0] = 'G'
	fmt.Println("modified bytes:", string(b))

	// Raw string literal: no escapes, may span lines.
	raw := `C:\temp\new`
	fmt.Println("raw:", raw)

	// --- searching and testing ------------------------------------------
	text := "The quick brown fox"
	fmt.Println("Contains 'quick':", strings.Contains(text, "quick"))
	fmt.Println("HasPrefix 'The':", strings.HasPrefix(text, "The"))
	fmt.Println("HasSuffix 'fox':", strings.HasSuffix(text, "fox"))
	fmt.Println("Index of 'brown':", strings.Index(text, "brown"))
	fmt.Println("Count of 'o':", strings.Count(text, "o"))
	fmt.Println("EqualFold:", strings.EqualFold("Go", "GO"))

	// --- transforming ---------------------------------------------------
	fmt.Println("ToUpper:", strings.ToUpper(text))
	fmt.Println("Replace:", strings.ReplaceAll(text, "quick", "slow"))
	fmt.Printf("TrimSpace: %q\n", strings.TrimSpace("  padded \n"))
	fmt.Println("Trim chars:", strings.Trim("--id--", "-"))
	fmt.Println("TrimPrefix:", strings.TrimPrefix("v1.2.3", "v"))
	fmt.Println("Repeat:", strings.Repeat("ab", 3))

	// --- splitting and joining ------------------------------------------
	parts := strings.Split("a,b,c", ",")
	fmt.Printf("Split: %q\n", parts)
	fmt.Println("Join:", strings.Join(parts, "-"))

	// Fields splits on any run of whitespace and drops empty fields.
	fmt.Printf("Fields: %q\n", strings.Fields("  many   spaces\there "))

	// strings.Cut splits ONCE at the first separator: perfect for key=value.
	key, value, found := strings.Cut("name=ada=lovelace", "=")
	fmt.Println("Cut:", key, "|", value, "|", found)

	// strings.CutLast (Go 1.27) splits at the LAST separator: file extensions.
	base, ext, ok := strings.CutLast("archive.tar.gz", ".")
	fmt.Println("CutLast:", base, "|", ext, "|", ok)

	// Iterator versions (Go 1.24+) avoid allocating a slice.
	fmt.Print("SplitSeq:")
	for piece := range strings.SplitSeq("x;y;z", ";") {
		fmt.Print(" [", piece, "]")
	}
	fmt.Println()

	fmt.Print("FieldsSeq:")
	for w := range strings.FieldsSeq("one two  three") {
		fmt.Print(" [", w, "]")
	}
	fmt.Println()

	// Lines keeps the trailing newline on each line.
	multi := "first\nsecond\nthird"
	for line := range strings.Lines(multi) {
		fmt.Printf("  line: %q\n", line)
	}

	// --- building efficiently -------------------------------------------
	// strings.Builder appends without copying the whole string every time.
	var sb strings.Builder
	sb.Grow(32) // optional: preallocate if you know the size
	for i := range 3 {
		fmt.Fprintf(&sb, "item%d;", i) // Builder implements io.Writer
	}
	sb.WriteString("done")
	sb.WriteByte('!')
	sb.WriteRune('✓')
	fmt.Println("Builder:", sb.String(), "| len:", sb.Len())

	// --- strconv: strings <-> numbers -----------------------------------
	n, err := strconv.Atoi("123")
	fmt.Println("Atoi:", n, err)

	_, err = strconv.Atoi("12a")
	fmt.Println("Atoi error:", err)

	big, err := strconv.ParseInt("ff", 16, 64) // base 16, fit in 64 bits
	fmt.Println("ParseInt hex:", big, err)

	f, _ := strconv.ParseFloat("3.14", 64)
	flag, _ := strconv.ParseBool("true")
	fmt.Println("ParseFloat:", f, "| ParseBool:", flag)

	fmt.Println("Itoa:", strconv.Itoa(99)+" bottles")
	fmt.Println("Quote:", strconv.Quote("tab\there"))

	// Do NOT write string(65): it yields "A" (a rune), not "65".
	fmt.Println("string(rune(65)):", string(rune(65)), "| Itoa(65):", strconv.Itoa(65))
}
