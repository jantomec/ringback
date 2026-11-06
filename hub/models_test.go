package main

import (
	"path/filepath"
	"testing"
)

func usingModels(t *testing.T) {
	t.Helper()
	before := modelsPath
	modelsPath = filepath.Join(t.TempDir(), "models.json")
	t.Cleanup(func() { modelsPath = before })
}

func offered(names ...string) []any {
	list := []any{map[string]any{"value": "default", "name": "Opus 5.5", "about": "Opus 5.5 · Best", "default": true}}
	for _, name := range names {
		list = append(list, map[string]any{"value": name, "name": name + " 5.5", "about": "For " + name})
	}
	return list
}

func TestModelsOfOneMac(t *testing.T) {
	usingModels(t)
	if got := modelLine(); got != "Default" {
		t.Errorf("no Mac yet: %q", got)
	}
	if choice := keepModels("Desk", offered("opus", "sonnet")); choice != "default" {
		t.Errorf("the choice before any is set: %q", choice)
	}
	if got := modelLine(); got != "Default (Opus 5.5)" {
		t.Errorf("the default names its model: %q", got)
	}
	answer := loadModels().answer()
	rows := answer["models"].([]map[string]any)
	if len(rows) != 3 || rows[0]["value"] != "default" || rows[0]["default"] != true || rows[2]["name"] != "sonnet 5.5" {
		t.Errorf("rows: %v", rows)
	}
	macs := answer["macs"].([]map[string]any)
	if len(macs) != 1 || macs[0]["linked"] != false || macs[0]["default"] != "Opus 5.5" {
		t.Errorf("macs: %v", macs)
	}
}

func TestModelsOfTwoMacs(t *testing.T) {
	usingModels(t)
	keepModels("Laptop", offered("opus"))
	keepModels("Desk", offered("opus", "sonnet"))
	rows := loadModels().answer()["models"].([]map[string]any)
	var sonnet map[string]any
	for _, r := range rows {
		if r["value"] == "sonnet" {
			sonnet = r
		}
	}
	if len(rows) != 3 {
		t.Errorf("every model once: %v", rows)
	}
	if missing, _ := sonnet["missing"].([]string); len(missing) != 1 || missing[0] != "Laptop" {
		t.Errorf("sonnet isn't on the laptop: %v", sonnet)
	}
}

func TestChoiceKeptAndGone(t *testing.T) {
	usingModels(t)
	keepModels("Desk", offered("sonnet"))
	s := loadModels()
	s.Choice = "sonnet"
	if err := writeJSON(modelsPath, s); err != nil {
		t.Fatal(err)
	}
	if got := modelLine(); got != "sonnet 5.5" {
		t.Errorf("the choice: %q", got)
	}
	// The Mac stops offering it: the choice stays, in case it comes back.
	if choice := keepModels("Desk", offered("opus")); choice != "sonnet" {
		t.Errorf("a Mac's list doesn't change the choice: %q", choice)
	}
	if got := modelLine(); got != "sonnet (no Mac offers it, so calls use the default)" {
		t.Errorf("a choice that's gone: %q", got)
	}
}
