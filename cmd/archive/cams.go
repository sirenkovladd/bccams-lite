package main

// Verified camera table. Must mirror data/cams.json (filenames verbatim);
// TestCamTableMatchesCamsJSON fails the suite on drift.
type camera struct {
	ID   string
	Name string
	Hood string
	Page string
	Lat  float64
	Lng  float64
	Dirs map[string]string // N/E/S/W -> image URL
}

const imgBase = "https://trafficcams.vancouver.ca/cameraimages/"

var cameras = []camera{
	{
		ID: "burrard-davie", Name: "Burrard St & Davie St", Hood: "West End",
		Page: "https://trafficcams.vancouver.ca/burrardDavie.htm",
		Lat:  49.2791, Lng: -123.1292,
		Dirs: map[string]string{
			"N": imgBase + "BurrardDavieNorth.jpg",
			"E": imgBase + "BurrardDavieEast.jpg",
			"S": imgBase + "BurrardDavieSouth.jpg",
			"W": imgBase + "BurrardDavieWest.jpg",
		},
	},
	{
		ID: "bute-davie", Name: "Bute St & Davie St", Hood: "West End",
		Page: "https://trafficcams.vancouver.ca/buteDavie.htm",
		Lat:  49.2818, Lng: -123.1334,
		Dirs: map[string]string{
			"N": imgBase + "ButeNorthDavie.jpg",
			"E": imgBase + "DavieEastBute.jpg",
			"S": imgBase + "ButeSouthDavie.jpg",
			"W": imgBase + "DavieWestBute.jpg",
		},
	},
	{
		ID: "burrard-drake", Name: "Burrard St & Drake St", Hood: "West End",
		Page: "https://trafficcams.vancouver.ca/burrardDrake.htm",
		Lat:  49.2809, Lng: -123.1289,
		Dirs: map[string]string{
			"N": imgBase + "BurrardDrakeNorth.jpg",
			"E": imgBase + "BurrardDrakeEast.jpg",
			"S": imgBase + "BurrardDrakeSouth.jpg",
			"W": imgBase + "BurrardDrakeWest.jpg",
		},
	},
	{
		ID: "burrard-pacific", Name: "Burrard St & Pacific Ave", Hood: "West End",
		Page: "https://trafficcams.vancouver.ca/BurrardPacific4.htm",
		Lat:  49.2775, Lng: -123.1285,
		Dirs: map[string]string{
			// NOTE: South still is BurrardBridgeSouth.jpg (asymmetric upstream) — do not "fix".
			"N": imgBase + "BurrardPacificNorth.jpg",
			"E": imgBase + "BurrardPacificEast.jpg",
			"S": imgBase + "BurrardBridgeSouth.jpg",
			"W": imgBase + "BurrardPacificWest.jpg",
		},
	},
}

var directions = []string{"N", "E", "S", "W"}
