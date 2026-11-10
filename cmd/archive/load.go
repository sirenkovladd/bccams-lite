package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// camera mirrors one data/cams.json record. The Go binary carries no table;
// data/cams.json is the single source of truth, loaded at startup via CAMS_JSON.
type camera struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Hood   string            `json:"neighbourhood"`
	Page   string            `json:"page"`
	Lat    float64           `json:"lat"`
	Lng    float64           `json:"lng"`
	Images map[string]string `json:"images"`
}

func loadCams(path string) ([]camera, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cams []camera
	if err := json.Unmarshal(raw, &cams); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(cams) == 0 {
		return nil, fmt.Errorf("no cameras in %s", path)
	}
	for i, c := range cams {
		if c.ID == "" || c.Page == "" || len(c.Images) == 0 {
			return nil, fmt.Errorf("cam %d in %s missing id/page/images", i, path)
		}
		if c.Lat == 0 && c.Lng == 0 {
			return nil, fmt.Errorf("cam %s in %s missing coordinates", c.ID, path)
		}
	}
	return cams, nil
}
