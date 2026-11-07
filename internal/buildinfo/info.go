// Package buildinfo is populated consistently for CLI, controller and agents.
package buildinfo

import "os"

var Version = "dev"
var Commit = "unknown"
var Date = "unknown"
var Repository = "nerdylucifer/modelgauge"
var Channel = "development"

type Info struct {
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	Date         string `json:"date"`
	Repository   string `json:"repository"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Channel      string `json:"channel"`
}

func Released() bool {
	return Channel == "release" || (Channel == "snapshot" && os.Getenv("MODELGAUGE_RELEASE_BASE_URL") != "")
}
