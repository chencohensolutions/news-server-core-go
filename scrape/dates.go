package scrape

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// parseDate is Dates.kt: ok false instead of an error, since an unparseable date must never drop an article.
func parseDate(raw string, spec DateSpec) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	zone, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return time.Time{}, false
	}
	switch spec.Format {
	case FormatISO:
		if t, ok := parseInstant(value); ok {
			return t, true
		}
		if l, ok := parseLocalDateTime(value); ok {
			return l.in(zone)
		}
		return time.Time{}, false
	case FormatEpochSeconds:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(n, 0).UTC(), true
	case FormatEpochMillis:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.UnixMilli(n).UTC(), true
	default:
		l, ok := parsePattern(value, spec.Format)
		if !ok {
			return time.Time{}, false
		}
		return l.in(zone)
	}
}

type localDateTime struct{ year, month, day, hour, minute, second, nano int }

func (l localDateTime) valid() bool {
	if l.month < 1 || l.month > 12 || l.day < 1 || l.hour > 23 || l.minute > 59 || l.second > 59 {
		return false
	}
	return l.day <= time.Date(l.year, time.Month(l.month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// in is kotlinx's LocalDateTime.toInstant(zone). A time skipped by a clock change moves forward by the gap, as
// java.time does.
func (l localDateTime) in(zone *time.Location) (time.Time, bool) {
	if !l.valid() {
		return time.Time{}, false
	}
	return time.Date(l.year, time.Month(l.month), l.day, l.hour, l.minute, l.second, l.nano, zone).UTC(), true
}

var (
	isoDateTime = regexp.MustCompile(`^([+-]?\d{4,})-(\d{2})-(\d{2})[Tt](\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,9}))?)?`)
	isoOffset   = regexp.MustCompile(`^(?:[Zz]|([+-])(\d{2}):(\d{2})(?::(\d{2}))?)$`)
)

func parseLocal(value string) (localDateTime, string, bool) {
	m := isoDateTime.FindStringSubmatch(value)
	if m == nil {
		return localDateTime{}, "", false
	}
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	l := localDateTime{year: n(m[1]), month: n(m[2]), day: n(m[3]), hour: n(m[4]), minute: n(m[5]), second: n(m[6])}
	if m[7] != "" {
		frac := m[7] + strings.Repeat("0", 9-len(m[7]))
		l.nano = n(frac)
	}
	return l, value[len(m[0]):], l.valid()
}

// parseInstant is kotlin.time.Instant.parse: an ISO-8601 date-time with Z or a ±hh:mm offset.
func parseInstant(value string) (time.Time, bool) {
	l, rest, ok := parseLocal(value)
	if !ok {
		return time.Time{}, false
	}
	m := isoOffset.FindStringSubmatch(rest)
	if m == nil {
		return time.Time{}, false
	}
	offset := 0
	if m[1] != "" {
		h, _ := strconv.Atoi(m[2])
		mi, _ := strconv.Atoi(m[3])
		s, _ := strconv.Atoi(m[4])
		if h > 18 || mi > 59 || s > 59 {
			return time.Time{}, false
		}
		offset = h*3600 + mi*60 + s
		if m[1] == "-" {
			offset = -offset
		}
	}
	return l.in(time.FixedZone("", offset))
}

// parseLocalDateTime is kotlinx's LocalDateTime.parse: an ISO-8601 date-time without an offset.
func parseLocalDateTime(value string) (localDateTime, bool) {
	l, rest, ok := parseLocal(value)
	return l, ok && rest == ""
}

// parsePattern reads the tokens yyyy, yy, M/MM, d/dd, H/HH, m/mm, s/ss; everything else matches literally, anywhere
// in the value.
func parsePattern(value, pattern string) (localDateTime, bool) {
	var tokens []byte
	var re strings.Builder
	for i := 0; i < len(pattern); {
		c := pattern[i]
		run := 1
		for i+run < len(pattern) && pattern[i+run] == c {
			run++
		}
		switch {
		case c == 'y' && run == 4:
			tokens = append(tokens, 'Y')
			re.WriteString(`(\d{4})`)
		case c == 'y' && run == 2:
			tokens = append(tokens, 'y')
			re.WriteString(`(\d{2})`)
		case strings.IndexByte("MdHms", c) >= 0 && run <= 2:
			tokens = append(tokens, c)
			re.WriteString(`(\d{1,2})`)
		default:
			re.WriteString(regexp.QuoteMeta(pattern[i : i+run]))
		}
		i += run
	}
	compiled, err := regexp.Compile(re.String())
	if err != nil {
		return localDateTime{}, false
	}
	m := compiled.FindStringSubmatch(value)
	if m == nil {
		return localDateTime{}, false
	}
	l := localDateTime{year: 1970, month: 1, day: 1}
	for i, t := range tokens {
		n, _ := strconv.Atoi(m[i+1])
		switch t {
		case 'Y':
			l.year = n
		case 'y':
			l.year = 2000 + n
		case 'M':
			l.month = n
		case 'd':
			l.day = n
		case 'H':
			l.hour = n
		case 'm':
			l.minute = n
		case 's':
			l.second = n
		}
	}
	return l, l.valid()
}

// FormatInstant writes t as Kotlin's Instant.toString: UTC, with the fraction in groups of three digits when there
// is one ("2026-10-26T07:15:00Z", "2026-10-26T07:15:00.120Z").
func FormatInstant(t time.Time) string {
	t = t.UTC()
	s := t.Format("2006-01-02T15:04:05")
	if ns := t.Nanosecond(); ns != 0 {
		frac := fmt.Sprintf("%09d", ns)
		switch {
		case ns%1_000_000 == 0:
			frac = frac[:3]
		case ns%1_000 == 0:
			frac = frac[:6]
		}
		s += "." + frac
	}
	return s + "Z"
}
