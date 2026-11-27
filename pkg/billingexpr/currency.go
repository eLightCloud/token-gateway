package billingexpr

import (
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

const cnyToUSD = "cny_to_usd"

var compileEnvPrototypeV2 = func() map[string]any {
	env := maps.Clone(compileEnvPrototypeV1)
	env[cnyToUSD] = func(float64) float64 { return 0 }
	return env
}()

func expressionVersion(expression string) (int, string) {
	tag, body, found := strings.Cut(expression, ":")
	if !found || len(tag) < 2 || tag[0] != 'v' {
		return DefaultExprVersion, expression
	}
	for _, digit := range tag[1:] {
		if digit < '0' || digit > '9' {
			return DefaultExprVersion, expression
		}
	}
	version, err := strconv.Atoi(tag[1:])
	if err != nil || tag[1:] != strconv.Itoa(version) {
		return 0, body
	}
	return version, body
}

type currencySyntax struct {
	calls      int
	references int
	err        error
	prices     []cnyPriceLiteral
}

func (syntax *currencySyntax) Visit(node *ast.Node) {
	switch part := (*node).(type) {
	case *ast.IdentifierNode:
		if part.Value == "$env" {
			syntax.err = fmt.Errorf("v2 billing expressions must reference environment values directly")
		}
		if part.Value == cnyToUSD {
			syntax.references++
		}
	case *ast.VariableDeclaratorNode:
		if part.Name == cnyToUSD {
			syntax.err = fmt.Errorf("%s is a reserved billing function", cnyToUSD)
		}
	}
	call, ok := (*node).(*ast.CallNode)
	if !ok {
		return
	}
	callee, ok := call.Callee.(*ast.IdentifierNode)
	if !ok {
		return
	}
	if callee.Value == "fixed" && len(call.Arguments) == 1 && isCNYPrice(call.Arguments[0]) {
		conversion := call.Arguments[0].(*ast.CallNode)
		amount, _ := requestRuleNumber(conversion.Arguments[0])
		syntax.prices = append(syntax.prices, cnyPriceLiteral{amount: amount, fixed: true})
	}
	if callee.Value != cnyToUSD {
		return
	}
	syntax.calls++
	if !isCNYPrice(call) {
		syntax.err = fmt.Errorf("%s requires one finite, non-negative numeric literal", cnyToUSD)
		return
	}
	amount, _ := requestRuleNumber(call.Arguments[0])
	syntax.prices = append(syntax.prices, cnyPriceLiteral{amount: amount})
}

func isCNYPrice(node ast.Node) bool {
	call, ok := node.(*ast.CallNode)
	if !ok || len(call.Arguments) != 1 {
		return false
	}
	callee, ok := call.Callee.(*ast.IdentifierNode)
	if !ok || callee.Value != cnyToUSD {
		return false
	}
	amount, literal := requestRuleNumber(call.Arguments[0])
	return literal && amount >= 0 && !math.IsInf(amount, 0)
}

// UsesCNYExchangeRate includes calls in branches omitted by optimization.
func UsesCNYExchangeRate(expression string) bool {
	entry, err := compileEntryFromCacheByHash(expression, ExprHashString(expression))
	return err == nil && len(entry.cnyPrices) > 0
}

type cnyPriceLiteral struct {
	amount float64
	fixed  bool
}

func validateCNYPrices(prices []cnyPriceLiteral, rate float64) error {
	if len(prices) == 0 {
		return nil
	}
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return fmt.Errorf("CNY exchange rate must be finite and positive")
	}
	conversion := currencyConversion{rate: rate}
	for _, price := range prices {
		usd := conversion.toUSD(price.amount)
		if conversion.err != nil {
			return conversion.err
		}
		if price.fixed && !validFixedUSDPrice(usd) {
			return fmt.Errorf("fixed USD price must have a finite scaled value")
		}
	}
	return nil
}

func validFixedUSDPrice(amount float64) bool {
	return amount >= 0 && !math.IsInf(amount*1_000_000, 0)
}

// ValidateExpressionCNYPrices checks every converted price without selecting a tier.
func ValidateExpressionCNYPrices(expression string, rate float64) error {
	entry, err := compileEntryFromCacheByHash(expression, ExprHashString(expression))
	if err != nil {
		return err
	}
	return validateCNYPrices(entry.cnyPrices, rate)
}

type currencyConversion struct {
	rate float64
	err  error
}

func (conversion *currencyConversion) toUSD(amount float64) float64 {
	usd := amount / conversion.rate
	if math.IsInf(usd, 0) || math.IsNaN(usd) || usd < 0 {
		conversion.err = fmt.Errorf("CNY price must convert to a finite, non-negative USD price")
	}
	return usd
}

// USDPriceExpression returns a read-only projection for pricing previews.
// The stored expression must keep its original CNY amounts and functions.
func USDPriceExpression(expression string, rate float64) (string, error) {
	entry, err := compileEntryFromCacheByHash(expression, ExprHashString(expression))
	if err != nil {
		return "", err
	}
	if err := validateCNYPrices(entry.cnyPrices, rate); err != nil {
		return "", err
	}
	if len(entry.cnyPrices) == 0 {
		return expression, nil
	}
	_, body := ParseExprVersion(expression)
	tree, err := parser.Parse(body)
	if err != nil {
		return "", err
	}
	projection := usdPriceProjection{currencyConversion: currencyConversion{rate: rate}}
	ast.Walk(&tree.Node, &projection)
	return tree.Node.String(), projection.err
}

type usdPriceProjection struct {
	currencyConversion
}

func (projection *usdPriceProjection) Visit(node *ast.Node) {
	if !isCNYPrice(*node) {
		return
	}
	amount, _ := requestRuleNumber((*node).(*ast.CallNode).Arguments[0])
	ast.Patch(node, &ast.FloatNode{Value: projection.toUSD(amount)})
}

// v1UnboundCNYCalls reserves the new built-in only when a v1 call has no
// local binding. Walk visits a declaration after its value and body.
type v1UnboundCNYCalls map[*ast.CallNode]struct{}

func (calls v1UnboundCNYCalls) Visit(node *ast.Node) {
	switch part := (*node).(type) {
	case *ast.CallNode:
		if name, ok := part.Callee.(*ast.IdentifierNode); ok && name.Value == cnyToUSD {
			calls[part] = struct{}{}
		}
	case *ast.VariableDeclaratorNode:
		if part.Name == cnyToUSD {
			ast.Find(part.Expr, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallNode); ok {
					delete(calls, call)
				}
				return false
			})
		}
	}
}
