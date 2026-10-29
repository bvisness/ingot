# PHASE 1: DSL -> GO

Convert from "ingot" syntax to plain old Go.

1. Use go/scanner to lex the file. When encountering `<` in certain expression contexts, break into custom lexing/parsing code for tags.
2. When the whole file is lexed, transpile the custom tags to plain Go code. Use a very plain subset of Go and be very explicit.

# PHASE 2: PARSE & VALIDATE GO

Because Ingot files can be transpiled to TypeScript, they are limited to a pretty restrictive subset of Go. But now that the whole file is plain old Go, we can parse it using the standard library parser and simply walk the AST to ensure we're only using Approved Features.

# PHASE 3: TRANSPILE, MAYBE

If using Ingot for the backend, no transpilation is necessary; just compile the resulting Go files into your project. But if transpiling to TypeScript, do that.

This can simply be another tree-walk, with full knowledge that the file is valid.
