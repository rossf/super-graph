package psql

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dosco/super-graph/qcode"
	"github.com/dosco/super-graph/util"
)

type Config struct {
	Schema   *DBSchema
	Vars     map[string]string
	TableMap map[string]string
}

type Compiler struct {
	schema *DBSchema
	vars   map[string]string
	tmap   map[string]string
}

func NewCompiler(conf Config) *Compiler {
	return &Compiler{conf.Schema, conf.Vars, conf.TableMap}
}

func (c *Compiler) Compile(w io.Writer, qc *qcode.QCode) error {
	st := util.NewStack()
	ti, err := c.getTable(qc.Query.Select)
	if err != nil {
		return err
	}

	st.Push(&selectBlockClose{nil, qc.Query.Select})
	st.Push(&selectBlock{nil, qc.Query.Select, ti, c})

	fmt.Fprintf(w, `SELECT json_object_agg('%s', %s) FROM (`,
		qc.Query.Select.FieldName, qc.Query.Select.Table)

	for {
		if st.Len() == 0 {
			break
		}

		intf := st.Pop()

		switch v := intf.(type) {
		case *selectBlock:
			childCols, childIDs := c.relationshipColumns(v.sel)
			v.render(w, c.schema, childCols, childIDs)

			for i := range childIDs {
				sub := v.sel.Joins[childIDs[i]]

				ti, err := c.getTable(sub)
				if err != nil {
					return err
				}

				st.Push(&joinClose{sub})
				st.Push(&selectBlockClose{v.sel, sub})
				st.Push(&selectBlock{v.sel, sub, ti, c})
				st.Push(&joinOpen{sub})
			}
		case *selectBlockClose:
			v.render(w)

		case *joinOpen:
			v.render(w)

		case *joinClose:
			v.render(w)

		}
	}

	io.WriteString(w, `) AS "done_1337";`)

	return nil
}

func (c *Compiler) getTable(sel *qcode.Select) (*DBTableInfo, error) {
	if tn, ok := c.tmap[sel.Table]; ok {
		return c.schema.GetTable(tn)
	}
	return c.schema.GetTable(sel.Table)
}

func (c *Compiler) relationshipColumns(parent *qcode.Select) (
	cols []*qcode.Column, childIDs []int) {

	colmap := make(map[string]struct{}, len(parent.Cols))
	for i := range parent.Cols {
		colmap[parent.Cols[i].Name] = struct{}{}
	}

	for i, sub := range parent.Joins {
		k := TTKey{sub.Table, parent.Table}

		rel, ok := c.schema.RelMap[k]
		if !ok {
			continue
		}

		if rel.Type == RelBelongTo || rel.Type == RelOneToMany {
			if _, ok := colmap[rel.Col2]; !ok {
				cols = append(cols, &qcode.Column{parent.Table, rel.Col2, rel.Col2})
			}
			childIDs = append(childIDs, i)
		}

		if rel.Type == RelOneToManyThrough {
			if _, ok := colmap[rel.Col1]; !ok {
				cols = append(cols, &qcode.Column{parent.Table, rel.Col1, rel.Col1})
			}
			childIDs = append(childIDs, i)
		}
	}

	return cols, childIDs
}

type selectBlock struct {
	parent *qcode.Select
	sel    *qcode.Select
	ti     *DBTableInfo
	*Compiler
}

func (v *selectBlock) render(w io.Writer,
	schema *DBSchema, childCols []*qcode.Column, childIDs []int) error {

	hasOrder := len(v.sel.OrderBy) != 0

	// SELECT
	if v.sel.AsList {
		fmt.Fprintf(w, `SELECT coalesce(json_agg("%s"`, v.sel.Table)

		if hasOrder {
			err := renderOrderBy(w, v.sel)
			if err != nil {
				return err
			}
		}

		fmt.Fprintf(w, `), '[]') AS "%s" FROM (`, v.sel.Table)
	}

	// ROW-TO-JSON
	io.WriteString(w, `SELECT `)

	if len(v.sel.DistinctOn) != 0 {
		v.renderDistinctOn(w)
	}

	io.WriteString(w, `row_to_json((`)

	fmt.Fprintf(w, `SELECT "sel_%d" FROM (SELECT `, v.sel.ID)

	// Combined column names
	v.renderColumns(w)

	err := v.renderJoinedColumns(w, childIDs)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, `) AS "sel_%d"`, v.sel.ID)

	fmt.Fprintf(w, `)) AS "%s"`, v.sel.Table)
	// END-ROW-TO-JSON

	if hasOrder {
		v.renderOrderByColumns(w)
	}
	// END-SELECT

	// FROM (SELECT .... )
	err = v.renderBaseSelect(w, schema, childCols, childIDs)
	if err != nil {
		return err
	}
	// END-FROM

	return nil
}

type selectBlockClose struct {
	parent *qcode.Select
	sel    *qcode.Select
}

func (v *selectBlockClose) render(w io.Writer) error {
	hasOrder := len(v.sel.OrderBy) != 0

	if hasOrder {
		err := renderOrderBy(w, v.sel)
		if err != nil {
			return err
		}
	}

	if len(v.sel.Paging.Limit) != 0 {
		fmt.Fprintf(w, ` LIMIT ('%s') :: integer`, v.sel.Paging.Limit)
	} else {
		io.WriteString(w, ` LIMIT ('20') :: integer`)
	}

	if len(v.sel.Paging.Offset) != 0 {
		fmt.Fprintf(w, ` OFFSET ('%s') :: integer`, v.sel.Paging.Offset)
	}

	if v.sel.AsList {
		fmt.Fprintf(w, `) AS "%s_%d"`, v.sel.Table, v.sel.ID)
	}

	return nil
}

type joinOpen struct {
	sel *qcode.Select
}

func (v joinOpen) render(w io.Writer) error {
	io.WriteString(w, ` LEFT OUTER JOIN LATERAL (`)
	return nil
}

type joinClose struct {
	sel *qcode.Select
}

func (v *joinClose) render(w io.Writer) error {
	fmt.Fprintf(w, `) AS "%s_%d.join" ON ('true')`, v.sel.Table, v.sel.ID)
	return nil
}

func (v *selectBlock) renderJoinTable(w io.Writer, schema *DBSchema, childIDs []int) {
	k := TTKey{v.sel.Table, v.parent.Table}
	rel, ok := schema.RelMap[k]
	if !ok {
		panic(errors.New("no relationship found"))
	}

	if rel.Type != RelOneToManyThrough {
		return
	}

	fmt.Fprintf(w, ` LEFT OUTER JOIN "%s" ON (("%s"."%s") = ("%s_%d"."%s"))`,
		rel.Through, rel.Through, rel.ColT, v.parent.Table, v.parent.ID, rel.Col1)

}

func (v *selectBlock) renderColumns(w io.Writer) {
	for i, col := range v.sel.Cols {
		fmt.Fprintf(w, `"%s_%d"."%s" AS "%s"`,
			v.sel.Table, v.sel.ID, col.Name, col.FieldName)

		if i < len(v.sel.Cols)-1 {
			io.WriteString(w, ", ")
		}
	}
}

func (v *selectBlock) renderJoinedColumns(w io.Writer, childIDs []int) error {
	if len(v.sel.Cols) != 0 && len(childIDs) != 0 {
		io.WriteString(w, ", ")
	}

	for i := range childIDs {
		s := v.sel.Joins[childIDs[i]]

		fmt.Fprintf(w, `"%s_%d.join"."%s" AS "%s"`,
			s.Table, s.ID, s.Table, s.FieldName)

		if i < len(childIDs)-1 {
			io.WriteString(w, ", ")
		}
	}

	return nil
}

func (v *selectBlock) renderBaseSelect(w io.Writer, schema *DBSchema, childCols []*qcode.Column, childIDs []int) error {
	var groupBy []int

	isRoot := v.parent == nil
	isFil := v.sel.Where != nil
	isSearch := v.sel.Args["search"] != nil
	isAgg := false

	io.WriteString(w, " FROM (SELECT ")

	for i, col := range v.sel.Cols {
		cn := col.Name

		_, isRealCol := v.ti.Columns[cn]

		if !isRealCol {
			if isSearch {
				switch {
				case cn == "search_rank":
					cn = v.ti.TSVCol
					arg := v.sel.Args["search"]

					fmt.Fprintf(w, `ts_rank("%s"."%s", to_tsquery('%s')) AS %s`,
						v.sel.Table, cn, arg.Val, col.Name)

				case strings.HasPrefix(cn, "search_headline_"):
					cn = cn[16:]
					arg := v.sel.Args["search"]

					fmt.Fprintf(w, `ts_headline("%s"."%s", to_tsquery('%s')) AS %s`,
						v.sel.Table, cn, arg.Val, col.Name)
				}
			} else {
				pl := funcPrefixLen(cn)
				if pl == 0 {
					fmt.Fprintf(w, `'%s not defined' AS %s`, cn, col.Name)
				} else {
					isAgg = true
					fn := cn[0 : pl-1]
					cn := cn[pl:]
					fmt.Fprintf(w, `%s("%s"."%s") AS %s`, fn, v.sel.Table, cn, col.Name)
				}
			}
		} else {
			groupBy = append(groupBy, i)
			fmt.Fprintf(w, `"%s"."%s"`, v.sel.Table, cn)
		}

		if i < len(v.sel.Cols)-1 || len(childCols) != 0 {
			io.WriteString(w, ", ")
		}
	}

	for i, col := range childCols {
		fmt.Fprintf(w, `"%s"."%s"`, col.Table, col.Name)

		if i < len(childCols)-1 {
			io.WriteString(w, ", ")
		}
	}

	if tn, ok := v.tmap[v.sel.Table]; ok {
		fmt.Fprintf(w, ` FROM "%s" AS "%s"`, tn, v.sel.Table)
	} else {
		fmt.Fprintf(w, ` FROM "%s"`, v.sel.Table)
	}

	if isRoot && isFil {
		io.WriteString(w, ` WHERE (`)
		if err := v.renderWhere(w); err != nil {
			return err
		}
		io.WriteString(w, `)`)
	}

	if !isRoot {
		v.renderJoinTable(w, schema, childIDs)

		io.WriteString(w, ` WHERE (`)
		v.renderRelationship(w, schema)

		if isFil {
			io.WriteString(w, ` AND `)
			if err := v.renderWhere(w); err != nil {
				return err
			}
		}
		io.WriteString(w, `)`)
	}

	if isAgg {
		if len(groupBy) != 0 {
			fmt.Fprintf(w, ` GROUP BY `)

			for i, id := range groupBy {
				fmt.Fprintf(w, `"%s"."%s"`, v.sel.Table, v.sel.Cols[id].Name)

				if i < len(groupBy)-1 {
					io.WriteString(w, ", ")
				}
			}
		}
	}

	if len(v.sel.Paging.Limit) != 0 {
		fmt.Fprintf(w, ` LIMIT ('%s') :: integer`, v.sel.Paging.Limit)
	} else {
		io.WriteString(w, ` LIMIT ('20') :: integer`)
	}

	if len(v.sel.Paging.Offset) != 0 {
		fmt.Fprintf(w, ` OFFSET ('%s') :: integer`, v.sel.Paging.Offset)
	}

	fmt.Fprintf(w, `) AS "%s_%d"`, v.sel.Table, v.sel.ID)
	return nil
}

func (v *selectBlock) renderOrderByColumns(w io.Writer) {
	if len(v.sel.Cols) != 0 {
		io.WriteString(w, ", ")
	}

	for i := range v.sel.OrderBy {
		c := v.sel.OrderBy[i].Col
		fmt.Fprintf(w, `"%s_%d"."%s" AS "%s_%d.ob.%s"`,
			v.sel.Table, v.sel.ID, c,
			v.sel.Table, v.sel.ID, c)

		if i < len(v.sel.OrderBy)-1 {
			io.WriteString(w, ", ")
		}
	}
}

func (v *selectBlock) renderRelationship(w io.Writer, schema *DBSchema) {
	k := TTKey{v.sel.Table, v.parent.Table}
	rel, ok := schema.RelMap[k]
	if !ok {
		panic(errors.New("no relationship found"))
	}

	switch rel.Type {
	case RelBelongTo:
		fmt.Fprintf(w, `(("%s"."%s") = ("%s_%d"."%s"))`,
			v.sel.Table, rel.Col1, v.parent.Table, v.parent.ID, rel.Col2)

	case RelOneToMany:
		fmt.Fprintf(w, `(("%s"."%s") = ("%s_%d"."%s"))`,
			v.sel.Table, rel.Col1, v.parent.Table, v.parent.ID, rel.Col2)

	case RelOneToManyThrough:
		fmt.Fprintf(w, `(("%s"."%s") = ("%s"."%s"))`,
			v.sel.Table, rel.Col1, rel.Through, rel.Col2)
	}
}

func (v *selectBlock) renderWhere(w io.Writer) error {
	st := util.NewStack()

	if v.sel.Where != nil {
		st.Push(v.sel.Where)
	}

	for {
		if st.Len() == 0 {
			break
		}

		intf := st.Pop()

		switch val := intf.(type) {
		case qcode.ExpOp:
			switch val {
			case qcode.OpAnd:
				io.WriteString(w, ` AND `)
			case qcode.OpOr:
				io.WriteString(w, ` OR `)
			case qcode.OpNot:
				io.WriteString(w, `NOT `)
			default:
				return fmt.Errorf("[Where] unexpected value encountered %v", intf)
			}
		case *qcode.Exp:
			switch val.Op {
			case qcode.OpAnd, qcode.OpOr:
				for i := len(val.Children) - 1; i >= 0; i-- {
					st.Push(val.Children[i])
					if i > 0 {
						st.Push(val.Op)
					}
				}
				continue
			case qcode.OpNot:
				st.Push(val.Children[0])
				st.Push(qcode.OpNot)
				continue
			}

			if val.NestedCol {
				fmt.Fprintf(w, `(("%s") `, val.Col)
			} else if len(val.Col) != 0 {
				fmt.Fprintf(w, `(("%s"."%s") `, v.sel.Table, val.Col)
			}
			valExists := true

			switch val.Op {
			case qcode.OpEquals:
				io.WriteString(w, `=`)
			case qcode.OpNotEquals:
				io.WriteString(w, `!=`)
			case qcode.OpGreaterOrEquals:
				io.WriteString(w, `>=`)
			case qcode.OpLesserOrEquals:
				io.WriteString(w, `<=`)
			case qcode.OpGreaterThan:
				io.WriteString(w, `>`)
			case qcode.OpLesserThan:
				io.WriteString(w, `<`)
			case qcode.OpIn:
				io.WriteString(w, `IN`)
			case qcode.OpNotIn:
				io.WriteString(w, `NOT IN`)
			case qcode.OpLike:
				io.WriteString(w, `LIKE`)
			case qcode.OpNotLike:
				io.WriteString(w, `NOT LIKE`)
			case qcode.OpILike:
				io.WriteString(w, `ILIKE`)
			case qcode.OpNotILike:
				io.WriteString(w, `NOT ILIKE`)
			case qcode.OpSimilar:
				io.WriteString(w, `SIMILAR TO`)
			case qcode.OpNotSimilar:
				io.WriteString(w, `NOT SIMILAR TO`)
			case qcode.OpContains:
				io.WriteString(w, `@>`)
			case qcode.OpContainedIn:
				io.WriteString(w, `<@`)
			case qcode.OpHasKey:
				io.WriteString(w, `?`)
			case qcode.OpHasKeyAny:
				io.WriteString(w, `?|`)
			case qcode.OpHasKeyAll:
				io.WriteString(w, `?&`)
			case qcode.OpIsNull:
				if strings.EqualFold(val.Val, "true") {
					io.WriteString(w, `IS NULL)`)
				} else {
					io.WriteString(w, `IS NOT NULL)`)
				}
				valExists = false
			case qcode.OpEqID:
				if len(v.ti.PrimaryCol) == 0 {
					return fmt.Errorf("no primary key column defined for %s", v.sel.Table)
				}
				fmt.Fprintf(w, `(("%s") = ('%s'))`, v.ti.PrimaryCol, val.Val)
				valExists = false
			case qcode.OpTsQuery:
				if len(v.ti.TSVCol) == 0 {
					return fmt.Errorf("no tsv column defined for %s", v.sel.Table)
				}

				fmt.Fprintf(w, `(("%s") @@ to_tsquery('%s'))`, v.ti.TSVCol, val.Val)
				valExists = false

			default:
				return fmt.Errorf("[Where] unexpected op code %d", val.Op)
			}

			if valExists {
				if val.Type == qcode.ValList {
					renderList(w, val)
				} else {
					renderVal(w, val, v.vars)
				}
				io.WriteString(w, `)`)
			}

		default:
			return fmt.Errorf("[Where] unexpected value encountered %v", intf)
		}
	}

	return nil
}

func renderOrderBy(w io.Writer, sel *qcode.Select) error {
	io.WriteString(w, ` ORDER BY `)
	for i := range sel.OrderBy {
		ob := sel.OrderBy[i]

		switch ob.Order {
		case qcode.OrderAsc:
			fmt.Fprintf(w, `"%s_%d.ob.%s" ASC`, sel.Table, sel.ID, ob.Col)
		case qcode.OrderDesc:
			fmt.Fprintf(w, `"%s_%d.ob.%s" DESC`, sel.Table, sel.ID, ob.Col)
		case qcode.OrderAscNullsFirst:
			fmt.Fprintf(w, `"%s_%d.ob.%s" ASC NULLS FIRST`, sel.Table, sel.ID, ob.Col)
		case qcode.OrderDescNullsFirst:
			fmt.Fprintf(w, `%s_%d.ob.%s DESC NULLS FIRST`, sel.Table, sel.ID, ob.Col)
		case qcode.OrderAscNullsLast:
			fmt.Fprintf(w, `"%s_%d.ob.%s ASC NULLS LAST`, sel.Table, sel.ID, ob.Col)
		case qcode.OrderDescNullsLast:
			fmt.Fprintf(w, `%s_%d.ob.%s DESC NULLS LAST`, sel.Table, sel.ID, ob.Col)
		default:
			return fmt.Errorf("[qcode.Order By] unexpected value encountered %v", ob.Order)
		}
		if i < len(sel.OrderBy)-1 {
			io.WriteString(w, ", ")
		}
	}
	return nil
}

func (v selectBlock) renderDistinctOn(w io.Writer) {
	io.WriteString(w, ` DISTINCT ON (`)
	for i := range v.sel.DistinctOn {
		fmt.Fprintf(w, `"%s_%d.ob.%s"`,
			v.sel.Table, v.sel.ID, v.sel.DistinctOn[i])

		if i < len(v.sel.DistinctOn)-1 {
			io.WriteString(w, ", ")
		}
	}
	io.WriteString(w, `) `)
}

func renderList(w io.Writer, ex *qcode.Exp) {
	io.WriteString(w, ` (`)
	for i := range ex.ListVal {
		switch ex.ListType {
		case qcode.ValBool, qcode.ValInt, qcode.ValFloat:
			io.WriteString(w, ex.ListVal[i])
		case qcode.ValStr:
			fmt.Fprintf(w, `'%s'`, ex.ListVal[i])
		}

		if i < len(ex.ListVal)-1 {
			io.WriteString(w, ", ")
		}
	}
	io.WriteString(w, `)`)
}

func renderVal(w io.Writer, ex *qcode.Exp, vars map[string]string) {
	io.WriteString(w, ` (`)
	switch ex.Type {
	case qcode.ValBool, qcode.ValInt, qcode.ValFloat:
		io.WriteString(w, ex.Val)
	case qcode.ValStr:
		fmt.Fprintf(w, `'%s'`, ex.Val)
	case qcode.ValVar:
		if val, ok := vars[ex.Val]; ok {
			io.WriteString(w, val)
		} else {
			fmt.Fprintf(w, `'{{%s}}'`, ex.Val)
		}
	}
	io.WriteString(w, `)`)
}

func funcPrefixLen(fn string) int {
	switch {
	case strings.HasPrefix(fn, "avg_"):
		return 4
	case strings.HasPrefix(fn, "count_"):
		return 6
	case strings.HasPrefix(fn, "max_"):
		return 4
	case strings.HasPrefix(fn, "min_"):
		return 4
	case strings.HasPrefix(fn, "sum_"):
		return 4
	case strings.HasPrefix(fn, "stddev_"):
		return 7
	case strings.HasPrefix(fn, "stddev_pop_"):
		return 11
	case strings.HasPrefix(fn, "stddev_samp_"):
		return 12
	case strings.HasPrefix(fn, "variance_"):
		return 9
	case strings.HasPrefix(fn, "var_pop_"):
		return 8
	case strings.HasPrefix(fn, "var_samp_"):
		return 9
	}
	return 0
}
