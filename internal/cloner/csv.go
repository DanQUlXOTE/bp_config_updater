package cloner

import (
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Row is a decoded CSV row keyed by header name. All values are strings.
type Row struct {
	Name     string            // optional; derived from Hostname if empty
	Hostname string            // required
	Extras   map[string]string // other columns → source parameter overrides
}

var nameSanitize = regexp.MustCompile(`[^a-z0-9]+`)

// ReadCSV parses a CSV with a required `hostname` column. An optional `name`
// column becomes the new source name. Any other columns become per-source
// parameter overrides (column header == parameter name).
func ReadCSV(r io.Reader) ([]Row, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	hostIdx, nameIdx := -1, -1
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "hostname":
			hostIdx = i
		case "name":
			nameIdx = i
		}
	}
	if hostIdx < 0 {
		return nil, fmt.Errorf("csv must contain a `hostname` column")
	}

	var rows []Row
	seen := map[string]int{} // hostname → first line number
	lineNum := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		lineNum++
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
		if len(rec) < len(header) {
			for len(rec) < len(header) {
				rec = append(rec, "")
			}
		}
		host := strings.TrimSpace(rec[hostIdx])
		if host == "" {
			return nil, fmt.Errorf("line %d: empty hostname", lineNum)
		}
		if firstLine, dup := seen[strings.ToLower(host)]; dup {
			return nil, fmt.Errorf("line %d: duplicate hostname %q (first seen on line %d)", lineNum, host, firstLine)
		}
		seen[strings.ToLower(host)] = lineNum
		row := Row{
			Hostname: host,
			Extras:   map[string]string{},
		}
		if nameIdx >= 0 {
			row.Name = strings.TrimSpace(rec[nameIdx])
		}
		if row.Name == "" {
			row.Name = deriveName(host)
		}
		for i, h := range header {
			if i == hostIdx || i == nameIdx {
				continue
			}
			k := strings.TrimSpace(h)
			v := strings.TrimSpace(rec[i])
			if k == "" || v == "" {
				continue
			}
			row.Extras[k] = v
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("csv has no data rows")
	}
	return rows, nil
}

func deriveName(host string) string {
	s := strings.ToLower(host)
	s = nameSanitize.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return "winevt-" + s
}
