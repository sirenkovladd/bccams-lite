package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCams(t *testing.T) {
	dir := t.TempDir()
	good := `[{"id":"a-b","name":"A & B","neighbourhood":"Downtown","page":"https://trafficcams.vancouver.ca/ab.htm","lat":49.1,"lng":-123.1,"images":{"N":"https://x/n.jpg"}},{"id":"c-d","name":"C & D","neighbourhood":"West End","page":"https://trafficcams.vancouver.ca/cd.htm","lat":49.2,"lng":-123.2,"images":{"N":"https://x/n.jpg","S":"https://x/s.jpg"}}]`
	p := filepath.Join(dir, "cams.json")
	if err := os.WriteFile(p, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	cams, err := loadCams(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cams) != 2 || cams[0].ID != "a-b" || cams[1].Images["S"] == "" {
		t.Fatalf("unexpected parse: %+v", cams)
	}
	for _, bad := range []string{`not json`, `[]`, `[{"id":"x"}]`} {
		pb := filepath.Join(dir, "bad.json")
		os.WriteFile(pb, []byte(bad), 0o644)
		if _, err := loadCams(pb); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
	if _, err := loadCams(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("expected error for missing file")
	}
}
