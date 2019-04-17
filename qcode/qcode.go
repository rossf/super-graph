package qcode

import (
	"fmt"
	"strings"

	"github.com/dosco/super-graph/util"
	"github.com/gobuffalo/flect"
)

type QCode struct {
	Query *Query
}

type Query struct {
	Select *Select
}

type Column struct {
	Table     string
	Name      string
	FieldName string
}

type Select struct {
	ID         int16
	Args       map[string]*Node
	AsList     bool
	Table      string
	Singular   string
	FieldName  string
	Cols       []*Column
	Where      *Exp
	OrderBy    []*OrderBy
	DistinctOn []string
	Paging     Paging
	Joins      []*Select
}

type Exp struct {
	Op        ExpOp
	Col       string
	NestedCol bool
	Type      ValType
	Val       string
	ListType  ValType
	ListVal   []string
	Children  []*Exp
}

type OrderBy struct {
	Col   string
	Order Order
}

type Paging struct {
	Limit  string
	Offset string
}

type ExpOp int

const (
	OpNop ExpOp = iota
	OpAnd
	OpOr
	OpNot
	OpEquals
	OpNotEquals
	OpGreaterOrEquals
	OpLesserOrEquals
	OpGreaterThan
	OpLesserThan
	OpIn
	OpNotIn
	OpLike
	OpNotLike
	OpILike
	OpNotILike
	OpSimilar
	OpNotSimilar
	OpContains
	OpContainedIn
	OpHasKey
	OpHasKeyAny
	OpHasKeyAll
	OpIsNull
	OpEqID
	OpTsQuery
)

func (t ExpOp) String() string {
	var v string

	switch t {
	case OpNop:
		v = "op-nop"
	case OpAnd:
		v = "op-and"
	case OpOr:
		v = "op-or"
	case OpNot:
		v = "op-not"
	case OpEquals:
		v = "op-equals"
	case OpNotEquals:
		v = "op-not-equals"
	case OpGreaterOrEquals:
		v = "op-greater-or-equals"
	case OpLesserOrEquals:
		v = "op-lesser-or-equals"
	case OpGreaterThan:
		v = "op-greater-than"
	case OpLesserThan:
		v = "op-lesser-than"
	case OpIn:
		v = "op-in"
	case OpNotIn:
		v = "op-not-in"
	case OpLike:
		v = "op-like"
	case OpNotLike:
		v = "op-not-like"
	case OpILike:
		v = "op-i-like"
	case OpNotILike:
		v = "op-not-i-like"
	case OpSimilar:
		v = "op-similar"
	case OpNotSimilar:
		v = "op-not-similar"
	case OpContains:
		v = "op-contains"
	case OpContainedIn:
		v = "op-contained-in"
	case OpHasKey:
		v = "op-has-key"
	case OpHasKeyAny:
		v = "op-has-key-any"
	case OpHasKeyAll:
		v = "op-has-key-all"
	case OpIsNull:
		v = "op-is-null"
	case OpEqID:
		v = "op-eq-id"
	case OpTsQuery:
		v = "op-ts-query"
	}
	return fmt.Sprintf("<%s>", v)
}

type ValType int

const (
	ValStr ValType = iota + 1
	ValInt
	ValFloat
	ValBool
	ValList
	ValVar
	ValNone
)

type AggregrateOp int

const (
	AgCount AggregrateOp = iota + 1
	AgSum
	AgAvg
	AgMax
	AgMin
)

type Order int

const (
	OrderAsc Order = iota + 1
	OrderDesc
	OrderAscNullsFirst
	OrderAscNullsLast
	OrderDescNullsFirst
	OrderDescNullsLast
)

type Config struct {
	Filter    []string
	FilterMap map[string][]string
	Blacklist []string
}

type Compiler struct {
	fl *Exp
	fm map[string]*Exp
	bl map[string]struct{}
}

func NewCompiler(conf Config) (*Compiler, error) {
	bl := make(map[string]struct{}, len(conf.Blacklist))

	for i := range conf.Blacklist {
		bl[strings.ToLower(conf.Blacklist[i])] = struct{}{}
	}

	fl, err := compileFilter(conf.Filter)
	if err != nil {
		return nil, err
	}

	fm := make(map[string]*Exp, len(conf.FilterMap))

	for k, v := range conf.FilterMap {
		fil, err := compileFilter(v)
		if err != nil {
			return nil, err
		}
		fm[strings.ToLower(k)] = fil
	}

	return &Compiler{fl, fm, bl}, nil
}

func (com *Compiler) CompileQuery(query string) (*QCode, error) {
	var qc QCode
	var err error

	op, err := ParseQuery(query)
	if err != nil {
		return nil, err
	}

	switch op.Type {
	case opQuery:
		qc.Query, err = com.compileQuery(op)
	case opMutate:
	case opSub:
	default:
		err = fmt.Errorf("Unknown operation type %d", op.Type)
	}

	if err != nil {
		return nil, err
	}

	return &qc, nil
}

func (com *Compiler) compileQuery(op *Operation) (*Query, error) {
	var selRoot *Select

	st := util.NewStack()
	id := int16(0)
	fs := make([]*Select, op.FieldLen)

	for i := range op.Fields {
		st.Push(op.Fields[i])
	}

	for {
		if st.Len() == 0 {
			break
		}

		intf := st.Pop()
		field, ok := intf.(*Field)

		if !ok || field == nil {
			return nil, fmt.Errorf("unexpected value poped out %v", intf)
		}

		fn := strings.ToLower(field.Name)
		if _, ok := com.bl[fn]; ok {
			continue
		}
		tn := flect.Pluralize(fn)

		s := &Select{
			ID:    id,
			Table: tn,
		}

		if fn == tn {
			s.Singular = flect.Singularize(fn)
		} else {
			s.Singular = fn
		}

		if fn == s.Table {
			s.AsList = true
		} else {
			s.Paging.Limit = "1"
		}

		if len(field.Alias) != 0 {
			s.FieldName = field.Alias
		} else if s.AsList {
			s.FieldName = s.Table
		} else {
			s.FieldName = s.Singular
		}

		id++
		fs[field.ID] = s

		err := com.compileArgs(s, field.Args)
		if err != nil {
			return nil, err
		}

		for i := range field.Children {
			f := field.Children[i]
			fn := strings.ToLower(f.Name)

			if _, ok := com.bl[fn]; ok {
				continue
			}

			if f.Children == nil {
				col := &Column{Name: fn}
				if len(f.Alias) != 0 {
					col.FieldName = f.Alias
				} else {
					col.FieldName = f.Name
				}

				s.Cols = append(s.Cols, col)
			} else {
				st.Push(f)
			}
		}

		if field.Parent == nil {
			selRoot = s
		} else {
			sp := fs[field.Parent.ID]
			sp.Joins = append(sp.Joins, s)
		}
	}

	fil, ok := com.fm[selRoot.Table]
	if !ok {
		fil = com.fl
	}

	if fil != nil && fil.Op != OpNop {
		if selRoot.Where != nil {
			selRoot.Where = &Exp{Op: OpAnd, Children: []*Exp{fil, selRoot.Where}}
		} else {
			selRoot.Where = fil
		}
	}

	return &Query{selRoot}, nil
}

func (com *Compiler) compileArgs(sel *Select, args []*Arg) error {
	var err error

	sel.Args = make(map[string]*Node, len(args))

	for i := range args {
		if args[i] == nil {
			return fmt.Errorf("[Args] unexpected nil argument found")
		}
		an := strings.ToLower(args[i].Name)
		if _, ok := sel.Args[an]; ok {
			continue
		}

		switch an {
		case "id":
			if sel.ID == int16(0) {
				err = com.compileArgID(sel, args[i])
			}
		case "search":
			err = com.compileArgSearch(sel, args[i])
		case "where":
			err = com.compileArgWhere(sel, args[i])
		case "orderby", "order_by", "order":
			err = com.compileArgOrderBy(sel, args[i])
		case "distinct_on", "distinct":
			err = com.compileArgDistinctOn(sel, args[i])
		case "limit":
			err = com.compileArgLimit(sel, args[i])
		case "offset":
			err = com.compileArgOffset(sel, args[i])
		}

		if err != nil {
			return err
		}

		sel.Args[an] = args[i].Val
	}

	return nil
}

type expT struct {
	parent *Exp
	node   *Node
}

func (com *Compiler) compileArgObj(arg *Arg) (*Exp, error) {
	if arg.Val.Type != nodeObj {
		return nil, fmt.Errorf("expecting an object")
	}

	return com.compileArgNode(arg.Val)
}

func (com *Compiler) compileArgNode(val *Node) (*Exp, error) {
	st := util.NewStack()
	var root *Exp

	st.Push(&expT{nil, val.Children[0]})

	for {
		if st.Len() == 0 {
			break
		}

		intf := st.Pop()
		eT, ok := intf.(*expT)
		if !ok || eT == nil {
			return nil, fmt.Errorf("unexpected value poped out %v", intf)
		}

		if len(eT.node.Name) != 0 {
			if _, ok := com.bl[strings.ToLower(eT.node.Name)]; ok {
				continue
			}
		}

		ex, err := newExp(st, eT)

		if err != nil {
			return nil, err
		}

		if ex == nil {
			continue
		}

		if eT.parent == nil {
			root = ex
		} else {
			eT.parent.Children = append(eT.parent.Children, ex)
		}
	}

	return root, nil
}

func (com *Compiler) compileArgID(sel *Select, arg *Arg) error {
	if sel.Where != nil && sel.Where.Op == OpEqID {
		return nil
	}

	ex := &Exp{Op: OpEqID, Val: arg.Val.Val}

	switch arg.Val.Type {
	case nodeStr:
		ex.Type = ValStr
	case nodeInt:
		ex.Type = ValInt
	case nodeFloat:
		ex.Type = ValFloat
	default:
		fmt.Errorf("expecting an string, int or float")
	}

	sel.Where = ex
	return nil
}

func (com *Compiler) compileArgSearch(sel *Select, arg *Arg) error {
	ex := &Exp{
		Op:   OpTsQuery,
		Type: ValStr,
		Val:  arg.Val.Val,
	}

	if sel.Where != nil {
		sel.Where = &Exp{Op: OpAnd, Children: []*Exp{ex, sel.Where}}
	} else {
		sel.Where = ex
	}
	return nil
}

func (com *Compiler) compileArgWhere(sel *Select, arg *Arg) error {
	var err error

	ex, err := com.compileArgObj(arg)
	if err != nil {
		return err
	}

	if sel.Where != nil {
		sel.Where = &Exp{Op: OpAnd, Children: []*Exp{ex, sel.Where}}
	} else {
		sel.Where = ex
	}

	return nil
}

func (com *Compiler) compileArgOrderBy(sel *Select, arg *Arg) error {
	if arg.Val.Type != nodeObj {
		return fmt.Errorf("expecting an object")
	}

	st := util.NewStack()

	for i := range arg.Val.Children {
		st.Push(arg.Val.Children[i])
	}

	for {
		if st.Len() == 0 {
			break
		}

		intf := st.Pop()
		node, ok := intf.(*Node)

		if !ok || node == nil {
			return fmt.Errorf("OrderBy: unexpected value poped out %v", intf)
		}

		if _, ok := com.bl[strings.ToLower(node.Name)]; ok {
			continue
		}

		if node.Type == nodeObj {
			for i := range node.Children {
				st.Push(node.Children[i])
			}
			continue
		}

		ob := &OrderBy{}

		val := strings.ToLower(node.Val)
		switch val {
		case "asc":
			ob.Order = OrderAsc
		case "desc":
			ob.Order = OrderDesc
		case "asc_nulls_first":
			ob.Order = OrderAscNullsFirst
		case "desc_nulls_first":
			ob.Order = OrderDescNullsFirst
		case "asc_nulls_last":
			ob.Order = OrderAscNullsLast
		case "desc_nulls_last":
			ob.Order = OrderDescNullsLast
		default:
			return fmt.Errorf("valid values include asc, desc, asc_nulls_first and desc_nulls_first")
		}

		setOrderByColName(ob, node)
		sel.OrderBy = append(sel.OrderBy, ob)
	}
	return nil
}

func (com *Compiler) compileArgDistinctOn(sel *Select, arg *Arg) error {
	node := arg.Val

	if _, ok := com.bl[strings.ToLower(node.Name)]; ok {
		return nil
	}

	if node.Type != nodeList && node.Type != nodeStr {
		return fmt.Errorf("expecting a list of strings or just a string")
	}

	if node.Type == nodeStr {
		sel.DistinctOn = append(sel.DistinctOn, node.Val)
	}

	for i := range node.Children {
		sel.DistinctOn = append(sel.DistinctOn, node.Children[i].Val)
	}

	return nil
}

func (com *Compiler) compileArgLimit(sel *Select, arg *Arg) error {
	node := arg.Val

	if node.Type != nodeInt {
		return fmt.Errorf("expecting an integer")
	}

	sel.Paging.Limit = node.Val

	return nil
}

func (com *Compiler) compileArgOffset(sel *Select, arg *Arg) error {
	node := arg.Val

	if node.Type != nodeInt {
		return fmt.Errorf("expecting an integer")
	}

	sel.Paging.Offset = node.Val

	return nil
}

func compileMutate() (*Query, error) {
	return nil, nil
}

func compileSub() (*Query, error) {
	return nil, nil
}

func newExp(st *util.Stack, eT *expT) (*Exp, error) {
	ex := &Exp{}
	node := eT.node

	if len(node.Name) == 0 {
		pushChildren(st, eT.parent, node)
		return nil, nil
	}

	name := strings.ToLower(node.Name)
	if name[0] == '_' {
		name = name[1:]
	}

	switch name {
	case "and":
		ex.Op = OpAnd
		pushChildren(st, ex, node)
	case "or":
		ex.Op = OpOr
		pushChildren(st, ex, node)
	case "not":
		ex.Op = OpNot
		st.Push(&expT{ex, node.Children[0]})
	case "eq", "equals":
		ex.Op = OpEquals
		ex.Val = node.Val
	case "neq", "not_equals":
		ex.Op = OpNotEquals
		ex.Val = node.Val
	case "gt", "greater_than":
		ex.Op = OpGreaterThan
		ex.Val = node.Val
	case "lt", "lesser_than":
		ex.Op = OpLesserThan
		ex.Val = node.Val
	case "gte", "greater_or_equals":
		ex.Op = OpGreaterOrEquals
		ex.Val = node.Val
	case "lte", "lesser_or_equals":
		ex.Op = OpLesserOrEquals
		ex.Val = node.Val
	case "in":
		ex.Op = OpIn
		setListVal(ex, node)
	case "nin", "not_in":
		ex.Op = OpNotIn
		setListVal(ex, node)
	case "like":
		ex.Op = OpLike
		ex.Val = node.Val
	case "nlike", "not_like":
		ex.Op = OpNotLike
		ex.Val = node.Val
	case "ilike":
		ex.Op = OpILike
		ex.Val = node.Val
	case "nilike", "not_ilike":
		ex.Op = OpILike
		ex.Val = node.Val
	case "similar":
		ex.Op = OpSimilar
		ex.Val = node.Val
	case "nsimilar", "not_similar":
		ex.Op = OpNotSimilar
		ex.Val = node.Val
	case "contains":
		ex.Op = OpContains
		ex.Val = node.Val
	case "contained_in":
		ex.Op = OpContainedIn
		ex.Val = node.Val
	case "has_key":
		ex.Op = OpHasKey
		ex.Val = node.Val
	case "has_key_any":
		ex.Op = OpHasKeyAny
		ex.Val = node.Val
	case "has_key_all":
		ex.Op = OpHasKeyAll
		ex.Val = node.Val
	case "is_null":
		ex.Op = OpIsNull
		ex.Val = node.Val
	default:
		pushChildren(st, eT.parent, node)
		return nil, nil // skip node
	}

	if ex.Op != OpAnd && ex.Op != OpOr && ex.Op != OpNot {
		switch node.Type {
		case nodeStr:
			ex.Type = ValStr
		case nodeInt:
			ex.Type = ValInt
		case nodeBool:
			ex.Type = ValBool
		case nodeFloat:
			ex.Type = ValFloat
		case nodeList:
			ex.Type = ValList
		case nodeVar:
			ex.Type = ValVar
		default:
			return nil, fmt.Errorf("[Where] valid values include string, int, float, boolean and list: %s", node.Type)
		}
		setWhereColName(ex, node)
	}

	return ex, nil
}

func setListVal(ex *Exp, node *Node) {
	if len(node.Children) != 0 {
		switch node.Children[0].Type {
		case nodeStr:
			ex.ListType = ValStr
		case nodeInt:
			ex.ListType = ValInt
		case nodeBool:
			ex.ListType = ValBool
		case nodeFloat:
			ex.ListType = ValFloat
		}
	}
	for i := range node.Children {
		ex.ListVal = append(ex.ListVal, node.Children[i].Val)
	}
}

func setWhereColName(ex *Exp, node *Node) {
	var list []string
	for n := node.Parent; n != nil; n = n.Parent {
		if n.Type != nodeObj {
			continue
		}
		k := strings.ToLower(n.Name)
		if k == "and" || k == "or" || k == "not" ||
			k == "_and" || k == "_or" || k == "_not" {
			continue
		}
		if len(k) != 0 {
			list = append([]string{k}, list...)
		}
	}
	if len(list) == 1 {
		ex.Col = list[0]

	} else if len(list) > 2 {
		ex.Col = strings.Join(list, ".")
		ex.NestedCol = true
	}
}

func setOrderByColName(ob *OrderBy, node *Node) {
	var list []string
	for n := node; n != nil; n = n.Parent {
		k := strings.ToLower(n.Name)
		if len(k) != 0 {
			list = append([]string{k}, list...)
		}
	}
	if len(list) != 0 {
		ob.Col = strings.Join(list, ".")
	}
}

func pushChildren(st *util.Stack, ex *Exp, node *Node) {
	for i := range node.Children {
		st.Push(&expT{ex, node.Children[i]})
	}
}

func compileFilter(filter []string) (*Exp, error) {
	var fl *Exp
	com := &Compiler{}

	if len(filter) == 0 {
		return &Exp{Op: OpNop}, nil
	}

	for i := range filter {
		node, err := ParseArgValue(filter[i])
		if err != nil {
			return nil, err
		}
		f, err := com.compileArgNode(node)
		if err != nil {
			return nil, err
		}
		if fl == nil {
			fl = f
		} else {
			fl = &Exp{Op: OpAnd, Children: []*Exp{fl, f}}
		}
	}
	return fl, nil
}
