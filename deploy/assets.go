// Package deploy bundles deployment assets into the ModelGauge executable.
package deploy

import "embed"

//go:embed k3s/*.yaml k3s/*.sh monitoring/*.yaml monitoring/*.sh monitoring/dashboards/*.json
var Assets embed.FS
