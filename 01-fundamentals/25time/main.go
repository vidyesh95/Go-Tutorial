// Lesson 25: The time package
//
// Core types:
//   - time.Time     an instant (wall clock + optional monotonic reading + Location)
//   - time.Duration an elapsed time in nanoseconds (an int64). 1500*time.Millisecond is idiomatic.
//   - time.Location a time zone
//
// Key rules and gotchas:
//   - Formatting uses a REFERENCE TIME, not %Y-%m-%d codes. The reference is
//     "Mon Jan 2 15:04:05 MST 2006" (1 2 3 4 5 6 7 = month 01, day 02, hour 03/15,
//     minute 04, second 05, year 06/2006, zone 07). So "2006-01-02" means YYYY-MM-DD.
//   - Prefer predefined layouts: time.RFC3339, time.DateOnly, time.DateTime, time.Kitchen.
//   - Parse with time.Parse (assumes UTC if no zone in the input) or
//     time.ParseInLocation when the text has no zone but you know it.
//   - Duration is NOT a number of seconds; use d.Seconds() / d.Milliseconds().
//     Never multiply two Durations: time.Second * time.Second is nonsense.
//   - Compare with t.Before / t.After / t.Equal. Do NOT use == on Time
//     (it also compares Location and monotonic reading).
//   - Adding calendar units: t.AddDate(0, 1, 0) adds one month; t.Add(24*time.Hour)
//     adds exactly 24 hours (differs across daylight-saving changes).
//   - Monotonic clock: time.Now() carries a monotonic reading, so time.Since(start)
//     is immune to wall-clock jumps (NTP, manual changes). Use it for measuring
//     elapsed time. Formatting or t.Round(0) strips it.
//   - Timers and Tickers: time.NewTimer fires once, time.NewTicker fires repeatedly.
//     Always Stop() a Ticker. For "wait or cancel" logic prefer context deadlines.
//   - In tests, don't depend on the real clock: inject a `now func() time.Time`.
//
// In industry: store and transmit timestamps in UTC (databases, JSON APIs, logs),
// use RFC 3339 on the wire, and convert to a user's zone only at the edge when
// displaying. Keep a clock abstraction (func() time.Time) so code with expiry,
// retries or TTLs is testable.
package main

import (
	"fmt"
	"time"
)

func main() {
	fmt.Println("--- Now, and a fixed date for stable output ---")
	now := time.Now()
	fmt.Println("Now has a year >= 2024:", now.Year() >= 2024)

	// time.Date is the way to build a specific instant.
	t := time.Date(2025, time.March, 14, 15, 9, 26, 0, time.UTC)
	fmt.Println("t:", t)
	fmt.Println("Weekday:", t.Weekday(), "YearDay:", t.YearDay(), "Unix:", t.Unix())

	fmt.Println("\n--- Duration ---")
	d := 90*time.Minute + 30*time.Second
	fmt.Println("d:", d, "| hours:", d.Hours(), "| rounded to min:", d.Round(time.Minute))
	parsedD, err := time.ParseDuration("1h15m30.5s")
	fmt.Println("ParseDuration:", parsedD, err)

	fmt.Println("\n--- Arithmetic ---")
	later := t.Add(36 * time.Hour)
	fmt.Println("t + 36h:        ", later.Format(time.DateTime))
	fmt.Println("t + 1 month:    ", t.AddDate(0, 1, 0).Format(time.DateOnly))
	fmt.Println("later - t:      ", later.Sub(t))
	fmt.Println("later.After(t): ", later.After(t))
	fmt.Println("Truncate to day:", t.Truncate(24*time.Hour).Format(time.RFC3339))

	fmt.Println("\n--- Formatting (reference layout) ---")
	fmt.Println("2006-01-02          :", t.Format("2006-01-02"))
	fmt.Println("02 Jan 2006 15:04   :", t.Format("02 Jan 2006 15:04"))
	fmt.Println("Monday, Jan 2 3:04PM:", t.Format("Monday, Jan 2 3:04PM"))
	fmt.Println("time.RFC3339        :", t.Format(time.RFC3339))
	fmt.Println("time.RFC3339Nano    :", t.Add(123*time.Millisecond).Format(time.RFC3339Nano))

	fmt.Println("\n--- Parsing ---")
	p, err := time.Parse(time.RFC3339, "2025-12-25T08:30:00+05:30")
	fmt.Println("Parsed:", p, err)
	fmt.Println("In UTC:", p.UTC())
	_, err = time.Parse("2006-01-02", "2025-13-45")
	fmt.Println("Bad date error:", err)

	fmt.Println("\n--- Time zones ---")
	ny, err := time.LoadLocation("America/New_York") // needs tzdata on the host
	if err != nil {
		// In minimal containers, import _ "time/tzdata" to embed zone data.
		fmt.Println("LoadLocation failed:", err)
	} else {
		fmt.Println("New York:", t.In(ny).Format(time.RFC1123Z))
		// Same instant, different zone: Equal is true, == would not be.
		fmt.Println("Equal to UTC value:", t.Equal(t.In(ny)))
	}
	fmt.Println("Store/send as UTC:", time.Now().UTC().Location())

	fmt.Println("\n--- Measuring elapsed time (monotonic clock) ---")
	start := time.Now()
	time.Sleep(20 * time.Millisecond)
	elapsed := time.Since(start) // monotonic: safe against wall-clock changes
	fmt.Println("Elapsed >= 20ms:", elapsed >= 20*time.Millisecond)

	fmt.Println("\n--- Timer and Ticker ---")
	timer := time.NewTimer(30 * time.Millisecond)
	<-timer.C // blocks until the timer fires once
	fmt.Println("timer fired")

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop() // a forgotten ticker keeps firing; always Stop it
	for i := range 3 {
		<-ticker.C
		fmt.Println("tick", i+1)
	}

	select {
	case <-time.After(20 * time.Millisecond): // one-shot timeout channel
		fmt.Println("timed out waiting (expected)")
	case <-make(chan struct{}):
	}
}
