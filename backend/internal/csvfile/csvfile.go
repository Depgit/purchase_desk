// Package csvfile reads uploaded CSV text into rows keyed by lower-case column name, and writes CSV.
package csvfile

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
)

type Row struct {
	Line   int
	Values map[string]string
}

func (r Row) Get(col string) string { return r.Values[col] }

// Has reports whether the file has the column at all (a blank cell still counts).
func (r Row) Has(col string) bool { _, ok := r.Values[col]; return ok }

func Parse(text string) ([]string, []Row, error) {
	rd := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\uFEFF")))
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	recs, err := rd.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("the file could not be read as CSV: %v", err)
	}
	return FromTable(recs)
}

// FromTable reads rows already split into cells (a CSV file, or one sheet of an Excel workbook): the first row
// is the header, and blank rows are left out. Line numbers count from 1 for the header.
func FromTable(recs [][]string) ([]string, []Row, error) {
	if len(recs) < 2 {
		return nil, nil, fmt.Errorf("the file needs a header row and at least one data row")
	}
	heads := make([]string, len(recs[0]))
	for i, h := range recs[0] {
		heads[i] = strings.ToLower(strings.TrimSpace(h))
	}
	rows := []Row{}
	for n, rec := range recs[1:] {
		empty := true
		m := map[string]string{}
		for i, h := range heads {
			v := ""
			if i < len(rec) {
				v = strings.TrimSpace(rec[i])
			}
			if v != "" {
				empty = false
			}
			m[h] = v
		}
		if !empty {
			rows = append(rows, Row{Line: n + 2, Values: m})
		}
	}
	return heads, rows, nil
}

func Write(heads []string, rows [][]string) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write(heads)
	_ = w.WriteAll(rows)
	w.Flush()
	return b.Bytes()
}
