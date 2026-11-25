package main

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// Minimal XLSX reader: pulls column A of the first sheet. Only what the
// license table needs, so the client stays dependency-free.

type xlsxSharedStrings struct {
	Items []struct {
		Text string `xml:"t"`
		Runs []struct {
			Text string `xml:"t"`
		} `xml:"r"`
	} `xml:"si"`
}

type xlsxSheet struct {
	Rows []struct {
		Cells []struct {
			Ref    string `xml:"r,attr"`
			Type   string `xml:"t,attr"`
			Value  string `xml:"v"`
			Inline struct {
				Text string `xml:"t"`
			} `xml:"is"`
		} `xml:"c"`
	} `xml:"sheetData>row"`
}

// readLicenseCodes returns the non-empty values of column A, skipping the
// header row when it looks like a title rather than a code.
func readLicenseCodes(path string) ([]string, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("打开表格失败: %w", err)
	}
	defer archive.Close()

	var shared []string
	var sheetData []byte
	for _, file := range archive.File {
		switch file.Name {
		case "xl/sharedStrings.xml":
			reader, err := file.Open()
			if err != nil {
				return nil, err
			}
			var doc xlsxSharedStrings
			err = xml.NewDecoder(reader).Decode(&doc)
			_ = reader.Close()
			if err != nil {
				return nil, fmt.Errorf("解析共享字符串失败: %w", err)
			}
			for _, item := range doc.Items {
				text := item.Text
				if text == "" {
					var b strings.Builder
					for _, run := range item.Runs {
						b.WriteString(run.Text)
					}
					text = b.String()
				}
				shared = append(shared, text)
			}
		case "xl/worksheets/sheet1.xml":
			reader, err := file.Open()
			if err != nil {
				return nil, err
			}
			sheetData, err = io.ReadAll(io.LimitReader(reader, 8<<20))
			_ = reader.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	if len(sheetData) == 0 {
		return nil, fmt.Errorf("表格中没有工作表")
	}

	var sheet xlsxSheet
	if err := xml.Unmarshal(sheetData, &sheet); err != nil {
		return nil, fmt.Errorf("解析工作表失败: %w", err)
	}

	codes := make([]string, 0, len(sheet.Rows))
	for _, row := range sheet.Rows {
		for _, cell := range row.Cells {
			if !isColumnA(cell.Ref) {
				continue
			}
			value := cell.Value
			switch cell.Type {
			case "s":
				index := -1
				if _, err := fmt.Sscanf(cell.Value, "%d", &index); err == nil && index >= 0 && index < len(shared) {
					value = shared[index]
				}
			case "inlineStr":
				value = cell.Inline.Text
			}
			value = strings.TrimSpace(value)
			if value != "" {
				codes = append(codes, value)
			}
		}
	}
	return codes, nil
}

func isColumnA(ref string) bool {
	if len(ref) < 2 || (ref[0] != 'A' && ref[0] != 'a') {
		return false
	}
	for _, r := range ref[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// normalizeLicenseCode accepts codes typed with or without dashes.
func normalizeLicenseCode(code string) string {
	cleaned := strings.ToUpper(strings.TrimSpace(code))
	cleaned = strings.ReplaceAll(cleaned, "-", "")
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	if len(cleaned) != 16 {
		return ""
	}
	return cleaned[0:4] + "-" + cleaned[4:8] + "-" + cleaned[8:12] + "-" + cleaned[12:16]
}
