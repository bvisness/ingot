package ingot

import (
	"fmt"
	"go/scanner"
	"go/token"
	"reflect"

	"github.com/bvisness/ingot/utils"
)

type ingotScanner struct {
	src  []byte
	fset *token.FileSet
	file *token.File

	state       ingotParserState
	outerStates []ingotParserState

	// Position in the file. Kept in sync with the Go scanner since it has its
	// own cursor.
	cur int

	// For Go lexing
	goScanner  scanner.Scanner
	prevGoTok  token.Token
	blankedCur int
}

type ingotParserState int

const (
	ipGo ingotParserState = iota
	ipTag
	ipTagContents
	ipClosingTag
)

// For stupid reasons, this will modify `src`.
func ParseFile(filename string, src []byte) {
	var p ingotScanner

	p.src = src
	p.fset = token.NewFileSet()
	p.file = p.fset.AddFile(filename, p.fset.Base(), len(src))
	p.file.SetLinesForContent(src)

	p.goScanner.Init(p.file, src, p.scanErrorHandler, scanner.ScanComments)

	for {
		pos, tok, lit := p.Lex()
		if tok == token.EOF {
			break
		}
		fmt.Printf("%s\t%s\t%q\n", p.fset.Position(pos), tok, lit)
	}
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

const (
	TagContents token.Token = iota + 1001

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

func (s *ingotScanner) Lex() (token.Pos, token.Token, string) {
	switch s.state {
	case ipGo:
		// NOTE(ben): Lex a Go token
		pos, tok, lit := s.goScanner.Scan()
		// NOTE(ben): Sync general cursor with the Go scanner.
		s.cur = scannerOffset(&s.goScanner)

		if tokenCanPrecedeTag(s.prevGoTok) && tok == token.LSS {
			// NOTE(ben): Saw '<' in a place where tags can go; switch to Ingot and
			// re-lex the '<' as a tag start
			s.pushOuterState()
			s.state = ipTag
			s.cur = int(pos) - s.file.Base()
			return s.Lex()
		}
		if tok == token.RBRACE && s.remaining() > 0 && s.src[s.cur] == '}' {
			// NOTE(ben): Crude way of detecting '}}', which switches back to Ingot
			s.state = s.popOuterState()
			s.cur = int(pos+2) - s.file.Base()
			return pos, GoExprEnd, "}}"
		}
		s.prevGoTok = tok
		return pos, tok, lit
	case ipTag:
		// NOTE(ben): Advance past whitespace, maybe EOF
		for s.remaining() > 0 && isspace(s.src[s.cur]) {
			s.cur++
		}
		if s.remaining() == 0 {
			return s.eof()
		}

		if pos, op := s.lexOperator(); pos != token.NoPos {
			switch op.tok {
			case TagEndClosed:
				// NOTE(ben): Tag self-closed; back to previous state
				s.state = s.popOuterState()
			case TagEndUnclosed:
				// NOTE(ben): Now inside the tag
				s.state = ipTagContents
			case GoExprStart:
				// NOTE(ben): Go expression inside the tag somewhere. Switch back to
				// Go and note that we just parsed a `<` from Go's perspective so we
				// don't have to worry about semicolon insertion below.
				s.pushOuterState()
				s.state = ipGo
			}
			if s.state == ipGo {
				s.blankOutIngotCodeAndResetScanner()
			}
			return pos, op.tok, op.lit
		}
		if pos, ident := s.lexIdent(); pos != token.NoPos {
			return pos, Ident, ident
		}
		if pos, tok, lit := s.lexString(); pos != token.NoPos {
			return pos, tok, lit
		}

		pos := s.pos()
		lit := string(s.src[s.cur])
		s.cur++
		return pos, token.ILLEGAL, lit
	case ipTagContents:
		start := s.cur
		for s.remaining() > 0 && s.src[s.cur] != '<' && !s.peekLit("{{") {
			s.cur++
		}

		if start < s.cur {
			return s.posAt(start), TagContents, string(s.src[start:s.cur])
		} else if s.remaining() == 0 {
			return s.eof()
		} else {
			// NOTE(ben): Some operator
			pos, op := s.lexOperator()
			utils.Assert(pos)
			switch op.tok {
			case GoExprStart:
				s.pushOuterState()
				s.state = ipGo
				s.blankOutIngotCodeAndResetScanner()
			case ClosingTagStart:
				s.state = ipClosingTag
			case TagStart:
				s.pushOuterState()
				s.state = ipTag
			default:
				panic(fmt.Sprintf("unexpected operator %s", op.lit))
			}
			return pos, op.tok, op.lit
		}
	case ipClosingTag:
		// NOTE(ben): Advance past whitespace, maybe EOF
		for s.remaining() > 0 && isspace(s.src[s.cur]) {
			s.cur++
		}
		if s.remaining() == 0 {
			return s.eof()
		}

		for _, op := range operators {
			if s.peekLit(op.lit) {
				if op.tok == TagEndClosed || op.tok == TagEndUnclosed {
					// NOTE(ben): Tag closed; back to previous state
					s.state = s.popOuterState()
				}
				pos := s.pos()
				s.cur += len(op.lit)
				if s.state == ipGo {
					// NOTE(ben): Reset Go scanner
					s.blankOutIngotCodeAndResetScanner()
					s.src[s.cur-1] = '_'                  // NOTE(ben): Reset semicolon insertion
					_, goTok, goLit := s.goScanner.Scan() // NOTE(ben): Consume the underscore
					s.prevGoTok = token.ILLEGAL
					utils.Assert(goTok == token.IDENT && goLit == "_")
				}
				return pos, op.tok, op.lit
			}
		}
		if pos, op := s.lexOperator(); pos != token.NoPos {
			if op.tok == TagEndClosed || op.tok == TagEndUnclosed {
				// NOTE(ben): Tag closed; back to previous state
				s.state = s.popOuterState()
			}
			if s.state == ipGo {
				// NOTE(ben): Reset Go scanner
				s.blankOutIngotCodeAndResetScanner()
				s.src[s.cur-1] = '_'                  // NOTE(ben): Reset semicolon insertion
				_, goTok, goLit := s.goScanner.Scan() // NOTE(ben): Consume the underscore
				utils.Assert(goTok == token.IDENT && goLit == "_")
			}
			return pos, op.tok, op.lit
		}
		if pos, ident := s.lexIdent(); pos != token.NoPos {
			return pos, Ident, ident
		}

		pos := s.pos()
		lit := string(s.src[s.cur])
		s.cur++
		return pos, token.ILLEGAL, lit
	default:
		panic(fmt.Sprintf("unexpected state %v", s.state))
	}
}

func (s *ingotScanner) peekLit(lit string) bool {
	if s.remaining() < len(lit) {
		return false
	}
	return string(s.src[s.cur:s.cur+len(lit)]) == lit
}

func (s *ingotScanner) lexOperator() (token.Pos, operator) {
	for _, op := range operators {
		if s.peekLit(op.lit) {
			pos := s.pos()
			s.cur += len(op.lit)
			return pos, op
		}
	}
	return token.NoPos, operator{}
}

func (s *ingotScanner) lexIdent() (token.Pos, string) {
	utils.Assert(s.remaining() > 0)
	start := s.cur
	if isalpha(s.src[s.cur]) {
		s.cur++
		for s.remaining() > 0 && (isalpha(s.src[s.cur]) || isdigit(s.src[s.cur])) {
			s.cur++
		}
		return s.posAt(start), string(s.src[start:s.cur])
	}
	return token.NoPos, ""
}

func (s *ingotScanner) lexString() (token.Pos, token.Token, string) {
	utils.Assert(s.remaining() > 0)
	start := s.cur
	if s.src[s.cur] == '"' {
		s.cur++
		escapeNext := false
		for ; ; s.cur++ {
			if s.remaining() == 0 {
				return s.posAt(start), token.ILLEGAL, string(s.src[start:])
			}
			if escapeNext {
				escapeNext = false
				continue
			}

			switch s.src[s.cur] {
			case '"':
				s.cur++
				return s.pos(), token.STRING, string(s.src[start:s.cur])
			case '\\':
				escapeNext = true
			}
		}
	}
	return token.NoPos, token.ILLEGAL, ""
}

func (s *ingotScanner) pushOuterState() {
	s.outerStates = append(s.outerStates, s.state)
}

func (s *ingotScanner) popOuterState() ingotParserState {
	res := s.outerStates[len(s.outerStates)-1]
	s.outerStates = s.outerStates[:len(s.outerStates)-1]
	return res
}

func (s *ingotScanner) pos() token.Pos {
	return s.posAt(s.cur)
}

func (s *ingotScanner) posAt(offset int) token.Pos {
	return token.Pos(s.file.Base() + offset)
}

func (s *ingotScanner) remaining() int {
	utils.Assert(s.cur <= len(s.src))
	return len(s.src) - s.cur
}

func (s *ingotScanner) blankOutIngotCodeAndResetScanner() {
	for i := s.blankedCur; i < s.cur; i++ {
		s.src[i] = ' '
	}
	s.blankedCur = s.cur
	s.goScanner.Init(s.file, s.src, s.scanErrorHandler, scanner.ScanComments)
	s.prevGoTok = token.ILLEGAL
}

func (s *ingotScanner) eof() (token.Pos, token.Token, string) {
	return s.posAt(len(s.src)), token.EOF, ""
}

func (s *ingotScanner) scanErrorHandler(pos token.Position, msg string) {
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
