package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// stateMu serialises everything that reads and writes the state files: requests, and the
// pushes that run after their answer. The Swift switchboard handled requests one at a time;
// this keeps that.
var stateMu sync.Mutex

func readJSON(path string, into any) {
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, into)
	}
}

// writeJSON writes atomically, so a restart finds either the old file or the new one.
func writeJSON(path string, value any) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Devices are the phones that registered for calls, keyed by VoIP push token:
// devices.json in the state folder. A phone that also gave a notification token gets
// connection checks, and "checked_in" is when it last reached the hub on one. A phone with
// work hours has them here too (workHoursKeys), and one whose hold switch you set has it
// ("hold", "yes" if on hold) and when ("hold_set").
var devicesPath = filepath.Join(stateDir, "devices.json")

func loadDevices() map[string]map[string]string {
	devices := map[string]map[string]string{}
	readJSON(devicesPath, &devices)
	return devices
}

func saveDevices(devices map[string]map[string]string) error { return writeJSON(devicesPath, devices) }

// registerDevice keeps a phone's tokens, whether its On-device voice is on ("yes" or "no",
// missing if it didn't say) and its work hours; empty hours clear them.
func registerDevice(token, environment, device, notificationToken, onDevice string, hours map[string]string) error {
	devices := loadDevices()
	entry := devices[token]
	if entry == nil {
		entry = map[string]string{}
	}
	entry["environment"] = environment
	entry["device"] = device
	entry["registered"] = isoNow()
	if notificationToken != "" {
		entry["notification_token"] = notificationToken
	}
	if onDevice != "" {
		entry["on_device"] = onDevice
	} else {
		delete(entry, "on_device")
	}
	setHours(entry, hours)
	devices[token] = entry
	return saveDevices(devices)
}

// phoneSpeaks says whether a registered phone speaks itself, so a call can be talked through
// without a speech node.
func phoneSpeaks() bool {
	for _, device := range loadDevices() {
		if device["on_device"] == "yes" {
			return true
		}
	}
	return false
}

func setHours(entry, hours map[string]string) {
	for _, key := range workHoursKeys {
		if value, ok := hours[key]; ok {
			entry[key] = value
		} else {
			delete(entry, key)
		}
	}
}

// checkedIn notes a connection check, which can bring the phone's work hours too, so a new
// time zone reaches the hub when you travel; nil hours leave them as they were.
func checkedIn(token string, hours map[string]string) error {
	devices := loadDevices()
	entry, ok := devices[token]
	if !ok {
		return nil
	}
	entry["checked_in"] = isoNow()
	if hours != nil {
		setHours(entry, hours)
	}
	return saveDevices(devices)
}

// setHold is you setting the phone's hold switch in the app, as of now, until its work hours
// next set it (Hold). nil if the phone isn't registered.
func setHold(token string, on bool) (map[string]string, error) {
	devices := loadDevices()
	entry, ok := devices[token]
	if !ok {
		return nil, nil
	}
	if on {
		entry["hold"] = "yes"
	} else {
		delete(entry, "hold")
	}
	entry["hold_set"] = isoNow()
	return entry, saveDevices(devices)
}

func forgetDevice(token string) error {
	devices := loadDevices()
	if _, ok := devices[token]; !ok {
		return nil
	}
	delete(devices, token)
	return saveDevices(devices)
}

// The calls that wait: held while every phone is on hold, until they ring. Kept in
// waiting.json in the state folder, keyed by call ID, so they survive a restart, with their
// project, subject, voice server, when they ring ("held_until", missing until you take
// calls) and when their voice server last asked about them ("refreshed"); "question" ("yes"
// or missing) as the voice server last said, and when the phone was told of its question
// ("notified"). The app lists them (/waiting), and can call one now, or send its question to
// the Claude app. A voice server asks again about each held call every 10 min, which
// refreshes it; one not refreshed for 25 min is dropped, so a call from a Mac that's gone
// doesn't wait forever.
var waitingPath = filepath.Join(stateDir, "waiting.json")

// waitingStale is how long after its last refresh a call is dropped; RINGBACK_WAITING_STALE
// sets fewer seconds, for the smoke test.
var waitingStale = envSeconds("RINGBACK_WAITING_STALE", 25*60)

func envSeconds(name string, fallback float64) time.Duration {
	if value, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil {
		return time.Duration(value * float64(time.Second))
	}
	return time.Duration(fallback * float64(time.Second))
}

func loadWaiting() map[string]map[string]string {
	calls := map[string]map[string]string{}
	readJSON(waitingPath, &calls)
	return calls
}

// freshWaiting are the calls refreshed within waitingStale; the log says which are dropped.
func freshWaiting() map[string]map[string]string {
	calls := loadWaiting()
	for id, call := range calls {
		refreshed, ok := parseISO8601(call["refreshed"])
		if ok && now().Sub(refreshed) <= waitingStale {
			continue
		}
		if !ok {
			refreshed = time.Time{}
		}
		log("call " + prefix(id, 8) + ": no longer waiting, its voice server hasn't asked about it since " + timeOfDay(refreshed.Local()))
		delete(calls, id)
	}
	return calls
}

// holdCall keeps a call held until heldUntil (empty: until you take calls), new or asked
// about again, with whether it has a question, as its voice server last said, and why it
// waits when it isn't your hours or the hold switch (reason: "no speech"). True the first time
// it has a question or a reason, when the phone is to be told: "notified" keeps when, so a
// restart or asking again doesn't tell it twice.
func holdCall(callID, project, subject, voice, heldUntil string, question bool, session, reason string) (bool, error) {
	calls := freshWaiting()
	notified := calls[callID]["notified"]
	call := map[string]string{"project": project, "voice": voice, "refreshed": isoNow()}
	for key, value := range map[string]string{"held_until": heldUntil, "subject": subject, "session_id": session, "reason": reason} {
		if value != "" {
			call[key] = value
		}
	}
	if question {
		call["question"] = "yes"
	}
	notify := (question || reason != "") && notified == ""
	if notify {
		call["notified"] = isoNow()
	} else if notified != "" {
		call["notified"] = notified
	}
	calls[callID] = call
	return notify, writeJSON(waitingPath, calls)
}

// waitingFor are the IDs of the calls that wait for a reason ("no speech").
func waitingFor(reason string) []any {
	ids := []any{}
	for id, call := range loadWaiting() {
		if call["reason"] == reason {
			ids = append(ids, id)
		}
	}
	return ids
}

// removeWaiting takes a call off the list, as it rings, the app calls it now or sends its
// question to the Claude app; false if it wasn't on it.
func removeWaiting(callID string) (bool, error) {
	calls := loadWaiting()
	if _, ok := calls[callID]; !ok {
		return false, nil
	}
	delete(calls, callID)
	return true, writeJSON(waitingPath, calls)
}

// listWaiting are the calls that wait, soonest first, those held until you take calls last,
// each with its call ID, and whether it has a question; stale ones are dropped.
func listWaiting() ([]map[string]any, error) {
	calls := freshWaiting()
	if len(calls) != len(loadWaiting()) {
		if err := writeJSON(waitingPath, calls); err != nil {
			return nil, err
		}
	}
	ids := make([]string, 0, len(calls))
	for id := range calls {
		ids = append(ids, id)
	}
	when := func(id string) time.Time {
		if t, ok := parseISO8601(calls[id]["held_until"]); ok {
			return t
		}
		return time.Unix(1<<40, 0)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := when(ids[i]), when(ids[j])
		if !a.Equal(b) {
			return a.Before(b)
		}
		return ids[i] < ids[j]
	})
	listed := []map[string]any{}
	for _, id := range ids {
		item := map[string]any{"call_id": id, "question": calls[id]["question"] != ""}
		for _, key := range []string{"project", "subject", "voice", "held_until", "session_id", "reason"} {
			if value, ok := calls[id][key]; ok {
				item[key] = value
			}
		}
		listed = append(listed, item)
	}
	return listed, nil
}

// The sessions that listen on each Mac, for the app's Sessions tab: contacts.json in the
// state folder, keyed by voice server, with when it last said ("refreshed") and its sessions,
// each with its session ID, project, and when it can, since when it listens and its latest
// briefing's subject and time. A voice server says every 30 s; one not heard from for 90 s is
// dropped, as when its Mac sleeps or the voice server stops.
var contactsPath = filepath.Join(stateDir, "contacts.json")

// contactsStale is how long after a voice server last said that its sessions are dropped;
// RINGBACK_CONTACTS_STALE sets fewer seconds, for the smoke test.
var contactsStale = envSeconds("RINGBACK_CONTACTS_STALE", 90)

type contactsEntry struct {
	Refreshed string              `json:"refreshed"`
	Sessions  []map[string]string `json:"sessions"`
	// Voice is the URL the phone calls these sessions at, when it isn't the key: an agent's
	// sessions are kept under the agent, and called at the hub.
	Voice string `json:"voice,omitempty"`
}

func loadContacts() map[string]contactsEntry {
	all := map[string]contactsEntry{}
	readJSON(contactsPath, &all)
	return all
}

// freshContacts are the voice servers heard from within contactsStale; the log says which
// are dropped.
func freshContacts() map[string]contactsEntry {
	all := loadContacts()
	for voice, entry := range all {
		refreshed, ok := parseISO8601(entry.Refreshed)
		if ok && now().Sub(refreshed) <= contactsStale {
			continue
		}
		log("sessions listening on " + voice + ": dropped, not heard from since " + timeOfDay(refreshed.Local()))
		delete(all, voice)
	}
	return all
}

// updateContacts keeps a voice server's or an agent's sessions now (key), replacing those it
// gave before, called at voice; true if they changed.
func updateContacts(key, voice string, sessions []map[string]string) (bool, error) {
	all := freshContacts()
	before, had := all[key]
	entry := contactsEntry{Refreshed: isoNow(), Sessions: sessions}
	if voice != key {
		entry.Voice = voice
	}
	all[key] = entry
	if err := writeJSON(contactsPath, all); err != nil {
		return false, err
	}
	if !had {
		return true, nil
	}
	a, _ := json.Marshal(before.Sessions)
	b, _ := json.Marshal(sessions)
	return !bytes.Equal(a, b), nil
}

// listContacts are every fresh voice server's sessions, by project, then since when they
// listen, each with its voice server; stale ones are dropped.
func listContacts() ([]map[string]string, error) {
	all := freshContacts()
	if len(all) != len(loadContacts()) {
		if err := writeJSON(contactsPath, all); err != nil {
			return nil, err
		}
	}
	listed := []map[string]string{}
	for key, entry := range all {
		voice := orDefault(entry.Voice, key)
		for _, session := range entry.Sessions {
			item := map[string]string{"voice": voice}
			for k, v := range session {
				item[k] = v
			}
			listed = append(listed, item)
		}
	}
	key := func(s map[string]string) string {
		return strings.ToLower(s["project"]) + "\x00" + s["since"] + "\x00" + s["session_id"]
	}
	sort.Slice(listed, func(i, j int) bool { return key(listed[i]) < key(listed[j]) })
	return listed, nil
}

// dropContacts forgets an agent's sessions once it disconnects.
func dropContacts(key string) {
	all := loadContacts()
	if _, ok := all[key]; !ok {
		return
	}
	delete(all, key)
	if writeJSON(contactsPath, all) == nil {
		log("sessions listening on " + key + ": dropped, it disconnected")
	}
}

// What the phone reported about each call, one JSON object per line: events.jsonl in the
// state folder. Each gets the hub's local time as "time".
var eventsPath = filepath.Join(stateDir, "events.jsonl")

func appendEvent(event map[string]any) error {
	record := map[string]any{}
	for k, v := range event {
		record[k] = v
	}
	if _, ok := record["time"]; !ok {
		record["time"] = now().Format("2006-01-02T15:04:05")
	}
	var line bytes.Buffer
	encoder := json.NewEncoder(&line)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(line.Bytes())
	return err
}
