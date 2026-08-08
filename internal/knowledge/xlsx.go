package knowledge

import (
	"io"

	"github.com/xuri/excelize/v2"
)

func ReadRowsFromXLSX(r io.Reader, sheet string) ([][]string, error) {
	f, err := excelize.OpenReader(r)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if sheet == "" {
		sheet = "release"
	}
	rows, err := f.GetRows(sheet)
	if err != nil {
		return nil, err
	}
	// Keep blank rows so ParseRows can report the original Excel row numbers.
	return rows, nil
}

func rowIsEmpty(row []string) bool {
	for _, cell := range row {
		if cell != "" {
			return false
		}
	}
	return true
}
