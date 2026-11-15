package deploy

import (
	"encoding/json"
	"go.yaml.in/yaml/v3"
	"io"
	"io/fs"
	"strings"
	"testing"
)

func TestDashboardsExplainEveryPanelAndLiveUnits(t *testing.T) {
	files, err := fs.Glob(Assets, "monitoring/dashboards/*.json")
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]bool{}
	for _, file := range files {
		data, _ := Assets.ReadFile(file)
		var d struct {
			UID, Refresh string
			Panels       []struct {
				Title, Description, Type string
				Targets                  []struct{ Expr string }
				FieldConfig              struct {
					Defaults struct{ Unit, NoValue string }
				}
			}
		}
		if err := json.Unmarshal(data, &d); err != nil {
			t.Fatal(file, err)
		}
		for _, p := range d.Panels {
			if p.Title == "" || strings.TrimSpace(p.Description) == "" {
				t.Fatal(file, "unexplained panel", p.Title)
			}
			if strings.HasPrefix(d.UID, "modelgauge-") && (d.UID == "modelgauge-live" || d.UID == "modelgauge-serving" || d.UID == "modelgauge-resources") {
				live[d.UID] = true
				if d.Refresh != "5s" {
					t.Fatal(file, "slow refresh")
				}
				if len(p.Targets) > 0 && (p.FieldConfig.Defaults.Unit == "" || p.FieldConfig.Defaults.NoValue == "") {
					t.Fatal(file, p.Title, "missing units/unavailable indication")
				}
				if p.Type == "timeseries" && !strings.Contains(p.Targets[0].Expr, "modelgauge_live_started_seconds @ end()") {
					t.Fatal(file, p.Title, "previous experiments can leak into current view")
				}
			}
		}
	}
	if len(live) != 3 {
		t.Fatal("missing live dashboard", live)
	}
}
func TestAnonymousMonitoringIsViewerOnlyAndServicesStayPrivate(t *testing.T) {
	data, _ := Assets.ReadFile("monitoring/stack.yaml")
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	found := false
	for {
		var doc struct {
			Kind     string
			Metadata struct{ Name string }
			Spec     struct {
				Type     string
				Template struct {
					Spec struct {
						Containers []struct {
							Name string
							Env  []struct{ Name, Value string }
						}
					}
				}
			}
		}
		err := decoder.Decode(&doc)
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if doc.Kind == "Service" && doc.Spec.Type != "" && doc.Spec.Type != "ClusterIP" {
			t.Fatal("monitoring publicly exposed", doc.Metadata.Name)
		}
		if doc.Kind == "Deployment" && doc.Metadata.Name == "grafana" {
			env := map[string]string{}
			for _, v := range doc.Spec.Template.Spec.Containers[0].Env {
				env[v.Name] = v.Value
			}
			if env["GF_AUTH_ANONYMOUS_ENABLED"] != "true" || env["GF_AUTH_ANONYMOUS_ORG_ROLE"] != "Viewer" || env["GF_DASHBOARDS_DEFAULT_HOME_DASHBOARD_PATH"] != "/var/lib/grafana/dashboards/live.json" {
				t.Fatal(env)
			}
			found = true
		}
	}
	if !found || !strings.Contains(string(data), "scrape_interval: 5s") {
		t.Fatal("missing anonymous dashboard or live scraping")
	}
}
