package main

// The model for conversation. Each Mac's agent says which models its Claude Code offers, when it
// links and every hour (agent/models.go). The hub remembers each Mac's list, so the app can show
// it while the Mac sleeps, and keeps one choice for all of them, as it keeps the hold: "default",
// or a name Claude Code takes such as "sonnet", which follows new versions by itself. The choice
// goes to every agent at once, and to each as it links; a Mac that doesn't offer it uses its own
// default.

import (
	"path/filepath"
	"sort"
	"time"
)

var modelsPath = filepath.Join(stateDir, "models.json")

// macModels is what a Mac's Claude Code offered when it last said.
type macModels struct {
	Models []map[string]any `json:"models"`
	At     string           `json:"at"`
}

type modelsState struct {
	Choice string               `json:"choice"`
	Macs   map[string]macModels `json:"macs"`
}

// modelAsked is when the agents were last asked for their models, so opening the app's Settings
// over and over doesn't start Claude Code over and over. The caller holds stateMu.
var modelAsked time.Time

func loadModels() modelsState {
	s := modelsState{Choice: "default", Macs: map[string]macModels{}}
	readJSON(modelsPath, &s)
	if s.Macs == nil {
		s.Macs = map[string]macModels{}
	}
	if s.Choice == "" {
		s.Choice = "default"
	}
	return s
}

// keepModels keeps what a Mac offers, and answers with the choice. The caller holds stateMu.
func keepModels(mac string, given []any) string {
	s := loadModels()
	list := []map[string]any{}
	for _, item := range given {
		if m, ok := item.(map[string]any); ok && str(m, "value") != "" {
			list = append(list, m)
		}
	}
	s.Macs[mac] = macModels{Models: list, At: isoNow()}
	if err := writeJSON(modelsPath, s); err != nil {
		log("can't keep the models: " + err.Error())
	}
	return s.Choice
}

// modelSetting is the app's Settings. {"token", "key"} reads it; with "model" it sets the choice;
// with "refresh" the agents are asked again, and what they say comes with the next read.
func modelSetting(body map[string]any) (map[string]any, error) {
	if _, _, err := pairedPhone(body); err != nil {
		return nil, err
	}
	s := loadModels()
	if value := str(body, "model"); value != "" && value != s.Choice {
		s.Choice = value
		if err := writeJSON(modelsPath, s); err != nil {
			return nil, err
		}
		log("the model for conversation: " + value + ", set on the phone")
		broadcast(map[string]any{"type": "model", "value": value})
	}
	if body["refresh"] == true && now().Sub(modelAsked) > 20*time.Second {
		modelAsked = now()
		broadcast(map[string]any{"type": "models_refresh"})
	}
	return s.answer(), nil
}

// answer is what the app shows: every model any Mac offers, once, in the order of the Mac that
// said last; for each the Macs that don't offer it; and the Macs, with what each uses by default.
func (s modelsState) answer() map[string]any {
	_, linkedMacs := linked()
	on := map[string]bool{}
	for _, m := range linkedMacs {
		on[str(m, "name")] = true
	}
	names := make([]string, 0, len(s.Macs))
	for name := range s.Macs {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := s.Macs[names[i]].At, s.Macs[names[j]].At
		if a != b {
			return a > b
		}
		return names[i] < names[j]
	})
	offers := func(mac, value string) bool {
		for _, m := range s.Macs[mac].Models {
			if str(m, "value") == value {
				return true
			}
		}
		return false
	}
	rows, seen := []map[string]any{}, map[string]bool{}
	macs := []map[string]any{}
	for _, name := range names {
		mac := map[string]any{"name": name, "linked": on[name], "at": s.Macs[name].At, "default": ""}
		for _, m := range s.Macs[name].Models {
			value := str(m, "value")
			if m["default"] == true {
				mac["default"] = str(m, "name")
			}
			if seen[value] {
				continue
			}
			seen[value] = true
			missing := []string{}
			for _, other := range names {
				if !offers(other, value) {
					missing = append(missing, other)
				}
			}
			row := map[string]any{"value": value, "name": str(m, "name"), "about": str(m, "about"), "default": m["default"] == true, "missing": missing}
			rows = append(rows, row)
		}
		macs = append(macs, mac)
	}
	return map[string]any{"choice": s.Choice, "models": rows, "macs": macs}
}

// modelLine says which model calls use, for the portal: "Default (Opus 5.5)" or "Sonnet 5.5".
func modelLine() string {
	s := loadModels()
	answer := s.answer()
	rows, _ := answer["models"].([]map[string]any)
	macs, _ := answer["macs"].([]map[string]any)
	if s.Choice == "default" {
		if len(macs) == 0 {
			return "Default"
		}
		first := str(macs[0], "default")
		for _, m := range macs {
			if str(m, "default") != first {
				return "Default"
			}
		}
		if first == "" {
			return "Default"
		}
		return "Default (" + first + ")"
	}
	for _, r := range rows {
		if str(r, "value") == s.Choice {
			return str(r, "name")
		}
	}
	return s.Choice + " (no Mac offers it, so calls use the default)"
}
