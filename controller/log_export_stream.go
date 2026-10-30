/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package controller

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// Serialize expensive exports per gateway without queuing work behind customer requests.
var logExportSlot = make(chan struct{}, 1)

const logExportMaxFileBytes = 256 << 20

var (
	errLogExportFileSize  = errors.New("账单文件超过 256 MB，请缩小时间范围或按用户、模型筛选后重试")
	errLogExportExcelRows = errors.New("Excel 账单超过工作表行数上限，请改用 CSV 或缩小时间范围")
	errLogExportTempSize  = errors.New("账单临时文件超过 1 GB，请改用 CSV 或缩小时间范围")
)

type logExportFileWriter struct {
	context context.Context
	file    *os.File
	written int64
}

func (w *logExportFileWriter) Write(data []byte) (int, error) {
	if err := w.context.Err(); err != nil {
		return 0, err
	}
	if int64(len(data)) > logExportMaxFileBytes-w.written {
		return 0, errLogExportFileSize
	}
	n, err := w.file.Write(data)
	w.written += int64(n)
	return n, err
}

func writeStreamingLogExport(c *gin.Context, params model.LogExportParams, format string) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()
	directory, err := os.MkdirTemp("", "usage-bill-")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "无法创建账单临时文件，请稍后重试"})
		return
	}
	defer os.RemoveAll(directory)
	output, err := os.CreateTemp(directory, "bill-*."+format)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "无法创建账单临时文件，请稍后重试"})
		return
	}
	defer output.Close()
	w := &logExportFileWriter{context: ctx, file: output}
	contentType := "text/csv; charset=utf-8"
	if format == "xlsx" {
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		err = streamLogExportXLSX(ctx, params, w, directory)
	} else {
		err = streamLogExportCSV(ctx, params, w)
	}
	if err != nil {
		if c.Request.Context().Err() != nil {
			return
		}
		message := "账单生成失败，请稍后重试"
		status := http.StatusInternalServerError
		if errors.Is(err, context.DeadlineExceeded) {
			message, status = "账单生成超过 5 分钟，请缩小时间范围或增加筛选条件", http.StatusRequestTimeout
		} else if errors.Is(err, errLogExportExcelRows) || errors.Is(err, errLogExportFileSize) || errors.Is(err, errLogExportTempSize) {
			message, status = err.Error(), http.StatusBadRequest
		}
		c.JSON(status, gin.H{"success": false, "message": message})
		return
	}
	if _, err = output.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "账单文件读取失败，请重试"})
		return
	}
	c.Header("Content-Disposition", "attachment; filename="+strconv.Quote("usage-bill."+format))
	c.Header("Cache-Control", "no-store")
	c.DataFromReader(http.StatusOK, w.written, contentType, output, nil)
}

// A single ordered cursor keeps identical request IDs and split charges intact.
func streamLogExportRows(ctx context.Context, params model.LogExportParams, write func([]string, bool) error) error {
	month := ""
	monthTotal, total := 0.0, 0.0
	err := model.WalkConsumeLogsForExport(ctx, params, func(logs []*model.Log, funds map[model.LogExportFundingKey]model.LogExportFunding, prices map[model.LogExportFundingKey]model.LogExportAgencyPricing) error {
		for _, log := range logs {
			if err := ctx.Err(); err != nil {
				return err
			}
			nextMonth := time.Unix(log.CreatedAt, 0).Format("2006-01")
			if month != "" && month != nextMonth {
				if err := write(logExportSummaryValues(month+" 月度小计", monthTotal), true); err != nil {
					return err
				}
				monthTotal = 0
			}
			month = nextMonth
			other, parsed := parseLogExportOther(log)
			if !parsed {
				other = logExportOther{}
			}
			key := model.LogExportFundingKey{UserID: int64(log.UserId), RequestID: log.RequestId}
			funding, hasFunding := funds[key]
			price, hasPrice := prices[key]
			row := logExportRow{Log: log, Other: other, Funding: funding, HasFunds: hasFunding, AgencyPricing: price, HasAgencyPricing: hasPrice}
			if amount, ok := logExportRowActualMoney(row); ok {
				monthTotal += amount
				total += amount
			}
			if err := write(logExportRowValues(row), false); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if month != "" {
		if err := write(logExportSummaryValues(month+" 月度小计", monthTotal), true); err != nil {
			return err
		}
	}
	return write(logExportSummaryValues("本期消费金额", total), true)
}

func streamLogExportCSV(ctx context.Context, params model.LogExportParams, output io.Writer) error {
	if _, err := io.WriteString(output, "\xEF\xBB\xBF"); err != nil {
		return err
	}
	w := csv.NewWriter(output)
	for _, values := range [][]string{{"按量消费明细"}, {logExportBillingNotice}, logExportHeaders} {
		if err := w.Write(values); err != nil {
			return err
		}
	}
	if err := streamLogExportRows(ctx, params, func(values []string, _ bool) error { return w.Write(values) }); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

func streamLogExportXLSX(ctx context.Context, params model.LogExportParams, output io.Writer, directory string) error {
	f := excelize.NewFile(excelize.Options{TmpDir: directory})
	defer f.Close()
	const sheet = "账单明细"
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return err
	}
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return err
	}
	for i, width := range logExportColumnWidths {
		if err := sw.SetColWidth(i+1, i+1, width); err != nil {
			return err
		}
	}
	header, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Alignment: &excelize.Alignment{Vertical: "center"}})
	if err != nil {
		return err
	}
	numberFormat := "0.00000000"
	moneyStyle, err := f.NewStyle(&excelize.Style{CustomNumFmt: &numberFormat})
	if err != nil {
		return err
	}
	if err := sw.MergeCell("A1", "S1"); err != nil {
		return err
	}
	if err := sw.MergeCell("A2", "S2"); err != nil {
		return err
	}
	rowIndex := 1
	write := func(values []string, summary bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if rowIndex > excelize.TotalRows {
			return errLogExportExcelRows
		}
		if rowIndex%500 == 0 {
			entries, err := os.ReadDir(directory)
			if err != nil {
				return err
			}
			var temporaryBytes int64
			for _, entry := range entries {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				temporaryBytes += info.Size()
			}
			if temporaryBytes > 1<<30 {
				return errLogExportTempSize
			}
		}
		cells := make([]any, len(values))
		for col, value := range values {
			cells[col] = excelize.Cell{Value: value}
			if rowIndex > 3 && col >= 3 && col <= 6 {
				if number, err := strconv.Atoi(value); err == nil {
					cells[col] = number
				}
			} else if rowIndex > 3 && ((col >= 7 && col <= 15) || col == 18) {
				if number, err := strconv.ParseFloat(value, 64); err == nil {
					cells[col] = excelize.Cell{Value: number, StyleID: moneyStyle}
				}
			}
		}
		opts := excelize.RowOpts{Height: 20}
		if rowIndex <= 3 || summary {
			opts.StyleID = header
		}
		if summary {
			if err := sw.MergeCell(fmt.Sprintf("A%d", rowIndex), fmt.Sprintf("L%d", rowIndex)); err != nil {
				return err
			}
		}
		err := sw.SetRow(fmt.Sprintf("A%d", rowIndex), cells, opts)
		rowIndex++
		return err
	}
	for _, values := range [][]string{{"按量消费明细"}, {logExportBillingNotice}, logExportHeaders} {
		if err := write(values, false); err != nil {
			return err
		}
	}
	if err := streamLogExportRows(ctx, params, write); err != nil {
		return err
	}
	if err := sw.Flush(); err != nil {
		return err
	}
	// Excelize otherwise buffers the complete ZIP even for streamed worksheets.
	f.SetZipWriter(func(io.Writer) excelize.ZipWriter { return zip.NewWriter(output) })
	return f.Write(io.Discard)
}
