// Copyright 2019 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package dailytest

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pingcap/errors"
	"github.com/pingcap/log"
	"github.com/pingcap/tidb/parser"
	"github.com/pingcap/tidb/parser/ast"
	"github.com/pingcap/tidb/types"

	// import parser_drive to avoid panic
	_ "github.com/pingcap/tidb/types/parser_driver"
)

type column struct {
	idx     int
	name    string
	data    *datum
	tp      *types.FieldType
	comment string
	min     string
	max     string
	step    int64
	set     []string

	table *table
}

func (col *column) String() string {
	if col == nil {
		return "<nil>"
	}

	return fmt.Sprintf("[column]idx: %d, name: %s, tp: %v, min: %s, max: %s, step: %d, set: %v\n",
		col.idx, col.name, col.tp, col.min, col.max, col.step, col.set)
}

func (col *column) parseRule(kvs []string) {
	if len(kvs) != 2 {
		return
	}

	key := strings.TrimSpace(kvs[0])
	value := strings.TrimSpace(kvs[1])
	if key == "range" {
		fields := strings.Split(value, ",")
		if len(fields) == 1 {
			col.min = strings.TrimSpace(fields[0])
		} else if len(fields) == 2 {
			col.min = strings.TrimSpace(fields[0])
			col.max = strings.TrimSpace(fields[1])
		}
	} else if key == "step" {
		var err error
		col.step, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			log.S().Fatal(err)
		}
	} else if key == "set" {
		fields := strings.Split(value, ",")
		for _, field := range fields {
			col.set = append(col.set, strings.TrimSpace(field))
		}
	}
}

// parse the data rules.
// rules like `a int unique comment '[[range=1,10;step=1]]'`,
// then we will get value from 1,2...10
func (col *column) parseColumnComment() {
	comment := strings.TrimSpace(col.comment)
	start := strings.Index(comment, "[[")
	end := strings.Index(comment, "]]")
	var content string
	if start < end {
		content = comment[start+2 : end]
	}

	fields := strings.Split(content, ";")
	for _, field := range fields {
		field = strings.TrimSpace(field)
		kvs := strings.Split(field, "=")
		col.parseRule(kvs)
	}
}

func (col *column) parseColumn(cd *ast.ColumnDef) {
	col.name = cd.Name.Name.L
	col.tp = cd.Tp
	col.parseColumnOptions(cd.Options)
	col.parseColumnComment()
	col.table.columns = append(col.table.columns, col)
}

func (col *column) parseColumnOptions(ops []*ast.ColumnOption) {
	for _, op := range ops {
		switch op.Tp {
		case ast.ColumnOptionPrimaryKey, ast.ColumnOptionAutoIncrement, ast.ColumnOptionUniqKey:
			col.table.uniqIndices[col.name] = col
		case ast.ColumnOptionComment:
			col.comment = op.Expr.(ast.ValueExpr).GetDatumString()
		}
	}
}

type table struct {
	name         string
	columns      []*column
	columnList   string
	indices      map[string]*column
	uniqIndices  map[string]*column
	unsignedCols map[string]*column
}

func (t *table) printColumns() string {
	ret := ""
	for _, col := range t.columns {
		ret += fmt.Sprintf("%v", col)
	}

	return ret
}

func (t *table) String() string {
	if t == nil {
		return "<nil>"
	}

	ret := fmt.Sprintf("[table]name: %s\n", t.name)
	ret += fmt.Sprintf("[table]columns:\n")
	ret += t.printColumns()

	ret += fmt.Sprintf("[table]column list: %s\n", t.columnList)

	ret += fmt.Sprintf("[table]indices:\n")
	for k, v := range t.indices {
		ret += fmt.Sprintf("key->%s, value->%v", k, v)
	}

	ret += fmt.Sprintf("[table]unique indices:\n")
	for k, v := range t.uniqIndices {
		ret += fmt.Sprintf("key->%s, value->%v", k, v)
	}

	return ret
}

func newTable() *table {
	return &table{
		indices:      make(map[string]*column),
		uniqIndices:  make(map[string]*column),
		unsignedCols: make(map[string]*column),
	}
}

func (t *table) findCol(cols []*column, name string) *column {
	for _, col := range cols {
		if col.name == name {
			return col
		}
	}
	return nil
}

func (t *table) parseTableConstraint(cons *ast.Constraint) {
	switch cons.Tp {
	case ast.ConstraintPrimaryKey, ast.ConstraintUniq,
		ast.ConstraintUniqKey, ast.ConstraintUniqIndex:
		for _, indexCol := range cons.Keys {
			name := indexCol.Column.Name.L
			t.uniqIndices[name] = t.findCol(t.columns, name)
		}
	case ast.ConstraintIndex, ast.ConstraintKey:
		for _, indexCol := range cons.Keys {
			name := indexCol.Column.Name.L
			t.indices[name] = t.findCol(t.columns, name)
		}
	}
}

func (t *table) buildColumnList() {
	columns := make([]string, 0, len(t.columns))
	for _, column := range t.columns {
		columns = append(columns, column.name)
	}

	t.columnList = strings.Join(columns, ",")
}

func parseTable(t *table, stmt *ast.CreateTableStmt) error {
	t.name = stmt.Table.Name.L
	t.columns = make([]*column, 0, len(stmt.Cols))

	for i, col := range stmt.Cols {
		column := &column{idx: i + 1, table: t, step: defaultStep, data: newDatum()}
		column.parseColumn(col)
	}

	for _, cons := range stmt.Constraints {
		t.parseTableConstraint(cons)
	}

	t.buildColumnList()

	return nil
}

func parseTableSQL(table *table, sql string) error {
	stmt, err := parser.New().ParseOneStmt(sql, "", "")
	if err != nil {
		return errors.Trace(err)
	}

	switch node := stmt.(type) {
	case *ast.CreateTableStmt:
		err = parseTable(table, node)
	default:
		err = errors.Errorf("invalid statement - %v", stmt.Text())
	}

	return errors.Trace(err)
}
