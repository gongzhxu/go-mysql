// Copyright 2012, Google Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package schema

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/pingcap/errors"

	"github.com/gongzhxu/go-mysql/mysql"
)

var (
	ErrTableNotExist    = errors.New("table is not exist")
	ErrMissingTableMeta = errors.New("missing table meta")
	HAHealthCheckSchema = "mysql.ha_health_check"
)

// Different column type
const (
	TYPE_NUMBER    = iota + 1 // tinyint, smallint, int, bigint, year
	TYPE_FLOAT                // float, double
	TYPE_ENUM                 // enum
	TYPE_SET                  // set
	TYPE_STRING               // char, varchar, etc.
	TYPE_DATETIME             // datetime
	TYPE_TIMESTAMP            // timestamp
	TYPE_DATE                 // date
	TYPE_TIME                 // time
	TYPE_BIT                  // bit
	TYPE_JSON                 // json
	TYPE_DECIMAL              // decimal
	TYPE_MEDIUM_INT
	TYPE_BINARY // binary, varbinary
	TYPE_POINT  // coordinates
)

type TableColumn struct {
	Name       string
	Type       int
	Collation  string
	RawType    string
	IsAuto     bool
	IsUnsigned bool
	IsVirtual  bool
	IsStored   bool
	EnumValues []string
	SetValues  []string
	FixedSize  uint
	MaxSize    uint
}

type Index struct {
	Name        string
	Columns     []string
	Cardinality []uint64
	NoneUnique  uint64
	Visible     bool
}

type Table struct {
	Schema string
	Name   string

	Columns   []TableColumn
	Indexes   []*Index
	PKColumns []int

	UnsignedColumns []int
}

func (ta *Table) String() string {
	return fmt.Sprintf("%s.%s", ta.Schema, ta.Name)
}

func (ta *Table) AddColumn(name string, columnType string, collation string, extra string) {
	index := len(ta.Columns)
	ta.Columns = append(ta.Columns, TableColumn{Name: name, Collation: collation})
	ta.Columns[index].RawType = columnType

	if strings.HasPrefix(columnType, "float") ||
		strings.HasPrefix(columnType, "double") {
		ta.Columns[index].Type = TYPE_FLOAT
	} else if strings.HasPrefix(columnType, "decimal") {
		ta.Columns[index].Type = TYPE_DECIMAL
	} else if strings.HasPrefix(columnType, "enum") {
		ta.Columns[index].Type = TYPE_ENUM
		ta.Columns[index].EnumValues = strings.Split(strings.ReplaceAll(
			strings.TrimSuffix(
				strings.TrimPrefix(
					columnType, "enum("),
				")"),
			"'", ""),
			",")
	} else if strings.HasPrefix(columnType, "set") {
		ta.Columns[index].Type = TYPE_SET
		ta.Columns[index].SetValues = strings.Split(strings.ReplaceAll(
			strings.TrimSuffix(
				strings.TrimPrefix(
					columnType, "set("),
				")"),
			"'", ""),
			",")
	} else if strings.HasPrefix(columnType, "binary") {
		ta.Columns[index].Type = TYPE_BINARY
		size := getSizeFromColumnType(columnType)
		ta.Columns[index].MaxSize = size
		ta.Columns[index].FixedSize = size
	} else if strings.HasPrefix(columnType, "varbinary") {
		ta.Columns[index].Type = TYPE_BINARY
		ta.Columns[index].MaxSize = getSizeFromColumnType(columnType)
	} else if strings.HasPrefix(columnType, "datetime") {
		ta.Columns[index].Type = TYPE_DATETIME
	} else if strings.HasPrefix(columnType, "timestamp") {
		ta.Columns[index].Type = TYPE_TIMESTAMP
	} else if strings.HasPrefix(columnType, "time") {
		ta.Columns[index].Type = TYPE_TIME
	} else if columnType == "date" {
		ta.Columns[index].Type = TYPE_DATE
	} else if strings.HasPrefix(columnType, "bit") {
		ta.Columns[index].Type = TYPE_BIT
	} else if strings.HasPrefix(columnType, "json") {
		ta.Columns[index].Type = TYPE_JSON
	} else if strings.Contains(columnType, "point") {
		ta.Columns[index].Type = TYPE_POINT
	} else if strings.Contains(columnType, "mediumint") {
		ta.Columns[index].Type = TYPE_MEDIUM_INT
	} else if strings.Contains(columnType, "int") || strings.HasPrefix(columnType, "year") {
		ta.Columns[index].Type = TYPE_NUMBER
	} else if strings.HasPrefix(columnType, "char") {
		ta.Columns[index].Type = TYPE_STRING
		size := getSizeFromColumnType(columnType)
		ta.Columns[index].FixedSize = size
		ta.Columns[index].MaxSize = size
	} else {
		ta.Columns[index].Type = TYPE_STRING
		ta.Columns[index].MaxSize = getSizeFromColumnType(columnType)
	}

	if strings.Contains(columnType, "unsigned") || strings.Contains(columnType, "zerofill") {
		ta.Columns[index].IsUnsigned = true
		ta.UnsignedColumns = append(ta.UnsignedColumns, index)
	}

	switch extra {
	case "auto_increment":
		ta.Columns[index].IsAuto = true
	case "VIRTUAL GENERATED":
		ta.Columns[index].IsVirtual = true
	case "STORED GENERATED":
		ta.Columns[index].IsStored = true
	}
}

func getSizeFromColumnType(columnType string) uint {
	startIndex := strings.Index(columnType, "(")
	if startIndex < 0 {
		return 0
	}

	// we are searching for the first () and there may not be any closing
	// brackets before the opening, so no need search at the offset from the
	// opening ones
	endIndex := strings.Index(columnType, ")")
	if startIndex < 0 || endIndex < 0 || startIndex > endIndex {
		return 0
	}

	i, err := strconv.Atoi(columnType[startIndex+1 : endIndex])
	if err != nil || i < 0 {
		return 0
	}
	return uint(i)
}

func (ta *Table) FindColumn(name string) int {
	for i, col := range ta.Columns {
		if col.Name == name {
			return i
		}
	}
	return -1
}

// Get TableColumn by column index of primary key.
func (ta *Table) GetPKColumn(index int) *TableColumn {
	if index >= len(ta.PKColumns) {
		return nil
	}
	return &ta.Columns[ta.PKColumns[index]]
}

func (ta *Table) IsPrimaryKey(colIndex int) bool {
	for _, i := range ta.PKColumns {
		if i == colIndex {
			return true
		}
	}
	return false
}

func (ta *Table) AddIndex(name string) (index *Index) {
	index = NewIndex(name)
	ta.Indexes = append(ta.Indexes, index)
	return index
}

func NewIndex(name string) *Index {
	return &Index{name, make([]string, 0, 8), make([]uint64, 0, 8), 0, true}
}

func (idx *Index) AddColumn(name string, cardinality uint64) {
	idx.Columns = append(idx.Columns, name)
	if cardinality == 0 {
		cardinality = uint64(len(idx.Cardinality) + 1)
	}
	idx.Cardinality = append(idx.Cardinality, cardinality)
}

func (idx *Index) FindColumn(name string) int {
	for i, colName := range idx.Columns {
		if name == colName {
			return i
		}
	}
	return -1
}

func IsTableExist(conn mysql.Executer, schema string, name string) (bool, error) {
	query := fmt.Sprintf("SELECT * FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = '%s' and TABLE_NAME = '%s' LIMIT 1", schema, name)
	r, err := conn.Execute(query)
	if err != nil {
		return false, errors.Trace(err)
	}

	return r.RowNumber() == 1, nil
}

func NewTableFromSqlDB(conn *sql.DB, schema string, name string) (*Table, error) {
	ta := &Table{
		Schema:  schema,
		Name:    name,
		Columns: make([]TableColumn, 0, 16),
		Indexes: make([]*Index, 0, 8),
	}

	if err := ta.fetchColumnsViaSqlDB(conn); err != nil {
		return nil, errors.Trace(err)
	}

	if err := ta.fetchIndexesViaSqlDB(conn); err != nil {
		return nil, errors.Trace(err)
	}

	return ta, nil
}

func NewTable(conn mysql.Executer, schema string, name string) (*Table, error) {
	ta := &Table{
		Schema:  schema,
		Name:    name,
		Columns: make([]TableColumn, 0, 16),
		Indexes: make([]*Index, 0, 8),
	}

	if err := ta.fetchColumns(conn); err != nil {
		return nil, errors.Trace(err)
	}

	if err := ta.fetchIndexes(conn); err != nil {
		return nil, errors.Trace(err)
	}

	return ta, nil
}

func (ta *Table) fetchColumns(conn mysql.Executer) error {
	r, err := conn.Execute(fmt.Sprintf("show full columns from `%s`.`%s`", ta.Schema, ta.Name))
	if err != nil {
		return errors.Trace(err)
	}

	for i := 0; i < r.RowNumber(); i++ {
		name, _ := r.GetString(i, 0)
		colType, _ := r.GetString(i, 1)
		collation, _ := r.GetString(i, 2)
		extra, _ := r.GetString(i, 6)

		ta.AddColumn(name, colType, collation, extra)
	}

	return nil
}

func (ta *Table) fetchColumnsViaSqlDB(conn *sql.DB) error {
	r, err := conn.Query(fmt.Sprintf("show full columns from `%s`.`%s`", ta.Schema, ta.Name))
	if err != nil {
		return errors.Trace(err)
	}

	defer r.Close()

	var unusedVal interface{}
	unused := &unusedVal

	for r.Next() {
		var name, colType, extra string
		var collation sql.NullString
		err := r.Scan(&name, &colType, &collation, &unused, &unused, &unused, &extra, &unused, &unused)
		if err != nil {
			return errors.Trace(err)
		}
		ta.AddColumn(name, colType, collation.String, extra)
	}

	return r.Err()
}

// hasInvisibleIndexSupportFromResult checks if the result from SHOW INDEX has Visible column
func hasInvisibleIndexSupportFromResult(r *mysql.Result) bool {
	for name := range r.FieldNames {
		if strings.EqualFold(name, "Visible") {
			return true
		}
	}
	return false
}

// hasInvisibleIndexSupportFromColumns checks if the columns from SHOW INDEX include Visible column
func hasInvisibleIndexSupportFromColumns(cols []string) bool {
	for _, col := range cols {
		if strings.EqualFold(col, "Visible") {
			return true
		}
	}
	return false
}

func isIndexInvisible(value string) bool {
	return strings.EqualFold(value, "NO")
}

func (ta *Table) fetchIndexes(conn mysql.Executer) error {
	r, err := conn.Execute(fmt.Sprintf("show index from `%s`.`%s`", ta.Schema, ta.Name))
	if err != nil {
		return errors.Trace(err)
	}
	var currentIndex *Index
	currentName := ""

	hasInvisibleIndex := hasInvisibleIndexSupportFromResult(r)

	for i := 0; i < r.RowNumber(); i++ {
		indexName, _ := r.GetString(i, 2)
		if currentName != indexName {
			currentIndex = ta.AddIndex(indexName)
			currentName = indexName
		}
		cardinality, _ := r.GetUint(i, 6)
		colName, _ := r.GetString(i, 4)
		currentIndex.AddColumn(colName, cardinality)
		currentIndex.NoneUnique, _ = r.GetUint(i, 1)
		if hasInvisibleIndex {
			visible, _ := r.GetString(i, 13)
			currentIndex.Visible = !isIndexInvisible(visible)
		}
	}

	return ta.fetchPrimaryKeyColumns()
}

func (ta *Table) fetchIndexesViaSqlDB(conn *sql.DB) error {
	r, err := conn.Query(fmt.Sprintf("show index from `%s`.`%s`", ta.Schema, ta.Name))
	if err != nil {
		return errors.Trace(err)
	}

	defer r.Close()

	var currentIndex *Index
	currentName := ""

	var unusedVal interface{}

	for r.Next() {
		var indexName string
		var colName sql.NullString
		var noneUnique uint64
		var cardinality interface{}
		var visible sql.NullString
		cols, err := r.Columns()
		if err != nil {
			return errors.Trace(err)
		}
		hasInvisibleIndex := hasInvisibleIndexSupportFromColumns(cols)
		values := make([]interface{}, len(cols))
		for i := 0; i < len(cols); i++ {
			switch i {
			case 1:
				values[i] = &noneUnique
			case 2:
				values[i] = &indexName
			case 4:
				values[i] = &colName
			case 6:
				values[i] = &cardinality
			case 13:
				if hasInvisibleIndex {
					values[i] = &visible
				}
			default:
				values[i] = &unusedVal
			}
		}
		err = r.Scan(values...)
		if err != nil {
			return errors.Trace(err)
		}

		if currentName != indexName {
			currentIndex = ta.AddIndex(indexName)
			currentName = indexName
		}

		c := toUint64(cardinality)
		// If colName is a null string, switch to ""
		if colName.Valid {
			currentIndex.AddColumn(colName.String, c)
		} else {
			currentIndex.AddColumn("", c)
		}
		currentIndex.NoneUnique = noneUnique

		if hasInvisibleIndex && visible.Valid {
			currentIndex.Visible = !isIndexInvisible(visible.String)
		}
	}

	return ta.fetchPrimaryKeyColumns()
}

func toUint64(i interface{}) uint64 {
	switch i := i.(type) {
	case int:
		return uint64(i)
	case int8:
		return uint64(i)
	case int16:
		return uint64(i)
	case int32:
		return uint64(i)
	case int64:
		return uint64(i)
	case uint:
		return uint64(i)
	case uint8:
		return uint64(i)
	case uint16:
		return uint64(i)
	case uint32:
		return uint64(i)
	case uint64:
		return i
	}

	return 0
}

func (ta *Table) fetchPrimaryKeyColumns() error {
	if len(ta.Indexes) == 0 {
		return nil
	}

	// Primary key must be the first index?
	pkIndex := ta.Indexes[0]
	if pkIndex.Name != "PRIMARY" {
		return nil
	}

	ta.PKColumns = make([]int, len(pkIndex.Columns))
	for i, pkCol := range pkIndex.Columns {
		ta.PKColumns[i] = ta.FindColumn(pkCol)
	}

	return nil
}

// GetPKValues gets primary keys in one row for a table, a table may use multi fields as the PK
func (ta *Table) GetPKValues(row []interface{}) ([]interface{}, error) {
	indexes := ta.PKColumns
	if len(indexes) == 0 {
		return nil, errors.Errorf("table %s has no PK", ta)
	} else if len(ta.Columns) != len(row) {
		return nil, errors.Errorf("table %s has %d columns, but row data %v len is %d", ta,
			len(ta.Columns), row, len(row))
	}

	values := make([]interface{}, 0, len(indexes))

	for _, index := range indexes {
		values = append(values, row[index])
	}

	return values, nil
}

// GetColumnValue gets term column's value
func (ta *Table) GetColumnValue(column string, row []interface{}) (interface{}, error) {
	index := ta.FindColumn(column)
	if index == -1 {
		return nil, errors.Errorf("table %s has no column name %s", ta, column)
	}

	return row[index], nil
}
