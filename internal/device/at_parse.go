package device

import (
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"

	"vocat/internal/modem"
)

func valueAfterPrefix(response modem.Response, prefix string) string {
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), strings.ToUpper(prefix)) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

func csvValues(value string) []string {
	reader := csv.NewReader(strings.NewReader(value))
	reader.TrimLeadingSpace = true
	reader.LazyQuotes = true
	record, err := reader.Read()
	if err != nil && err != io.EOF {
		return nil
	}
	for index := range record {
		record[index] = strings.TrimSpace(record[index])
	}
	return record
}

func decimalDigits(value string, minimum, maximum int) bool {
	value = strings.TrimSpace(value)
	return len(value) >= minimum && len(value) <= maximum && strings.IndexFunc(value, func(character rune) bool {
		return character < '0' || character > '9'
	}) < 0
}

func parseOptionalInt(value string) *int {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return intPointer(number)
}

func intPointer(value int) *int {
	return &value
}

func definiteATRejection(err error) bool {
	var commandErr *modem.CommandError
	if !errors.As(err, &commandErr) {
		return false
	}
	final := strings.ToUpper(strings.TrimSpace(commandErr.Final))
	return final == "ERROR" || strings.HasPrefix(final, "+CME ERROR") || strings.HasPrefix(final, "+CMS ERROR")
}
