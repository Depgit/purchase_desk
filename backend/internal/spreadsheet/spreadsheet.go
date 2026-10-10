// Package spreadsheet writes tables into an Excel (.xlsx) file: one sheet per table, a bold header row, sensible column widths.
package spreadsheet

import (
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

type Sheet struct {
	Name  string
	Heads []string
	Rows  [][]any
	Text  bool // format every column as text, so what someone types (dates, mobiles, codes) comes back as typed

	Hidden      bool         // a helper sheet the formulas read from
	Protect     bool         // read-only, or with LockedCols: only those cells of the existing rows are locked
	LockedCols  []string     // columns whose existing rows cannot be edited (keys, bank details)
	Validations []Validation // dropdowns and stop messages per column
}

// Validation is a rule on one column's data rows. With List or Source the cell must be one of those values (a
// dropdown); with neither, only the prompt shows. Source is a formula and may use {col} for this sheet's column
// letter (as "$E") and {Sheet name.col} for the filled cells of another sheet's column.
type Validation struct {
	Column      string
	List        []string
	Source      string
	PromptTitle string
	Prompt      string
	ErrorTitle  string
	Error       string
	AllowOthers bool // warn instead of stop: the value may be new (a new brand)
}

// lastRow is how far down rules and unlocked cells reach, so rows added at the bottom get them too.
const lastRow = 20000

var placeholder = regexp.MustCompile(`\{([^}]+)\}`)

// resolve fills the placeholders of a validation formula.
func resolve(src string, own []string, heads map[string][]string) string {
	return placeholder.ReplaceAllStringFunc(src, func(m string) string {
		ref := m[1 : len(m)-1]
		if i := strings.LastIndex(ref, "."); i > 0 {
			sheet, col := ref[:i], ref[i+1:]
			for c, h := range heads[sheet] {
				if h == col {
					// Exactly the filled rows from row 2 down, so the dropdown has no blank entries.
					l, _ := excelize.ColumnNumberToName(c + 1)
					return fmt.Sprintf("OFFSET('%[1]s'!$%[2]s$2,0,0,MAX(1,COUNTA('%[1]s'!$%[2]s:$%[2]s)-1),1)", sheet, l)
				}
			}
			return m
		}
		for c, h := range own {
			if h == ref {
				l, _ := excelize.ColumnNumberToName(c + 1)
				return "$" + l
			}
		}
		return m
	})
}

// Build returns the .xlsx bytes. Numbers stay numbers, so the vendor can add them up in Excel.
func Build(sheets []Sheet) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"E3EAFD"}}})
	text, _ := f.NewStyle(&excelize.Style{NumFmt: 49}) // "@": text
	used := map[string]int{}
	heads := map[string][]string{}
	for _, sh := range sheets {
		heads[sh.Name] = sh.Heads
	}
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
		if err := rules(f, name, sh, heads); err != nil {
			return nil, err
		}
		if sh.Protect {
			if len(sh.LockedCols) > 0 && len(sh.Heads) > 0 {
				// Everything is editable except the header and the locked columns of the rows already there.
				open, _ := f.NewStyle(&excelize.Style{NumFmt: 49, Protection: &excelize.Protection{Locked: false}})
				shut, _ := f.NewStyle(&excelize.Style{NumFmt: 49, Protection: &excelize.Protection{Locked: true}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"EDEDED"}}})
				last, _ := excelize.ColumnNumberToName(len(sh.Heads))
				_ = f.SetColStyle(name, "A:"+last, open)
				l, _ := excelize.CoordinatesToCellName(len(sh.Heads), 1)
				_ = f.SetCellStyle(name, "A1", l, bold)
				for c, h := range sh.Heads {
					for _, lc := range sh.LockedCols {
						if h == lc && len(sh.Rows) > 0 {
							from, _ := excelize.CoordinatesToCellName(c+1, 2)
							to, _ := excelize.CoordinatesToCellName(c+1, len(sh.Rows)+1)
							_ = f.SetCellStyle(name, from, to, shut)
						}
					}
				}
			}
			if err := f.ProtectSheet(name, &excelize.SheetProtectionOptions{SelectLockedCells: true, SelectUnlockedCells: true, Sort: true, AutoFilter: true,
				FormatColumns: true, FormatRows: true, InsertRows: len(sh.LockedCols) > 0}); err != nil {
				return nil, err
			}
		}
		if sh.Hidden {
			_ = f.SetSheetVisible(name, false)
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

// rules adds a sheet's dropdowns, prompts and stop messages to its data rows.
func rules(f *excelize.File, name string, sh Sheet, heads map[string][]string) error {
	for _, v := range sh.Validations {
		col := -1
		for c, h := range sh.Heads {
			if h == v.Column {
				col = c
			}
		}
		if col < 0 {
			continue
		}
		l, _ := excelize.ColumnNumberToName(col + 1)
		dv := excelize.NewDataValidation(true)
		dv.SetSqref(fmt.Sprintf("%s2:%s%d", l, l, lastRow))
		switch {
		case len(v.List) > 0:
			if err := dv.SetDropList(v.List); err != nil {
				return err
			}
		case v.Source != "":
			dv.SetSqrefDropList(resolve(v.Source, sh.Heads, heads))
		default:
			if err := dv.SetRange(0, 32767, excelize.DataValidationTypeTextLength, excelize.DataValidationOperatorBetween); err != nil {
				return err
			}
		}
		if v.Prompt != "" {
			dv.SetInput(v.PromptTitle, v.Prompt)
		}
		if v.Error != "" {
			style := excelize.DataValidationErrorStyleStop
			if v.AllowOthers {
				style = excelize.DataValidationErrorStyleWarning
			}
			dv.SetError(style, v.ErrorTitle, v.Error)
		}
		if err := f.AddDataValidation(name, dv); err != nil {
			return err
		}
	}
	return nil
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
