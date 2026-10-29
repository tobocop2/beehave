package workflow

import (
	"maps"
	"slices"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// Functions returns the functions that an expression may call. Each one is pure, so a replay computes the same value.
// All but regex_match come from the go-cty standard library.
func Functions() map[string]function.Function {
	return map[string]function.Function{
		"coalesce":    stdlib.CoalesceFunc,
		"contains":    stdlib.ContainsFunc,
		"format":      stdlib.FormatFunc,
		"join":        stdlib.JoinFunc,
		"length":      stdlib.LengthFunc,
		"lower":       stdlib.LowerFunc,
		"regex_match": regexMatch(),
		"split":       stdlib.SplitFunc,
	}
}

// FunctionNames returns the sorted names of Functions.
func FunctionNames() []string { return slices.Sorted(maps.Keys(Functions())) }

// regexMatch returns the function that is true when an RE2 pattern matches anywhere in a string. It asks the standard library for all matches.
func regexMatch() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{{Name: "pattern", Type: cty.String}, {Name: "string", Type: cty.String}},
		Type:   function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			all, err := stdlib.RegexAllFunc.Call(args)
			if err != nil {
				return cty.NilVal, err
			}

			return cty.BoolVal(all.LengthInt() > 0), nil
		},
	})
}

// unknownFunctions returns one problem for each call inside expr of a function that Functions does not hold.
// The text names the known functions. The subject is the name in the call.
// An argument that is absent is a static null and holds no call.
func unknownFunctions(expr hcl.Expression) hcl.Diagnostics {
	node, ok := expr.(hclsyntax.Node)
	if !ok {
		return nil
	}

	known := FunctionNames()

	return hclsyntax.VisitAll(node, func(n hclsyntax.Node) hcl.Diagnostics {
		call, isCall := n.(*hclsyntax.FunctionCallExpr)
		if !isCall || slices.Contains(known, call.Name) {
			return nil
		}

		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "unknown function `" + call.Name + "`." + hint(call.Name, known),
			Subject:  &call.NameRange,
		}}
	})
}
