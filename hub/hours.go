package main

import (
	"strconv"
	"strings"
	"time"
)

// workHoursKeys are how the devices file keeps a phone's hours, as the app sends them:
// "HH:MM" and a time zone identifier such as "America/Chicago".
var workHoursKeys = []string{"hours_from", "hours_to", "time_zone"}

// WorkHours are a phone's hours, set in the app: Claude calls only between From and To, in
// the phone's time zone. A From later than To wraps past midnight (22:00–06:00), and equal
// ones mean any time. They set the phone's hold switch at their edges (Hold).
type WorkHours struct {
	From, To int // minutes after midnight
	Zone     *time.Location
}

// newWorkHours reads a phone's hours as the devices file keeps them; false if it has none.
func newWorkHours(fields map[string]string) (WorkHours, bool) {
	from, ok1 := minutes(fields["hours_from"])
	to, ok2 := minutes(fields["hours_to"])
	zone, err := time.LoadLocation(fields["time_zone"])
	if !ok1 || !ok2 || fields["time_zone"] == "" || err != nil {
		return WorkHours{}, false
	}
	return WorkHours{from, to, zone}, true
}

// workHoursFields are the hours in a phone's request, checked, as the devices file keeps
// them: empty when it sent none (both times missing or empty), false when a time isn't
// "HH:MM" or the time zone isn't one this machine knows.
func workHoursFields(body map[string]any) (map[string]string, bool) {
	from, _ := body["hours_from"].(string)
	to, _ := body["hours_to"].(string)
	if from == "" && to == "" {
		return map[string]string{}, true
	}
	zone, _ := body["time_zone"].(string)
	fields := map[string]string{"hours_from": from, "hours_to": to, "time_zone": zone}
	if _, ok := newWorkHours(fields); !ok {
		return nil, false
	}
	return fields, true
}

func (h WorkHours) contain(t time.Time) bool {
	if h.From == h.To {
		return true
	}
	local := t.In(h.Zone)
	m := local.Hour()*60 + local.Minute()
	if h.From < h.To {
		return h.From <= m && m < h.To
	}
	return m >= h.From || m < h.To
}

// at is the time of day m on t's day, in the phone's zone.
func (h WorkHours) at(t time.Time, m int) time.Time {
	l := t.In(h.Zone)
	return time.Date(l.Year(), l.Month(), l.Day(), m/60, m%60, 0, 0, h.Zone)
}

// next is the first time of day m after t.
func (h WorkHours) next(m int, t time.Time) time.Time {
	c := h.at(t, m)
	if !c.After(t) {
		c = h.at(c.AddDate(0, 0, 1), m)
	}
	return c
}

// previous is the last time of day m at or before t.
func (h WorkHours) previous(m int, t time.Time) time.Time {
	c := h.at(t, m)
	if c.After(t) {
		c = h.at(c.AddDate(0, 0, -1), m)
	}
	return c
}

// start is when they next start after t.
func (h WorkHours) start(t time.Time) time.Time { return h.next(h.From, t) }

// end is when they next end after t.
func (h WorkHours) end(t time.Time) time.Time { return h.next(h.To, t) }

// lastEdge is when they last started or ended at or before t, and whether it was a start;
// false if they're any time (From and To equal), which has no edges.
func (h WorkHours) lastEdge(t time.Time) (time.Time, bool, bool) {
	if h.From == h.To {
		return time.Time{}, false, false
	}
	start, end := h.previous(h.From, t), h.previous(h.To, t)
	if start.After(end) {
		return start, true, true
	}
	return end, false, true
}

// written is t in ISO 8601 with the phone's offset from UTC: "2026-10-31T08:00:00-05:00".
func (h WorkHours) written(t time.Time) string { return t.In(h.Zone).Format(time.RFC3339) }

// minutes after midnight of "HH:MM"; false if it isn't one.
func minutes(text string) (int, bool) {
	hh, mm, ok := strings.Cut(text, ":")
	if !ok || len(hh) != 2 || len(mm) != 2 {
		return 0, false
	}
	hour, err1 := strconv.Atoi(hh)
	minute, err2 := strconv.Atoi(mm)
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 || hh[0] == '+' || mm[0] == '+' {
		return 0, false
	}
	return hour*60 + minute, true
}

// Hold is a phone's hold switch (Stage 5 step 2): on hold, its calls wait; taking calls,
// they ring. You set it in the app ("hold", and when, "hold_set", in the devices file), and
// its work hours set it at their edges: on hold when they end, taking calls when they start.
// An edge sets it, it doesn't flip it, so one that finds it already so changes nothing, and
// between edges your setting stands: taking calls from 21:00 lasts until the hours end the
// next day.
type Hold struct {
	On bool
	// Until is when it next changes by itself: when its hours next start (on hold) or end
	// (taking calls); zero without hours, or with From and To equal.
	Until time.Time
}

func newHold(device map[string]string, t time.Time) Hold {
	hours, hasHours := newWorkHours(device)
	set, hasSet := parseISO8601(device["hold_set"])
	on := device["hold"] == "yes"
	if hasHours {
		if edge, start, ok := hours.lastEdge(t); ok && (!hasSet || edge.After(set)) {
			on = !start
		}
	}
	hold := Hold{On: on}
	if hasHours && hours.From != hours.To {
		if on {
			hold.Until = hours.start(t)
		} else {
			hold.Until = hours.end(t)
		}
	}
	return hold
}

// answer is the switch as the app gets it: {"hold": Bool, "until": ISO 8601}, until with the
// phone's offset.
func (h Hold) answer(device map[string]string) map[string]any {
	answer := map[string]any{"hold": h.On}
	if hours, ok := newWorkHours(device); ok && !h.Until.IsZero() {
		answer["until"] = hours.written(h.Until)
	}
	return answer
}

// heldUntil says whether a call asked for at t is held: while every registered phone is on
// hold. until is when the first of them comes off hold by its hours, written with that
// phone's offset; empty when none will by itself, so the call waits until you take calls.
// Not held if a phone takes calls, or none is registered.
func heldUntil(t time.Time) (held bool, until string) {
	devices := loadDevices()
	if len(devices) == 0 {
		return false, ""
	}
	var earliest time.Time
	var earliestHours WorkHours
	for _, device := range devices {
		hold := newHold(device, t)
		if !hold.On {
			return false, ""
		}
		if hours, ok := newWorkHours(device); ok && !hold.Until.IsZero() && (earliest.IsZero() || hold.Until.Before(earliest)) {
			earliest, earliestHours = hold.Until, hours
		}
	}
	if earliest.IsZero() {
		return true, ""
	}
	return true, earliestHours.written(earliest)
}

// describe is a phone's work hours as the log and `devices` show them; empty if it has none.
func describe(hours map[string]string) string {
	from, ok1 := hours["hours_from"]
	to, ok2 := hours["hours_to"]
	if !ok1 || !ok2 {
		return ""
	}
	return "work hours " + from + "–" + to + " " + hours["time_zone"]
}
