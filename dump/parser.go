package dump

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/gongzhxu/go-mysql/mysql"
	"github.com/pingcap/errors"
)

var ErrSkip = errors.New("Handler error, but skipped")

type ParseHandler interface {
	// Parse CHANGE MASTER TO MASTER_LOG_FILE=name, MASTER_LOG_POS=pos;
	BinLog(name string, pos uint64) error
	GtidSet(gtidsets string) error
	Data(schema string, table string, values []string) error
}

var (
	binlogExp = regexp.MustCompile(`^CHANGE (MASTER|REPLICATION SOURCE) TO (MASTER_LOG_FILE|SOURCE_LOG_FILE)='(.+)', (MASTER_LOG_POS|SOURCE_LOG_POS)=(\d+);`)
	useExp    = regexp.MustCompile("^USE `(.+)`;")
	valuesExp = regexp.MustCompile("^INSERT INTO `(.+?)` VALUES \\((.+)\\);$")

	// The pattern will only match MySQL GTID, as you know SET GLOBAL gtid_slave_pos='0-1-4' is used for MariaDB.
	// SET @@GLOBAL.GTID_PURGED='1638041a-0457-11e9-bb9f-00505690b730:1-429405150';
	// https://dev.mysql.com/doc/refman/5.7/en/replication-gtids-concepts.html
	gtidExp = regexp.MustCompile(`(\w{8}(-\w{4}){3}-\w{12}(:\d+(-\d+)?)+)`)
)

// Parse the dump data with Dumper generate.
// It can not parse all the data formats with mysqldump outputs
func Parse(r io.Reader, h ParseHandler, parseBinlogPos bool) error {
	rb := bufio.NewReaderSize(r, 1024*16)

	var db string
	var binlogParsed bool

	for {
		line, err := rb.ReadString('\n')
		if err != nil && err != io.EOF {
			return errors.Trace(err)
		} else if mysql.ErrorEqual(err, io.EOF) {
			break
		}

		// Ignore '\n' on Linux or '\r\n' on Windows
		line = strings.TrimRightFunc(line, func(c rune) bool {
			return c == '\r' || c == '\n'
		})

		if parseBinlogPos && !binlogParsed {
			// parsed gtid set from mysqldump
			// gtid comes before binlog file-position
			if m := gtidExp.FindAllStringSubmatch(line, -1); len(m) == 1 {
				gtidStr := m[0][1]
				if gtidStr != "" {
					if err := h.GtidSet(gtidStr); err != nil {
						return errors.Trace(err)
					}
				}
			}
			if m := binlogExp.FindAllStringSubmatch(line, -1); len(m) == 1 {
				name := m[0][3]
				pos, err := strconv.ParseUint(m[0][5], 10, 64)
				if err != nil {
					return errors.Errorf("parse binlog %v err, invalid number", line)
				}

				if err = h.BinLog(name, pos); err != nil && err != ErrSkip {
					return errors.Trace(err)
				}

				binlogParsed = true
			}
		}

		if m := useExp.FindAllStringSubmatch(line, -1); len(m) == 1 {
			db = m[0][1]
		}

		if m := valuesExp.FindAllStringSubmatch(line, -1); len(m) == 1 {
			table := m[0][1]

			values, err := parseValues(m[0][2])
			if err != nil {
				return errors.Errorf("parse values %v err", line)
			}

			if err = h.Data(db, table, values); err != nil && err != ErrSkip {
				return errors.Trace(err)
			}
		}
	}

	return nil
}

func parseValues(str string) ([]string, error) {
	// values are separated by comma, but we can not split using comma directly
	// string is enclosed by single quote

	// a simple implementation, may be more robust later.

	values := make([]string, 0, 8)

	i := 0
	for i < len(str) {
		if str[i] != '\'' {
			// no string, read until comma
			j := i + 1
			for ; j < len(str) && str[j] != ','; j++ {
			}
			values = append(values, str[i:j])
			// skip ,
			i = j + 1
		} else {
			// read string until another single quote
			j := i + 1

			escaped := false
			for j < len(str) {
				if str[j] == '\\' {
					// skip escaped character
					j += 2
					escaped = true
					continue
				} else if str[j] == '\'' {
					break
				} else {
					j++
				}
			}

			if j >= len(str) {
				return nil, fmt.Errorf("parse quote values error")
			}

			value := str[i : j+1]
			if escaped {
				value = unescapeString(value)
			}
			values = append(values, value)
			// skip ' and ,
			i = j + 2
		}

		// need skip blank???
	}

	return values, nil
}

// unescapeString un-escapes the string.
// mysqldump will escape the string when dumps,
// Refer http://dev.mysql.com/doc/refman/5.7/en/string-literals.html
func unescapeString(s string) string {
	i := 0

	value := make([]byte, 0, len(s))
	for i < len(s) {
		if s[i] == '\\' {
			j := i + 1
			if j == len(s) {
				// The last char is \, remove
				break
			}

			value = append(value, unescapeChar(s[j]))
			i += 2
		} else {
			value = append(value, s[i])
			i++
		}
	}

	return string(value)
}

func unescapeChar(ch byte) byte {
	// \" \' \\ \n \0 \b \Z \r \t ==> escape to one char
	switch ch {
	case 'n':
		ch = '\n'
	case '0':
		ch = 0
	case 'b':
		ch = 8
	case 'Z':
		ch = 26
	case 'r':
		ch = '\r'
	case 't':
		ch = '\t'
	}
	return ch
}
