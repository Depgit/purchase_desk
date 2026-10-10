// Package spreadsheet writes tables into an Excel (.xlsx) file: one sheet per table, a bold header row, sensible column widths.
package spreadsheet

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

type Sheet struct {
	Name  string
	Heads []string
	Rows  [][]any
	Text  bool // format every column as text, so what someone types (dates, mobiles, codes) comes back as typed
}

// Build returns the .xlsx bytes. Numbers stay numbers, so the vendor can add them up in Excel.
func Build(sheets []Sheet) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"E3EAFD"}}})
	text, _ := f.NewStyle(&excelize.Style{NumFmt: 49}) // "@": text
	used := map[string]int{}
	for i, sh := range sheets {
		name := sheetName(sh.Name, used)
		if i == 0 {
			if err := f.SetSheetName("Sheet1", name); err != nil {
				return nil, err
			}
		} else if _, err := f.NewSheet(name); err != nil {
			return nil, err
		}
		widths := make([]int, len(sh.Heads))
		for c, h := range sh.Heads {
			cell, _ := excelize.CoordinatesToCellName(c+1, 1)
			_ = f.SetCellValue(name, cell, h)
			widths[c] = utf8.RuneCountInString(h)
		}
		if len(sh.Heads) > 0 {
			last, _ := excelize.CoordinatesToCellName(len(sh.Heads), 1)
			_ = f.SetCellStyle(name, "A1", last, bold)
		}
		for r, row := range sh.Rows {
			for c, v := range row {
				cell, _ := excelize.CoordinatesToCellName(c+1, r+2)
				_ = f.SetCellValue(name, cell, v)
				if c < len(widths) {
					if n := utf8.RuneCountInString(fmt.Sprint(v)); n > widths[c] && r < 200 {
						widths[c] = n
					}
				}
			}
		}
		for c, w := range widths {
			col, _ := excelize.ColumnNumberToName(c + 1)
			if w > 45 {
				w = 45
			}
			_ = f.SetColWidth(name, col, col, float64(w+2))
		}
		if sh.Text && len(sh.Heads) > 0 {
			last, _ := excelize.ColumnNumberToName(len(sh.Heads))
			_ = f.SetColStyle(name, "A:"+last, text)
			l, _ := excelize.CoordinatesToCellName(len(sh.Heads), 1)
			_ = f.SetCellStyle(name, "A1", l, bold)
		}
		_ = f.SetPanes(name, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	}
	var b bytes.Buffer
	if err := f.Write(&b); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func sheetName(n string, used map[string]int) string {
	n = strings.NewReplacer(":", " ", "\\", " ", "/", " ", "?", " ", "*", " ", "[", "(", "]", ")").Replace(n)
	if len([]rune(n)) > 31 {
		n = string([]rune(n)[:31])
	}
	if used[n] > 0 {
		used[n]++
		suffix := fmt.Sprintf(" %d", used[n])
		r := []rune(n)
		if len(r)+len(suffix) > 31 {
			r = r[:31-len(suffix)]
		}
		n = string(r) + suffix
	}
	used[n]++
	return n
}

// Read returns every sheet of an .xlsx file as rows of cell text, in sheet order.
func Read(data []byte) ([]string, map[string][][]string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	names := f.GetSheetList()
	out := map[string][][]string{}
	for _, n := range names {
		rows, err := f.GetRows(n)
		if err != nil {
			return nil, nil, err
		}
		out[n] = rows
	}
	return names, out, nil
}

// Send writes the sheets to the response as an .xlsx download named name.
func Send(w http.ResponseWriter, name string, sheets []Sheet) error {
	b, err := Build(sheets)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	_, err = w.Write(b)
	return err
}
