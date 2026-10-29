package utils

import (
	"fmt"
	"strings"
)

// Panics if the provided value is falsy (so, zero). This works for booleans
// but also normal values, through the magic of generics.
func Assert[T comparable](value T, msg ...any) {
	var zero T
	if value == zero {
		var finalMsg strings.Builder
		for i, arg := range msg {
			if i > 0 {
				finalMsg.WriteString(" ")
			}
			fmt.Fprintf(&finalMsg, "%v", arg)
		}
		panic(finalMsg.String())
	}
}
