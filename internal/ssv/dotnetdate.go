package ssv

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Time wraps time.Time to decode SSV's .NET WCF date format,
// e.g. "/Date(1778013229136)/" or "/Date(1778013229136+0200)/".
//
// SSV uses very large millisecond values as a "never expires" sentinel
// (year 9999); we split into seconds + nanoseconds during decoding so
// the multiplication never overflows int64.
type Time struct {
	time.Time
}

var dotNetDateRe = regexp.MustCompile(`^/Date\((-?\d+)([+-]\d{4})?\)/$`)

func (t *Time) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	// Let json decode the surrounding quotes AND the JSON escapes;
	// SSV serialises the date as "\/Date(...)\/" — the slashes are
	// escaped per JSON's allowance and must be unescaped first.
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("ssv: decode .NET date: %w", err)
	}
	if s == "" {
		return nil
	}
	m := dotNetDateRe.FindStringSubmatch(s)
	if m == nil {
		return fmt.Errorf("ssv: invalid .NET date %q", s)
	}
	ms, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return fmt.Errorf("ssv: parse epoch %q: %w", m[1], err)
	}
	sec := ms / 1000
	nsec := (ms % 1000) * int64(time.Millisecond)
	t.Time = time.Unix(sec, nsec).UTC()
	return nil
}
