// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package render

import (
	"fmt"
	"text/template/parse"
)

// allowedFuncs — закрытый перечень функций шаблона: свои и разрешённые
// встроенные text/template. Встроенные из FuncMap не убрать, поэтому
// запрет держит гейт: идентификатор вне перечня — отказ разбора. env,
// expandenv, getHostByName, include и прочие функции sprig сюда не входят
// и не входят никогда: их нет в FuncMap, и гейт отвергает имя до исполнения.
var allowedFuncs = map[string]bool{
	// свои
	"toJson": true, "toYaml": true, "nindent": true,
	// встроенные: логика и сравнение для if, доступ по ключу, сборка строки
	"and": true, "or": true, "not": true,
	"eq": true, "ne": true, "lt": true, "le": true, "gt": true, "ge": true,
	"len": true, "index": true, "printf": true,
}

// cleanRoots — поля корня контекста, которые пишет платформа: их значение
// вправе печататься через toYaml | nindent. Прочие (.Registry, .ID, .Login,
// .Status) несут данные арендатора и чужих контроллеров и печатаются только
// через toJson.
var cleanRoots = map[string]bool{"Params": true, "Template": true}

// GateError — отказ AST-гейта с указанием места.
type GateError struct {
	Pos    parse.Pos
	Reason string
}

func (e *GateError) Error() string {
	return fmt.Sprintf("template gate: offset %d: %s", e.Pos, e.Reason)
}

// scope — что обозначает точка в текущем месте дерева: корень контекста
// либо значение из .Params (внутри with/range по полю платформы).
type scope struct{ dotIsParams bool }

// gate проходит дерево разбора и отвергает всё вне закрытой формы.
func gate(t *parse.Tree) error {
	if t == nil || t.Root == nil {
		return &GateError{Reason: "empty template"}
	}
	return walk(t.Root, scope{})
}

func walk(n parse.Node, s scope) error {
	switch n := n.(type) {
	case nil:
		return nil
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Nodes {
			if err := walk(c, s); err != nil {
				return err
			}
		}
		return nil
	case *parse.TextNode, *parse.CommentNode, *parse.BreakNode, *parse.ContinueNode:
		return nil
	case *parse.ActionNode:
		if err := checkPipe(n.Pipe, n.Position()); err != nil {
			return err
		}
		return checkPrinting(n.Pipe, n.Position(), s)
	case *parse.IfNode:
		// if точку не переопределяет: условие вправе читать поля заказа.
		return walkBranch(&n.BranchNode, s, s)
	case *parse.WithNode:
		inner, err := rebind(n.Pipe, n.Position(), s, "with")
		if err != nil {
			return err
		}
		return walkBranch(&n.BranchNode, inner, s)
	case *parse.RangeNode:
		inner, err := rebind(n.Pipe, n.Position(), s, "range")
		if err != nil {
			return err
		}
		return walkBranch(&n.BranchNode, inner, s)
	case *parse.TemplateNode:
		return &GateError{Pos: n.Position(), Reason: "template/block calls are forbidden"}
	default:
		return &GateError{Pos: n.Position(), Reason: fmt.Sprintf("node %T is forbidden", n)}
	}
}

// walkBranch: тело — в области body, ветка else — во внешней области (там
// точка не переопределена).
func walkBranch(b *parse.BranchNode, body, outer scope) error {
	if err := checkPipe(b.Pipe, b.Position()); err != nil {
		return err
	}
	if err := walk(b.List, body); err != nil {
		return err
	}
	return walk(b.ElseList, outer)
}

// rebind: with и range переопределяют точку и потому законны только над
// одним полем платформы (.Params…, $.Params… либо полем текущего значения
// платформы). Перебор числа, результата функции или поля заказа отвергнут:
// цикл не ограничен данными, а точка внутри несла бы данные арендатора.
func rebind(p *parse.PipeNode, pos parse.Pos, s scope, what string) (scope, error) {
	if err := checkPipe(p, pos); err != nil {
		return s, err
	}
	if len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 1 {
		return s, &GateError{Pos: pos, Reason: what + " must take a single platform field"}
	}
	if tainted(p.Cmds[0].Args[0], s) {
		return s, &GateError{Pos: pos, Reason: what + " over order or status data is forbidden; use .Params"}
	}
	switch p.Cmds[0].Args[0].(type) {
	case *parse.FieldNode, *parse.VariableNode:
		return scope{dotIsParams: true}, nil
	case *parse.DotNode:
		if s.dotIsParams {
			return s, nil
		}
	}
	return s, &GateError{Pos: pos, Reason: what + " must take a single platform field"}
}

// checkPipe: без объявлений и присваиваний переменных, только разрешённые
// функции, без цепочек и вызовов методов из данных.
func checkPipe(p *parse.PipeNode, pos parse.Pos) error {
	if p == nil {
		return &GateError{Pos: pos, Reason: "empty pipeline"}
	}
	if len(p.Decl) > 0 || p.IsAssign {
		return &GateError{Pos: pos, Reason: "variable declarations and assignments are forbidden"}
	}
	for _, c := range p.Cmds {
		for _, a := range c.Args {
			if err := checkArg(a); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkArg(a parse.Node) error {
	switch a := a.(type) {
	case *parse.IdentifierNode:
		if !allowedFuncs[a.Ident] {
			return &GateError{Pos: a.Position(), Reason: fmt.Sprintf("function %q is not allowed", a.Ident)}
		}
		return nil
	case *parse.FieldNode, *parse.DotNode, *parse.StringNode, *parse.NumberNode, *parse.BoolNode, *parse.NilNode:
		return nil
	case *parse.VariableNode:
		// Объявлять переменные нельзя, значит, законна только корневая $.
		if len(a.Ident) == 0 || a.Ident[0] != "$" {
			return &GateError{Pos: a.Position(), Reason: "variables other than $ are forbidden"}
		}
		return nil
	case *parse.PipeNode:
		return checkPipe(a, a.Position())
	case *parse.ChainNode:
		return &GateError{Pos: a.Position(), Reason: "chained field access on an expression is forbidden"}
	default:
		return &GateError{Pos: a.Position(), Reason: fmt.Sprintf("argument %T is forbidden", a)}
	}
}

// checkPrinting: печатающее действие кончается на toJson; на toYaml |
// nindent — только если в нём нет ни одного поля заказа или статуса.
func checkPrinting(p *parse.PipeNode, pos parse.Pos, s scope) error {
	n := len(p.Cmds)
	last := firstIdent(p.Cmds[n-1])
	if last == "toJson" {
		return nil
	}
	if last == "nindent" && n >= 2 && firstIdent(p.Cmds[n-2]) == "toYaml" {
		if pipeTainted(p, s) {
			return &GateError{Pos: pos, Reason: "order and status fields are printed only through toJson"}
		}
		return nil
	}
	return &GateError{Pos: pos, Reason: "every printing action must end with toJson, or with toYaml | nindent for .Params"}
}

func pipeTainted(p *parse.PipeNode, s scope) bool {
	for _, c := range p.Cmds {
		for _, a := range c.Args {
			if tainted(a, s) {
				return true
			}
		}
	}
	return false
}

// tainted — несёт ли узел данные заказа или статусы дочерних ресурсов.
func tainted(a parse.Node, s scope) bool {
	switch a := a.(type) {
	case *parse.FieldNode:
		if s.dotIsParams {
			return false
		}
		return !cleanRoots[a.Ident[0]]
	case *parse.DotNode:
		return !s.dotIsParams
	case *parse.VariableNode:
		// $ — всегда корень контекста.
		return len(a.Ident) < 2 || !cleanRoots[a.Ident[1]]
	case *parse.PipeNode:
		return pipeTainted(a, s)
	}
	return false
}

func firstIdent(c *parse.CommandNode) string {
	if len(c.Args) == 0 {
		return ""
	}
	if id, ok := c.Args[0].(*parse.IdentifierNode); ok {
		return id.Ident
	}
	return ""
}
