package cloner

import (
	"strings"
	"testing"
)

func fixtureV1() map[string]any {
	return map[string]any{
		"apiVersion": "bindplane.observiq.com/v1",
		"kind":       "Configuration",
		"metadata":   map[string]any{"name": "RDC_WINC_TEST", "id": "abc"},
		"spec": map[string]any{
			"sources": []any{
				map[string]any{
					"id":   "src-1",
					"name": "winevt-template",
					"type": "windows_event",
					"parameters": []any{
						map[string]any{"name": "use_remote", "value": false},
						map[string]any{"name": "remote_server", "value": ""},
						map[string]any{"name": "channels", "value": []any{"Security"}},
					},
					"processors": []any{
						map[string]any{"type": "add_fields", "parameters": []any{}},
					},
				},
			},
			"destinations": []any{
				map[string]any{"name": "dest-chronicle", "type": "chronicle"},
			},
		},
	}
}

func fixtureV2() map[string]any {
	c := fixtureV1()
	c["apiVersion"] = "bindplane.observiq.com/v2"
	src := c["spec"].(map[string]any)["sources"].([]any)[0].(map[string]any)
	src["routes"] = map[string]any{
		"logs": []any{map[string]any{"id": "r1", "components": []any{"destinations/dest-chronicle"}}},
	}
	return c
}

func TestCloneAppendsSourcesAndPreservesInlineProcessors(t *testing.T) {
	cfg := fixtureV1()
	rows := []Row{
		{Name: "winevt-host1", Hostname: "host1.example.com", Extras: map[string]string{}},
		{Name: "winevt-host2", Hostname: "host2.example.com", Extras: map[string]string{"channels": "Application"}},
	}
	creds := Creds{Username: "u", Password: "p", Domain: "example.com"}

	res, err := Clone(cfg, "winevt-template", rows, creds)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 2 {
		t.Fatalf("expected 2 added, got %d", len(res.Added))
	}

	sources := cfg["spec"].(map[string]any)["sources"].([]any)
	if len(sources) != 3 {
		t.Fatalf("expected 3 sources total, got %d", len(sources))
	}

	newSrc := sources[1].(map[string]any)
	if newSrc["displayName"] != "winevt-host1" {
		t.Errorf("bad displayName: %v", newSrc["displayName"])
	}
	if _, ok := newSrc["name"]; ok {
		t.Errorf("name should be cleared so Bindplane generates a fresh ID")
	}
	if _, ok := newSrc["id"]; ok {
		t.Errorf("id should be cleared on clone")
	}
	// Inline processors copied.
	procs, _ := newSrc["processors"].([]any)
	if len(procs) != 1 {
		t.Errorf("expected 1 inline processor copied, got %d", len(procs))
	}
	// Parameter overrides.
	params := paramMap(newSrc)
	if params["remote_server"] != "host1.example.com" {
		t.Errorf("remote_server not set: %v", params["remote_server"])
	}
	if params["use_remote"] != true {
		t.Errorf("use_remote should be true, got %v", params["use_remote"])
	}
	if params["remote_username"] != "u" || params["remote_password"] != "p" {
		t.Errorf("creds not set: %v %v", params["remote_username"], params["remote_password"])
	}
	if params["remote_domain"] != "example.com" {
		t.Errorf("domain not set: %v", params["remote_domain"])
	}

	// Template source unchanged.
	tmpl := sources[0].(map[string]any)
	tmplParams := paramMap(tmpl)
	if tmplParams["remote_server"] != "" {
		t.Errorf("template source was mutated: remote_server=%v", tmplParams["remote_server"])
	}
	if tmpl["id"] != "src-1" {
		t.Errorf("template id changed: %v", tmpl["id"])
	}

	// CSV extra override applied on row 2.
	row2 := sources[2].(map[string]any)
	row2Params := paramMap(row2)
	if row2Params["channels"] != "Application" {
		t.Errorf("row2 channel override not applied: %v", row2Params["channels"])
	}
}

func TestCloneV2CopiesRoutes(t *testing.T) {
	cfg := fixtureV2()
	rows := []Row{{Name: "x", Hostname: "h", Extras: map[string]string{}}}
	_, err := Clone(cfg, "winevt-template", rows, Creds{Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	sources := cfg["spec"].(map[string]any)["sources"].([]any)
	newSrc := sources[1].(map[string]any)
	if _, ok := newSrc["routes"]; !ok {
		t.Fatal("routes not copied to cloned source")
	}
}

func TestCloneFindsTemplateByDisplayName(t *testing.T) {
	cfg := fixtureV1()
	src := cfg["spec"].(map[string]any)["sources"].([]any)[0].(map[string]any)
	src["name"] = "s-01KFARY5V0EFW4D8G06BH7ATB9"
	src["displayName"] = "Windows Domain Controllers"

	_, err := Clone(cfg, "Windows Domain Controllers",
		[]Row{{Name: "n", Hostname: "h"}}, Creds{Username: "u", Password: "p"})
	if err != nil {
		t.Fatalf("lookup by displayName failed: %v", err)
	}
}

func TestCloneRejectsNonWindowsEvent(t *testing.T) {
	cfg := fixtureV1()
	src := cfg["spec"].(map[string]any)["sources"].([]any)[0].(map[string]any)
	src["type"] = "file"
	_, err := Clone(cfg, "winevt-template", []Row{{Hostname: "h", Name: "n"}}, Creds{Username: "u", Password: "p"})
	if err == nil || !strings.Contains(err.Error(), "windows_event") {
		t.Fatalf("expected windows_event type error, got %v", err)
	}
}

func TestCloneUnknownParamWarning(t *testing.T) {
	cfg := fixtureV1()
	rows := []Row{{Name: "x", Hostname: "h", Extras: map[string]string{"not_a_real_param": "v"}}}
	res, err := Clone(cfg, "winevt-template", rows, Creds{Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.UnknownParams) != 1 || res.UnknownParams[0] != "not_a_real_param" {
		t.Fatalf("expected unknown param warning, got %v", res.UnknownParams)
	}
}

func TestReadCSV(t *testing.T) {
	csv := "hostname,name,channels\nh1,n1,Security\nh2,,Application\n"
	rows, err := ReadCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].Hostname != "h1" || rows[0].Name != "n1" {
		t.Errorf("row0: %+v", rows[0])
	}
	if rows[1].Name != "winevt-h2" {
		t.Errorf("row1 derived name: %q", rows[1].Name)
	}
	if rows[0].Extras["channels"] != "Security" {
		t.Errorf("row0 extras: %+v", rows[0].Extras)
	}
}

func TestReadCSVRequiresHostname(t *testing.T) {
	_, err := ReadCSV(strings.NewReader("name\nn1\n"))
	if err == nil {
		t.Fatal("expected error for missing hostname column")
	}
}

func paramMap(src map[string]any) map[string]any {
	out := map[string]any{}
	params, _ := src["parameters"].([]any)
	for _, p := range params {
		pm, _ := p.(map[string]any)
		name, _ := pm["name"].(string)
		out[name] = pm["value"]
	}
	return out
}
