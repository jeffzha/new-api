package controller

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

var logExportHeaders = []string{
	"用户名称", "账期", "模型名称", "输入token", "输出token", "缓存读取token",
	"缓存创建token", " 输入单价（元/M） ", " 输出单价（元/M） ", "缓存读取倍率",
	"缓存创建倍率", "阶梯折扣", "实际消费（元）", "代金券抵扣（元）",
	"充值余额支付（元）", "授信额度支付（元）",
	"是否使用代理商优惠", "代理商销售价格（系数）", "代理商优惠金额（元）",
}

var logExportColumnWidths = []float64{
	32.9074074074074, 18.9074074074074, 17.6296296296296, 14.5462962962963,
	13, 19.8148148148148, 17.4537037037037, 23.1759259259259,
	24.1759259259259, 16.0925925925926, 21.0925925925926, 12.5462962962963,
	23, 14.2685185185185, 13, 13.3611111111111,
	18, 22, 20,
}

const logExportBillingNotice = "计费说明：实际消费为客户最终结算金额，已包含代理商销售价格、客户专属价格及其他适用优惠；代理商成本和平台成本不作为客户账单金额。每笔记录均标明是否使用代理商优惠、销售价格系数及优惠金额。"

type logExportDocument struct {
	LogType    int
	Rows       []logExportRow
	MonthTotal map[string]float64
	Total      float64
}

func logExportSummaryValues(label string, amount float64) []string {
	values := make([]string, len(logExportHeaders))
	values[0] = label
	values[12] = fmt.Sprintf("%.8f", amount)
	return values
}

var errLogExportDocumentSize = errors.New("PDF/Word 账单内容过多，请选择 Excel/CSV 格式或缩小时间范围")

func loadLogExportDocument(ctx context.Context, params model.LogExportParams) (logExportDocument, error) {
	document := logExportDocument{LogType: params.LogType, MonthTotal: make(map[string]float64)}
	textBytes := 0
	err := model.WalkConsumeLogsForExport(ctx, params, func(logs []*model.Log, funding map[model.LogExportFundingKey]model.LogExportFunding, agencyPricing map[model.LogExportFundingKey]model.LogExportAgencyPricing) error {
		if len(document.Rows)+len(logs) > model.LogExportMaxRows {
			return model.ErrLogExportTooManyRows
		}
		for _, log := range logs {
			textBytes += len(log.Content) + len(log.Other) + len(log.Username) + len(log.ModelName) + 512
		}
		if textBytes > 8<<20 {
			return errLogExportDocumentSize
		}
		rows, err := logExportBatchRows(ctx, params.LogType, logs, funding, agencyPricing)
		if err != nil {
			return err
		}
		document.Rows = append(document.Rows, rows...)
		return nil
	})
	if err != nil {
		return logExportDocument{}, err
	}
	for _, row := range document.Rows {
		if params.LogType != 0 && params.LogType != model.LogTypeConsume {
			document.Total++
			continue
		}
		if amount, ok := logExportRowActualMoney(row); ok {
			month := time.Unix(row.Log.CreatedAt, 0).Format("2006-01")
			document.MonthTotal[month] += amount
			document.Total += amount
		}
	}
	return document, nil
}

func logExportTableRows(document logExportDocument) [][]string {
	rows := make([][]string, 0, len(document.Rows)+len(document.MonthTotal)+4)
	if document.LogType != 0 && document.LogType != model.LogTypeConsume {
		for _, row := range document.Rows {
			rows = append(rows, logExportEventValues(document.LogType, row))
		}
		return rows
	}
	currentMonth := ""
	for _, row := range document.Rows {
		month := time.Unix(row.Log.CreatedAt, 0).Format("2006-01")
		if currentMonth != "" && month != currentMonth {
			rows = append(rows, logExportSummaryValues(currentMonth+" 月度小计", document.MonthTotal[currentMonth]))
		}
		currentMonth = month
		rows = append(rows, logExportRowValues(row))
	}
	if currentMonth != "" {
		rows = append(rows, logExportSummaryValues(currentMonth+" 月度小计", document.MonthTotal[currentMonth]))
	}
	return rows
}

func writeLogExport(c *gin.Context, params model.LogExportParams) {
	params.LogType = model.LogTypeConsume
	if value, exists := c.GetQuery("type"); exists {
		logType, err := strconv.Atoi(value)
		if err != nil || (logType != model.LogTypeConsume && logType != model.LogTypeTopup &&
			logType != model.LogTypeQuotaAdjustment && logType != model.LogTypeSystem &&
			logType != model.LogTypeError && logType != model.LogTypeRefund) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "不支持的账单类型"})
			return
		}
		params.LogType = logType
	}
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "csv")))
	if format == "excel" {
		format = "xlsx"
	}
	if format == "word" {
		format = "docx"
	}
	if format != "csv" && format != "xlsx" && format != "pdf" && format != "docx" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "不支持的账单格式"})
		return
	}
	select {
	case logExportSlot <- struct{}{}:
		defer func() { <-logExportSlot }()
	default:
		c.Header("Retry-After", "30")
		c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "message": "当前有账单正在生成，请稍后再试"})
		return
	}
	if format == "csv" || format == "xlsx" {
		writeStreamingLogExport(c, params, format)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()
	document, err := loadLogExportDocument(ctx, params)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, model.ErrLogExportTooManyRows) || errors.Is(err, errLogExportDocumentSize) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"success": false, "message": err.Error()})
		return
	}

	var body []byte
	var contentType string
	switch format {
	case "xlsx":
		body, err = buildLogExportXLSX(document)
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "pdf":
		body, err = buildLogExportPDF(document)
		contentType = "application/pdf"
	case "docx":
		body, err = buildLogExportDOCX(document)
		contentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		body, err = buildLogExportCSV(document)
		contentType = "text/csv; charset=utf-8"
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errLogExportDocumentSize) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"success": false, "message": err.Error()})
		return
	}
	if len(body) > logExportMaxFileBytes {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": errLogExportFileSize.Error()})
		return
	}
	c.Header("Content-Disposition", "attachment; filename="+strconv.Quote("usage-bill."+format))
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, contentType, body)
}

func buildLogExportCSV(document logExportDocument) ([]byte, error) {
	schema := logExportSchemaFor(document.LogType)
	var body bytes.Buffer
	body.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&body)
	if err := writer.Write([]string{schema.Title}); err != nil {
		return nil, err
	}
	if err := writer.Write([]string{schema.Notice}); err != nil {
		return nil, err
	}
	if err := writer.Write(schema.Headers); err != nil {
		return nil, err
	}
	for _, row := range logExportTableRows(document) {
		if err := writer.Write(row); err != nil {
			return nil, err
		}
	}
	if err := writer.Write(schema.summary(schema.totalLabel(), document.Total)); err != nil {
		return nil, err
	}
	writer.Flush()
	return body.Bytes(), writer.Error()
}

func buildLogExportXLSX(document logExportDocument) ([]byte, error) {
	schema := logExportSchemaFor(document.LogType)
	consume := document.LogType == 0 || document.LogType == model.LogTypeConsume
	file := excelize.NewFile()
	defer file.Close()
	const sheet = "账单明细"
	if err := file.SetSheetName("Sheet1", sheet); err != nil {
		return nil, err
	}
	border := []excelize.Border{{Type: "left", Color: "000000", Style: 1}, {Type: "right", Color: "000000", Style: 1}, {Type: "top", Color: "000000", Style: 1}, {Type: "bottom", Color: "000000", Style: 1}}
	titleStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 14, Bold: true}, Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"}, Border: []excelize.Border{{Type: "bottom", Color: "000000", Style: 1}}})
	headerStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11, Bold: true}, Alignment: &excelize.Alignment{Vertical: "center"}, Border: border, Fill: excelize.Fill{Type: "pattern", Color: []string{"FFFFFF"}, Pattern: 1}})
	headerLeftStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11, Bold: true}, Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"}, Border: border, Fill: excelize.Fill{Type: "pattern", Color: []string{"FFFFFF"}, Pattern: 1}})
	headerRightStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11, Bold: true}, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}, Border: border, Fill: excelize.Fill{Type: "pattern", Color: []string{"FFFFFF"}, Pattern: 1}})
	headerCenterStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11, Bold: true}, Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"}, Border: border, Fill: excelize.Fill{Type: "pattern", Color: []string{"FFFFFF"}, Pattern: 1}})
	bodyStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11}, Alignment: &excelize.Alignment{Vertical: "center"}, Border: border})
	bodyLeftStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11}, Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"}, Border: border})
	summaryStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11, Bold: true}, Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"}, Border: border})
	totalStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11, Bold: true}, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}, Border: border})
	countFormat := `_ * #,##0_ ;_ * \-#,##0_ ;_ * "-"??_ ;_ @_ `
	countStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11}, Alignment: &excelize.Alignment{Vertical: "center"}, Border: border, CustomNumFmt: &countFormat})
	priceFormat := `_ * #,##0.00_ ;_ * \-#,##0.00_ ;_ * "-"??_ ;_ @_ `
	moneyFormat := `0.000000;\-0.000000;"-";@`
	discountFormat := `0.00_);[Red]\(0.00\)`
	priceStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11}, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}, Border: border, CustomNumFmt: &priceFormat})
	ratioStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11}, Alignment: &excelize.Alignment{Vertical: "center"}, Border: border, CustomNumFmt: &priceFormat})
	discountStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11}, Alignment: &excelize.Alignment{Vertical: "center"}, Border: border, CustomNumFmt: &discountFormat})
	moneyStyle, _ := file.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "宋体", Size: 11}, Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"}, Border: border, CustomNumFmt: &moneyFormat})
	lastColumn, _ := excelize.ColumnNumberToName(len(schema.Headers))
	if err := file.MergeCell(sheet, "A1", lastColumn+"1"); err != nil {
		return nil, err
	}
	file.SetCellValue(sheet, "A1", schema.Title)
	file.SetCellStyle(sheet, "A1", lastColumn+"1", titleStyle)
	file.SetRowHeight(sheet, 1, 17.4)
	if err := file.MergeCell(sheet, "A2", lastColumn+"2"); err != nil {
		return nil, err
	}
	file.SetCellValue(sheet, "A2", schema.Notice)
	file.SetCellStyle(sheet, "A2", lastColumn+"2", bodyLeftStyle)
	file.SetRowHeight(sheet, 2, 32)
	for col, value := range schema.Headers {
		cell, _ := excelize.CoordinatesToCellName(col+1, 3)
		file.SetCellValue(sheet, cell, value)
	}
	file.SetCellStyle(sheet, "A3", lastColumn+"3", headerStyle)
	file.SetCellStyle(sheet, "B3", "B3", headerLeftStyle)
	if consume {
		file.SetCellStyle(sheet, "H3", "I3", headerRightStyle)
		file.SetCellStyle(sheet, "M3", lastColumn+"3", headerCenterStyle)
	}
	file.SetRowHeight(sheet, 3, 20)
	rowIndex := 4
	for _, values := range logExportTableRows(document) {
		isSummary := consume && strings.Contains(values[0], "月度小计")
		if isSummary {
			file.MergeCell(sheet, fmt.Sprintf("A%d", rowIndex), fmt.Sprintf("L%d", rowIndex))
		}
		for col, value := range values {
			if len([]rune(value)) > excelize.TotalCellChars {
				return nil, errLogExportExcelCell
			}
			if isSummary && col > 0 && col < 12 {
				continue
			}
			cell, _ := excelize.CoordinatesToCellName(col+1, rowIndex)
			cellValue := any(value)
			if consume && col >= 3 && col <= 6 {
				if number, err := strconv.Atoi(value); err == nil {
					cellValue = number
				}
			} else if consume && ((col >= 7 && col <= 15) || col == 17 || col == 18) {
				if number, err := strconv.ParseFloat(value, 64); err == nil {
					cellValue = number
				}
			}
			file.SetCellValue(sheet, cell, cellValue)
		}
		style := bodyStyle
		if isSummary {
			style = summaryStyle
		}
		file.SetCellStyle(sheet, fmt.Sprintf("A%d", rowIndex), fmt.Sprintf("%s%d", lastColumn, rowIndex), style)
		if consume && !isSummary {
			file.SetCellStyle(sheet, fmt.Sprintf("B%d", rowIndex), fmt.Sprintf("B%d", rowIndex), bodyLeftStyle)
			file.SetCellStyle(sheet, fmt.Sprintf("D%d", rowIndex), fmt.Sprintf("G%d", rowIndex), countStyle)
			file.SetCellStyle(sheet, fmt.Sprintf("H%d", rowIndex), fmt.Sprintf("I%d", rowIndex), priceStyle)
			file.SetCellStyle(sheet, fmt.Sprintf("J%d", rowIndex), fmt.Sprintf("K%d", rowIndex), ratioStyle)
			file.SetCellStyle(sheet, fmt.Sprintf("L%d", rowIndex), fmt.Sprintf("L%d", rowIndex), discountStyle)
			file.SetCellStyle(sheet, fmt.Sprintf("M%d", rowIndex), fmt.Sprintf("P%d", rowIndex), moneyStyle)
			file.SetCellStyle(sheet, fmt.Sprintf("Q%d", rowIndex), fmt.Sprintf("Q%d", rowIndex), bodyStyle)
			file.SetCellStyle(sheet, fmt.Sprintf("R%d", rowIndex), fmt.Sprintf("R%d", rowIndex), ratioStyle)
			file.SetCellStyle(sheet, fmt.Sprintf("S%d", rowIndex), fmt.Sprintf("S%d", rowIndex), moneyStyle)
		}
		file.SetRowHeight(sheet, rowIndex, 20)
		rowIndex++
	}
	rowIndex += 2
	file.MergeCell(sheet, fmt.Sprintf("A%d", rowIndex), fmt.Sprintf("%s%d", lastColumn, rowIndex))
	file.SetCellValue(sheet, fmt.Sprintf("A%d", rowIndex), schema.totalText(document.Total))
	file.SetCellStyle(sheet, fmt.Sprintf("A%d", rowIndex), fmt.Sprintf("%s%d", lastColumn, rowIndex), totalStyle)
	file.SetRowHeight(sheet, rowIndex, 22)
	for i, width := range schema.Widths {
		col, _ := excelize.ColumnNumberToName(i + 1)
		file.SetColWidth(sheet, col, col, width)
	}
	orientation, paperSize, fit := "portrait", 9, 1
	file.SetPageLayout(sheet, &excelize.PageLayoutOptions{Orientation: &orientation, Size: &paperSize, FitToWidth: &fit})
	buffer, err := file.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func xmlEscape(value string) string {
	var escaped strings.Builder
	_ = xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}

func buildLogExportDOCX(document logExportDocument) ([]byte, error) {
	schema := logExportSchemaFor(document.LogType)
	columnWidths := make([]int, len(schema.Headers))
	totalWeight := 0.0
	for _, width := range schema.Widths {
		totalWeight += width
	}
	for col := range columnWidths {
		columnWidths[col] = 800
		if document.LogType != 0 && document.LogType != model.LogTypeConsume {
			columnWidths[col] = int(15200 * schema.Widths[col] / totalWeight)
		}
	}
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	body.WriteString(`<w:p><w:r><w:rPr><w:b/><w:sz w:val="28"/><w:rFonts w:eastAsia="宋体"/></w:rPr><w:t>` + xmlEscape(schema.Title) + `</w:t></w:r></w:p>`)
	body.WriteString(`<w:p><w:r><w:rPr><w:sz w:val="18"/><w:rFonts w:eastAsia="宋体"/></w:rPr><w:t>`)
	body.WriteString(xmlEscape(schema.Notice))
	body.WriteString(`</w:t></w:r></w:p>`)
	body.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="0" w:type="auto"/><w:tblLayout w:type="fixed"/><w:tblBorders><w:top w:val="single" w:sz="4" w:color="000000"/><w:left w:val="single" w:sz="4" w:color="000000"/><w:bottom w:val="single" w:sz="4" w:color="000000"/><w:right w:val="single" w:sz="4" w:color="000000"/><w:insideH w:val="single" w:sz="4" w:color="000000"/><w:insideV w:val="single" w:sz="4" w:color="000000"/></w:tblBorders></w:tblPr>`)
	body.WriteString(`<w:tblGrid>`)
	for _, width := range columnWidths {
		fmt.Fprintf(&body, `<w:gridCol w:w="%d"/>`, width)
	}
	body.WriteString(`</w:tblGrid>`)
	writeRow := func(values []string, bold bool, header bool) {
		body.WriteString(`<w:tr>`)
		if header {
			body.WriteString(`<w:trPr><w:tblHeader/></w:trPr>`)
		}
		for col, value := range values {
			fmt.Fprintf(&body, `<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/>`, columnWidths[col])
			body.WriteString(`</w:tcPr><w:p><w:pPr><w:jc w:val="center"/></w:pPr><w:r><w:rPr><w:rFonts w:eastAsia="宋体"/><w:sz w:val="14"/>`)
			if bold {
				body.WriteString(`<w:b/>`)
			}
			body.WriteString(`</w:rPr><w:t>`)
			body.WriteString(xmlEscape(value))
			body.WriteString(`</w:t></w:r></w:p></w:tc>`)
		}
		body.WriteString(`</w:tr>`)
	}
	writeRow(schema.Headers, true, true)
	for _, row := range logExportTableRows(document) {
		if (document.LogType != 0 && document.LogType != model.LogTypeConsume) || !strings.Contains(row[0], "月度小计") {
			writeRow(row, false, false)
			continue
		}
		body.WriteString(`<w:tr><w:tc><w:tcPr><w:gridSpan w:val="12"/><w:tcW w:w="9600" w:type="dxa"/><w:shd w:fill="FFFFFF"/></w:tcPr><w:p><w:pPr><w:jc w:val="center"/></w:pPr><w:r><w:rPr><w:b/><w:rFonts w:eastAsia="宋体"/><w:sz w:val="14"/></w:rPr><w:t>`)
		body.WriteString(xmlEscape(row[0]))
		body.WriteString(`</w:t></w:r></w:p></w:tc>`)
		for _, value := range row[12:] {
			body.WriteString(`<w:tc><w:tcPr><w:tcW w:w="800" w:type="dxa"/></w:tcPr><w:p><w:pPr><w:jc w:val="center"/></w:pPr><w:r><w:rPr><w:b/><w:rFonts w:eastAsia="宋体"/><w:sz w:val="14"/></w:rPr><w:t>`)
			body.WriteString(xmlEscape(value))
			body.WriteString(`</w:t></w:r></w:p></w:tc>`)
		}
		body.WriteString(`</w:tr>`)
	}
	body.WriteString(fmt.Sprintf(`</w:tbl><w:p/><w:tbl><w:tblPr><w:tblW w:w="0" w:type="auto"/><w:tblBorders><w:top w:val="single" w:sz="4" w:color="000000"/><w:left w:val="single" w:sz="4" w:color="000000"/><w:bottom w:val="single" w:sz="4" w:color="000000"/><w:right w:val="single" w:sz="4" w:color="000000"/></w:tblBorders></w:tblPr><w:tr><w:tc><w:tcPr><w:gridSpan w:val="%d"/><w:tcW w:w="15200" w:type="dxa"/></w:tcPr><w:p><w:pPr><w:jc w:val="right"/></w:pPr><w:r><w:rPr><w:b/><w:rFonts w:eastAsia="宋体"/><w:sz w:val="22"/></w:rPr><w:t>`, len(schema.Headers)))
	body.WriteString(xmlEscape(schema.totalText(document.Total)))
	body.WriteString(`</w:t></w:r></w:p></w:tc></w:tr></w:tbl><w:sectPr><w:pgSz w:w="16838" w:h="11906" w:orient="landscape"/><w:pgMar w:top="567" w:right="340" w:bottom="567" w:left="340"/></w:sectPr></w:body></w:document>`)
	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   body.String(),
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func pdfText(value string) string {
	var output strings.Builder
	for _, character := range value {
		var data [4]byte
		if character <= 0xffff {
			binary.BigEndian.PutUint16(data[:2], uint16(character))
			output.WriteString(fmt.Sprintf("%04X", binary.BigEndian.Uint16(data[:2])))
			continue
		}
		high, low := utf16Surrogate(character)
		output.WriteString(fmt.Sprintf("%04X%04X", high, low))
	}
	return output.String()
}

func utf16Surrogate(character rune) (uint16, uint16) {
	value := uint32(character) - 0x10000
	return uint16(0xD800 + value>>10), uint16(0xDC00 + value&0x3FF)
}

func pdfLiteral(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `(`, `\(`)
	return strings.ReplaceAll(value, `)`, `\)`)
}

func writePDFCellText(content *strings.Builder, value string, left, baseline, width, fontSize float64, alignment string) {
	if value == "" {
		return
	}
	textWidth := 0.0
	for _, character := range value {
		if character <= 0x7f {
			textWidth += fontSize * 0.52
		} else {
			textWidth += fontSize
		}
	}
	availableWidth := max(width-4, 1)
	if textWidth > availableWidth {
		fontSize = max(3, fontSize*availableWidth/textWidth)
		textWidth = 0
		for _, character := range value {
			if character <= 0x7f {
				textWidth += fontSize * 0.52
			} else {
				textWidth += fontSize
			}
		}
	}
	x := left + 2
	if alignment == "center" {
		x = left + (width-textWidth)/2
	} else if alignment == "right" {
		x = left + width - textWidth - 2
	}
	runes := []rune(value)
	for start := 0; start < len(runes); {
		ascii := runes[start] <= 0x7f
		end := start + 1
		for end < len(runes) && (runes[end] <= 0x7f) == ascii {
			end++
		}
		run := string(runes[start:end])
		if ascii {
			fmt.Fprintf(content, "BT /F2 %.2f Tf 1 0 0 1 %.2f %.2f Tm (%s) Tj ET\n", fontSize, x, baseline, pdfLiteral(run))
			x += float64(len([]rune(run))) * fontSize * 0.52
		} else {
			fmt.Fprintf(content, "BT /F1 %.2f Tf 1 0 0 1 %.2f %.2f Tm <%s> Tj ET\n", fontSize, x, baseline, pdfText(run))
			x += float64(len([]rune(run))) * fontSize
		}
		start = end
	}
}

// Event details can be much longer than consumption labels. Wrap them into
// continuation lines before pagination instead of drawing beyond the cell.
func logExportPDFRows(document logExportDocument, schema logExportSchema) ([][]string, error) {
	rows := logExportTableRows(document)
	if document.LogType == 0 || document.LogType == model.LogTypeConsume {
		return rows, nil
	}
	totalWidth := 0.0
	for _, width := range schema.Widths {
		totalWidth += width
	}
	wrapped := make([][]string, 0, len(rows))
	for _, row := range rows {
		lines := make([][]string, len(row))
		lineCount := 1
		for col, value := range row {
			available := 1143*schema.Widths[col]/totalWidth - 4
			var line strings.Builder
			width := 0.0
			for _, char := range value {
				if char == 13 {
					continue
				}
				characterWidth := 6.5
				if char <= 0x7f {
					characterWidth *= 0.52
				}
				if char == 10 || (width+characterWidth > available && line.Len() > 0) {
					lines[col] = append(lines[col], line.String())
					line.Reset()
					width = 0
				}
				if char != 10 {
					line.WriteRune(char)
					width += characterWidth
				}
			}
			lines[col] = append(lines[col], line.String())
			lineCount = max(lineCount, len(lines[col]))
		}
		if len(wrapped)+lineCount > 10000 {
			return nil, errLogExportDocumentSize
		}
		for i := range lineCount {
			values := make([]string, len(row))
			for col := range row {
				if i < len(lines[col]) {
					values[col] = lines[col][i]
				}
			}
			wrapped = append(wrapped, values)
		}
	}
	return wrapped, nil
}

func buildLogExportPDF(document logExportDocument) ([]byte, error) {
	schema := logExportSchemaFor(document.LogType)
	rows, err := logExportPDFRows(document, schema)
	if err != nil {
		return nil, err
	}
	const rowsPerPage = 27
	pageCount := max((len(rows)+rowsPerPage-1)/rowsPerPage, 1)
	objects := make([][]byte, 6)
	objects[0] = []byte(`<< /Type /Catalog /Pages 2 0 R >>`)
	objects[2] = []byte(`<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /UniGB-UCS2-H /DescendantFonts [4 0 R] >>`)
	objects[3] = []byte(`<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 4 >> /FontDescriptor 5 0 R /DW 1000 >>`)
	objects[4] = []byte(`<< /Type /FontDescriptor /FontName /STSong-Light /Flags 6 /FontBBox [-25 -254 1000 880] /ItalicAngle 0 /Ascent 880 /Descent -120 /CapHeight 880 /StemV 80 >>`)
	objects[5] = []byte(`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>`)
	pageObjectIDs := make([]int, 0, pageCount)
	for page := range pageCount {
		pageObjectID := 7 + page*2
		contentObjectID := pageObjectID + 1
		pageObjectIDs = append(pageObjectIDs, pageObjectID)
		start := page * rowsPerPage
		end := min(start+rowsPerPage, len(rows))
		pageRows := append([][]string{schema.Headers}, rows[start:end]...)
		var content strings.Builder
		content.WriteString("0.7 w 0 0 0 RG\n")
		writePDFCellText(&content, schema.Title, 0, 810, 1191, 16, "center")
		writePDFCellText(&content, schema.Notice, 24, 792, 1143, 8, "left")
		if page > 0 {
			writePDFCellText(&content, fmt.Sprintf("第%d页", page+1), 1080, 810, 80, 8, "right")
		}
		rowHeight, left, top, tableWidth := 24.0, 24.0, 770.0, 1143.0
		columnWidths := make([]float64, len(schema.Widths))
		totalWeight := 0.0
		for _, width := range schema.Widths {
			totalWeight += width
		}
		for i, width := range schema.Widths {
			columnWidths[i] = tableWidth * width / totalWeight
		}
		for rowIndex, row := range pageRows {
			y := top - float64(rowIndex)*rowHeight
			content.WriteString(fmt.Sprintf("%.2f %.2f %.2f %.2f re S\n", left, y-rowHeight, tableWidth, rowHeight))
			isSummary := (document.LogType == 0 || document.LogType == model.LogTypeConsume) && rowIndex > 0 && strings.Contains(row[0], "月度小计")
			x := left
			for col := 1; col < len(schema.Headers); col++ {
				x += columnWidths[col-1]
				if isSummary && col < 12 {
					continue
				}
				content.WriteString(fmt.Sprintf("%.2f %.2f m %.2f %.2f l S\n", x, y-rowHeight, x, y))
			}
			fontSize := 6.5
			if rowIndex == 0 {
				fontSize = 7
			}
			if isSummary {
				mergedWidth := 0.0
				for _, width := range columnWidths[:12] {
					mergedWidth += width
				}
				writePDFCellText(&content, row[0], left, y-15, mergedWidth, fontSize, "center")
				x = left + mergedWidth
				for col := 12; col < len(row); col++ {
					writePDFCellText(&content, row[col], x, y-15, columnWidths[col], fontSize, "center")
					x += columnWidths[col]
				}
				continue
			}
			x = left
			for col, value := range row {
				alignment := "left"
				if (rowIndex == 0 && col >= 12) || (rowIndex > 0 && col >= 12) {
					alignment = "center"
				} else if col == 7 || col == 8 {
					alignment = "right"
				}
				writePDFCellText(&content, value, x, y-15, columnWidths[col], fontSize, alignment)
				x += columnWidths[col]
			}
		}
		if page == pageCount-1 {
			y := top - float64(len(pageRows))*rowHeight - 2*rowHeight
			content.WriteString(fmt.Sprintf("%.2f %.2f %.2f %.2f re S\n", left, y-rowHeight, tableWidth, rowHeight))
			writePDFCellText(&content, schema.totalText(document.Total), left, y-15, tableWidth, 8, "right")
		}
		stream := content.String()
		objects = append(objects, []byte(fmt.Sprintf(`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 1191 842] /Resources << /Font << /F1 3 0 R /F2 6 0 R >> >> /Contents %d 0 R >>`, contentObjectID)))
		objects = append(objects, []byte(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream)))
	}
	kids := make([]string, len(pageObjectIDs))
	for i, id := range pageObjectIDs {
		kids[i] = fmt.Sprintf("%d 0 R", id)
	}
	objects[1] = []byte(fmt.Sprintf(`<< /Type /Pages /Count %d /Kids [%s] >>`, pageCount, strings.Join(kids, " ")))
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&output, "trailer << /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", len(objects)+1, xref)
	return output.Bytes(), nil
}
