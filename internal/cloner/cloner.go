package cloner

import (
	"fmt"
	"strconv"
	"strings"
)

// Creds are the shared Windows credentials applied to every new source.
type Creds struct {
	Username string
	Password string
	Domain   string // optional
}

// Result summarizes what the clone did.
type Result struct {
	APIVersion    string
	TemplateName  string
	Added         []string // new source names
	UnknownParams []string // CSV headers that didn't match any parameter on the template
}

// Clone mutates cfg in place: it finds the template windows_event source
// identified by templateRef (name or numeric index), then appends one new
// source per CSV row. It preserves inline processors and v2 routes.
//
// cfg is the decoded Configuration as a raw map (from JSON or YAML) so that
// unknown fields round-trip intact.
func Clone(cfg map[string]any, templateRef string, rows []Row, creds Creds) (*Result, error) {
	apiVersion, _ := cfg["apiVersion"].(string)

	spec, ok := cfg["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("configuration has no spec")
	}
	sourcesAny, ok := spec["sources"]
	if !ok {
		return nil, fmt.Errorf("configuration spec has no sources")
	}
	sources, ok := sourcesAny.([]any)
	if !ok {
		return nil, fmt.Errorf("spec.sources is not a list")
	}

	tmplIdx, err := findTemplate(sources, templateRef)
	if err != nil {
		return nil, err
	}
	tmpl, _ := sources[tmplIdx].(map[string]any)
	tmplType, _ := tmpl["type"].(string)
	// Source type may carry a version suffix like "windowsevents_v2:6".
	baseType := tmplType
	if i := strings.Index(baseType, ":"); i >= 0 {
		baseType = baseType[:i]
	}
	if baseType != "windows_event" && baseType != "windowsevents_v2" && baseType != "windowsevents" {
		return nil, fmt.Errorf("template source type is %q, expected a Windows Event source (windows_event / windowsevents_v2)", tmplType)
	}
	tmplName, _ := tmpl["name"].(string)

	// Check which CSV parameter headers exist on the template so we can
	// warn about typos.
	tmplParamNames := parameterNames(tmpl)

	res := &Result{
		APIVersion:   apiVersion,
		TemplateName: tmplName,
	}

	seenUnknown := map[string]bool{}
	for _, row := range rows {
		clone := deepCopyMap(tmpl)
		// Clear server-generated identity fields so Bindplane assigns new ones.
		// `name` in an inline ResourceConfiguration is Bindplane's opaque ID
		// (e.g. "s-01K..."); `displayName` is the UI label we want to set.
		delete(clone, "id")
		delete(clone, "name")
		clone["displayName"] = row.Name

		setParam(clone, "use_remote", true)
		setParam(clone, "remote_server", row.Hostname)
		setParam(clone, "remote_username", creds.Username)
		setParam(clone, "remote_password", creds.Password)
		if creds.Domain != "" {
			setParam(clone, "remote_domain", creds.Domain)
		}

		for k, v := range row.Extras {
			if !tmplParamNames[k] && !seenUnknown[k] {
				res.UnknownParams = append(res.UnknownParams, k)
				seenUnknown[k] = true
			}
			setParam(clone, k, coerce(v))
		}

		sources = append(sources, clone)
		res.Added = append(res.Added, row.Name)
	}

	spec["sources"] = sources
	return res, nil
}

// findTemplate accepts a numeric index or an exact match on the source's
// displayName or name. displayName is preferred because Bindplane names
// sources with opaque IDs like "s-01KFARY..." while displayName holds the
// human-readable label shown in the UI.
func findTemplate(sources []any, ref string) (int, error) {
	if n, err := strconv.Atoi(ref); err == nil {
		if n < 0 || n >= len(sources) {
			return -1, fmt.Errorf("template index %d out of range (0..%d)", n, len(sources)-1)
		}
		return n, nil
	}
	var labels []string
	for i, s := range sources {
		m, _ := s.(map[string]any)
		name, _ := m["name"].(string)
		display, _ := m["displayName"].(string)
		label := display
		if label == "" {
			label = name
		} else {
			label = fmt.Sprintf("%s (name=%s)", display, name)
		}
		labels = append(labels, label)
		if display == ref || name == ref {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no source with displayName or name %q; found: %s", ref, strings.Join(labels, ", "))
}

func parameterNames(src map[string]any) map[string]bool {
	out := map[string]bool{}
	params, _ := src["parameters"].([]any)
	for _, p := range params {
		pm, _ := p.(map[string]any)
		if n, ok := pm["name"].(string); ok {
			out[n] = true
		}
	}
	return out
}

// setParam updates or inserts parameters[{name, value}].
func setParam(src map[string]any, name string, value any) {
	params, _ := src["parameters"].([]any)
	for i, p := range params {
		pm, _ := p.(map[string]any)
		if n, _ := pm["name"].(string); n == name {
			pm["value"] = value
			params[i] = pm
			src["parameters"] = params
			return
		}
	}
	params = append(params, map[string]any{"name": name, "value": value})
	src["parameters"] = params
}

// coerce converts CSV string values to typed values for common cases so the
// Bindplane UI validators accept them (true/false, integers).
func coerce(s string) any {
	switch strings.ToLower(s) {
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return s
}

func deepCopyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = deepCopyAny(v)
	}
	return out
}

func deepCopyAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopyMap(t)
	case []any:
		cp := make([]any, len(t))
		for i, e := range t {
			cp[i] = deepCopyAny(e)
		}
		return cp
	default:
		return v
	}
}
