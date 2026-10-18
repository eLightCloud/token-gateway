package seedanceenhance

import (
	"errors"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// ValidatePriceExpression enforces the enhancement's seconds-only contract on
// the original AST, including branches removed by compiler optimization.
func ValidatePriceExpression(expression string) error {
	if _, err := billingexpr.CompileFromCache(expression); err != nil {
		return err
	}
	_, body := billingexpr.ParseExprVersion(expression)
	tree, err := parser.Parse(body)
	if err != nil {
		return err
	}
	invalid := ast.Find(tree.Node, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallNode); ok {
			if callee, ok := call.Callee.(*ast.IdentifierNode); ok && callee.Value == "u" {
				if len(call.Arguments) != 1 {
					return true
				}
				key, ok := call.Arguments[0].(*ast.StringNode)
				if !ok {
					return true
				}
				switch key.Value {
				case "seconds", "output_resolution", "fps_tier", "video_input":
					return false
				default:
					return true
				}
			}
		}
		identifier, ok := node.(*ast.IdentifierNode)
		if !ok {
			return false
		}
		switch identifier.Value {
		case "p", "c", "len", "cr", "cc", "cc1h", "img", "img_cr", "img_o", "ai", "ao", "image_count", "fixed", "param", "header", "hour", "minute", "weekday", "month", "day":
			return true
		}
		return false
	})
	if invalid != nil {
		return errors.New("enhanced video prices must use final video seconds and specifications only")
	}
	if !billingexpr.UsedUsageKeys(expression)["seconds"] && !zeroVideoPrice(tree.Node) {
		return errors.New("enhanced video prices must use seconds; an explicit zero price is allowed")
	}
	return nil
}

func zeroVideoPrice(node ast.Node) bool {
	switch value := node.(type) {
	case *ast.IntegerNode:
		return value.Value == 0
	case *ast.FloatNode:
		return value.Value == 0
	case *ast.CallNode:
		identifier, ok := value.Callee.(*ast.IdentifierNode)
		return ok && identifier.Value == "tier" && len(value.Arguments) == 2 && zeroVideoPrice(value.Arguments[1])
	case *ast.ConditionalNode:
		return zeroVideoPrice(value.Exp1) && zeroVideoPrice(value.Exp2)
	}
	return false
}
