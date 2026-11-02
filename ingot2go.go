package ingot

import (
	"fmt"
	"go/scanner"
	"go/token"
	"io"
	"reflect"
	"slices"

	"github.com/bvisness/ingot/utils"
)

func ParseFile(filename string, src []byte, out io.Writer) {
	var l ingotLexer

	// NOTE(ben): It's very stupid, but we have to give the lexer its own copy of
	// the source because it will actually overwrite the source with whitespace
	// as part of babysitting the standard Go lexer.
	l.src = slices.Clone(src)
	l.fset = token.NewFileSet()
	l.file = l.fset.AddFile(filename, l.fset.Base(), len(src))
	l.file.SetLinesForContent(src)

	l.goScanner.Init(l.file, src, l.scanErrorHandler, scanner.ScanComments)

	// TODO(ben): Turn this and the above into constructors
	var t goTranspiler
	t.src = src
	t.out = out

	for {
		pos, tok, lit := l.Lex()
		if tok == token.EOF {
			break
		}
		fmt.Printf("%s\t%s\t%q\n", l.fset.Position(pos), tok, lit)

		t.Pump(l.posToOffset(pos), tok, lit)
	}
}

// ============================================================================
// LEXER

const ingotTokenStart = 1000

const (
	TagContents token.Token = iota + ingotTokenStart

	TagStart        // <
	ClosingTagStart // </
	TagEndUnclosed  // >
	TagEndClosed    // />

	Ident       // e.g. div, my-element, FooBar123
	Eq          // =
	String      // "foo"
	GoExprStart // {{
	GoExprEnd   // }}

	// NOTE(ben): For loops, if/else, etc. are lexed using the actual Go scanner.
)

type operator struct {
	lit string
	tok token.Token
}

var operators = []operator{
	// NOTE(ben): Longest first, at least in cases where there is ambiguity
	{"</", ClosingTagStart}, {"/>", TagEndClosed},
	{"<", TagStart}, {">", TagEndUnclosed},

	{"=", Eq}, {"{{", GoExprStart},
	// NOTE(ben): We don't include GoExprEnd here because it is encountered
	// specially by the Go scanner, not by the Ingot lexer.
}

type ingotLexer struct {
	src  []byte
	fset *token.FileSet
	file *token.File

	state       ingotLexerState
	outerStates []ingotLexerState

	// Position in the file. Kept in sync with the Go scanner since it has its
	// own cursor.
	cur int

	// For Go lexing
	goScanner  scanner.Scanner
	prevGoTok  token.Token
	blankedCur int
}

type ingotLexerState int

const (
	ilStateGo ingotLexerState = iota
	ilStateTag
	ilStateTagContents
	ilStateClosingTag
)

// NOTE(ben): The final "literal" return value is normalized somewhat to always
// return the characters recognized as the token, even for normal Go operators.
func (l *ingotLexer) Lex() (token.Pos, token.Token, string) {
	switch l.state {
	case ilStateGo:
		// NOTE(ben): Lex a Go token
		start := l.cur
		pos, tok, _ := l.goScanner.Scan()
		// NOTE(ben): Sync general cursor with the Go scanner.
		l.cur = scannerOffset(&l.goScanner)

		if tokenCanPrecedeTag(l.prevGoTok) && tok == token.LSS {
			// NOTE(ben): Saw '<' in a place where tags can go; switch to Ingot and
			// re-lex the '<' as a tag start
			l.pushOuterState()
			l.state = ilStateTag
			l.cur = l.posToOffset(pos)
			return l.Lex()
		}
		if tok == token.RBRACE && l.remaining() > 0 && l.src[l.cur] == '}' {
			// NOTE(ben): Crude way of detecting '}}', which switches back to Ingot
			l.state = l.popOuterState()
			l.cur = l.posToOffset(pos + 2)
			return pos, GoExprEnd, "}}"
		}
		l.prevGoTok = tok
		return pos, tok, string(l.src[start:l.cur])
	case ilStateTag:
		// NOTE(ben): Advance past whitespace, maybe EOF
		for l.remaining() > 0 && isspace(l.src[l.cur]) {
			l.cur++
		}
		if l.remaining() == 0 {
			return l.eof()
		}

		if pos, op := l.lexOperator(); pos != token.NoPos {
			switch op.tok {
			case TagEndClosed:
				// NOTE(ben): Tag self-closed; back to previous state
				l.state = l.popOuterState()
			case TagEndUnclosed:
				// NOTE(ben): Now inside the tag
				l.state = ilStateTagContents
			case GoExprStart:
				// NOTE(ben): Go expression inside the tag somewhere. Switch back to
				// Go and note that we just parsed a `<` from Go's perspective so we
				// don't have to worry about semicolon insertion below.
				l.pushOuterState()
				l.state = ilStateGo
			}
			if l.state == ilStateGo {
				l.blankOutIngotCodeAndResetScanner()
			}
			return pos, op.tok, op.lit
		}
		if pos, ident := l.lexIdent(); pos != token.NoPos {
			return pos, Ident, ident
		}
		if pos, tok, lit := l.lexString(); pos != token.NoPos {
			return pos, tok, lit
		}

		pos := l.pos()
		lit := string(l.src[l.cur])
		l.cur++
		return pos, token.ILLEGAL, lit
	case ilStateTagContents:
		start := l.cur
		for l.remaining() > 0 && l.src[l.cur] != '<' && !l.peekLit("{{") {
			l.cur++
		}

		if start < l.cur {
			return l.posAt(start), TagContents, string(l.src[start:l.cur])
		} else if l.remaining() == 0 {
			return l.eof()
		} else {
			// NOTE(ben): Some operator
			pos, op := l.lexOperator()
			utils.Assert(pos)
			switch op.tok {
			case GoExprStart:
				l.pushOuterState()
				l.state = ilStateGo
				l.blankOutIngotCodeAndResetScanner()
			case ClosingTagStart:
				l.state = ilStateClosingTag
			case TagStart:
				l.pushOuterState()
				l.state = ilStateTag
			default:
				panic(fmt.Sprintf("unexpected operator %s", op.lit))
			}
			return pos, op.tok, op.lit
		}
	case ilStateClosingTag:
		// NOTE(ben): Advance past whitespace, maybe EOF
		for l.remaining() > 0 && isspace(l.src[l.cur]) {
			l.cur++
		}
		if l.remaining() == 0 {
			return l.eof()
		}

		for _, op := range operators {
			if l.peekLit(op.lit) {
				if op.tok == TagEndClosed || op.tok == TagEndUnclosed {
					// NOTE(ben): Tag closed; back to previous state
					l.state = l.popOuterState()
				}
				pos := l.pos()
				l.cur += len(op.lit)
				if l.state == ilStateGo {
					// NOTE(ben): Reset Go scanner
					l.blankOutIngotCodeAndResetScanner()
					l.src[l.cur-1] = '_'                  // NOTE(ben): Reset semicolon insertion
					_, goTok, goLit := l.goScanner.Scan() // NOTE(ben): Consume the underscore
					l.prevGoTok = token.ILLEGAL
					utils.Assert(goTok == token.IDENT && goLit == "_")
				}
				return pos, op.tok, op.lit
			}
		}
		if pos, op := l.lexOperator(); pos != token.NoPos {
			if op.tok == TagEndClosed || op.tok == TagEndUnclosed {
				// NOTE(ben): Tag closed; back to previous state
				l.state = l.popOuterState()
			}
			if l.state == ilStateGo {
				// NOTE(ben): Reset Go scanner
				l.blankOutIngotCodeAndResetScanner()
				l.src[l.cur-1] = '_'                  // NOTE(ben): Reset semicolon insertion
				_, goTok, goLit := l.goScanner.Scan() // NOTE(ben): Consume the underscore
				utils.Assert(goTok == token.IDENT && goLit == "_")
			}
			return pos, op.tok, op.lit
		}
		if pos, ident := l.lexIdent(); pos != token.NoPos {
			return pos, Ident, ident
		}

		pos := l.pos()
		lit := string(l.src[l.cur])
		l.cur++
		return pos, token.ILLEGAL, lit
	default:
		panic(fmt.Sprintf("unexpected state %v", l.state))
	}
}

func (l *ingotLexer) peekLit(lit string) bool {
	if l.remaining() < len(lit) {
		return false
	}
	return string(l.src[l.cur:l.cur+len(lit)]) == lit
}

func (l *ingotLexer) lexOperator() (token.Pos, operator) {
	for _, op := range operators {
		if l.peekLit(op.lit) {
			pos := l.pos()
			l.cur += len(op.lit)
			return pos, op
		}
	}
	return token.NoPos, operator{}
}

func (l *ingotLexer) lexIdent() (token.Pos, string) {
	utils.Assert(l.remaining() > 0)
	start := l.cur
	if isalpha(l.src[l.cur]) {
		l.cur++
		for l.remaining() > 0 && (isalpha(l.src[l.cur]) || isdigit(l.src[l.cur])) {
			l.cur++
		}
		return l.posAt(start), string(l.src[start:l.cur])
	}
	return token.NoPos, ""
}

func (l *ingotLexer) lexString() (token.Pos, token.Token, string) {
	utils.Assert(l.remaining() > 0)
	start := l.cur
	if l.src[l.cur] == '"' {
		l.cur++
		escapeNext := false
		for ; ; l.cur++ {
			if l.remaining() == 0 {
				return l.posAt(start), token.ILLEGAL, string(l.src[start:])
			}
			if escapeNext {
				escapeNext = false
				continue
			}

			switch l.src[l.cur] {
			case '"':
				l.cur++
				return l.pos(), token.STRING, string(l.src[start:l.cur])
			case '\\':
				escapeNext = true
			}
		}
	}
	return token.NoPos, token.ILLEGAL, ""
}

func (l *ingotLexer) pushOuterState() {
	l.outerStates = append(l.outerStates, l.state)
}

func (l *ingotLexer) popOuterState() ingotLexerState {
	res := l.outerStates[len(l.outerStates)-1]
	l.outerStates = l.outerStates[:len(l.outerStates)-1]
	return res
}

func (l *ingotLexer) pos() token.Pos {
	return l.posAt(l.cur)
}

func (l *ingotLexer) posAt(offset int) token.Pos {
	return token.Pos(l.file.Base() + offset)
}

func (l *ingotLexer) posToOffset(pos token.Pos) int {
	return int(pos) - l.file.Base()
}

func (l *ingotLexer) remaining() int {
	utils.Assert(l.cur <= len(l.src))
	return len(l.src) - l.cur
}

func (l *ingotLexer) blankOutIngotCodeAndResetScanner() {
	for i := l.blankedCur; i < l.cur; i++ {
		l.src[i] = ' '
	}
	l.blankedCur = l.cur
	l.goScanner.Init(l.file, l.src, l.scanErrorHandler, scanner.ScanComments)
	l.prevGoTok = token.ILLEGAL
}

func (l *ingotLexer) eof() (token.Pos, token.Token, string) {
	return l.posAt(len(l.src)), token.EOF, ""
}

func (l *ingotLexer) scanErrorHandler(pos token.Position, msg string) {
	fmt.Printf("ERROR: %s\t%s", pos, msg)
}

func isalpha(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isdigit(c byte) bool {
	return '0' <= c && c <= '9'
}

func isspace(c byte) bool {
	switch c {
	case ' ', '\f', '\n', '\r', '\t', '\v':
		return true
	default:
		return false
	}
}

// NOTE(ben): The Go [scanner.Scanner] type hides its cursor position,
// `.offset`, for absolutely no good reason.
//
// The result of this is a direct index into the source file, so equivalent
// to `p.cur`.
func scannerOffset(s *scanner.Scanner) int {
	return int(reflect.ValueOf(s).Elem().FieldByName("offset").Int())
}

// NOTE(ben): This is far from an exact science. Certainly we need to avoid
// those places where it is valid to put a less-than operator. But outside of
// that, there are still many places where it is invalid to put a Go struct as
// well, and at that point the question is: do you want the error to be a bad
// less-than, or a bad struct?
func tokenCanPrecedeTag(tok token.Token) bool {
	switch tok {
	case token.RETURN:
		return true
	case
		token.INC, token.DEC,
		token.PERIOD,
		token.RPAREN, token.RBRACK, token.RBRACE,
		token.SEMICOLON:
		return false
	default:
		return tok.IsOperator()
	}
}

// ============================================================================
// TRANSPILING TO GO

type goTranspiler struct {
	src   []byte
	out   io.Writer
	state ingotGoTranspilerState

	firstGoTokenOffset int
}

type ingotGoTranspilerState int

const (
	igtStateGo ingotGoTranspilerState = iota
	// TODO(ben): sigh to be honest I wish I had assembled an AST during the lexing
	// stage because I totally could :( and it would make it way easier to do the
	// actual transpiling part without worrying about the wrong operators in funny
	// places etc. etc. and most of the logic is gonna be mirrored.
)

func (t *goTranspiler) Pump(offset int, tok token.Token, lit string) error {
	switch t.state {
	case igtStateGo:
		if tokenIsGo(tok) {
		} else {
			// NOTE(ben): Emit run of Go tokens in source form
			end := offset + len(lit)
			_, err := t.out.Write(t.src[t.firstGoTokenOffset:end])
			if err != nil {
				return err
			}

			// NOTE(ben): Then switch to whatever other state (it really must be a
			// tag)
			switch tok {
			case TagStart:
				t.state = 
			}
		}
	}
	return nil
}

func tokenIsGo(tok token.Token) bool {
	return tok < ingotTokenStart
}
