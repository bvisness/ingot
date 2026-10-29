package ingot

import (
	"fmt"
	"go/scanner"
	"go/token"

	"github.com/bvisness/ingot/utils"
)

type ingotParser struct {
	src  []byte
	fset *token.FileSet
	file *token.File

	state       ingotParserState
	outerStates []ingotParserState

	// For Go lexing
	s          scanner.Scanner
	prevTok    token.Token
	blankedCur int

	// For Ingot lexing
	cur int
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
	var p ingotParser

	p.src = src
	p.fset = token.NewFileSet()
	p.file = p.fset.AddFile(filename, p.fset.Base(), len(src))
	p.file.SetLinesForContent(src)

	p.s.Init(p.file, src, p.scanErrorHandler, scanner.ScanComments)

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
	Text token.Token = iota + 1001 // Arbitrary tag contents

	TagStart        // <
	ClosingTagStart // </
	TagEndUnclosed  // >
	TagEndClosed    // />

	DoctypeStart // <!

	Ident       // e.g. div, my-element, FooBar123
	Eq          // =
	Quote       // "
	GoExprStart // {{

	// NOTE(ben): For loops, if/else, etc. are lexed using the actual Go scanner.
)

type operator struct {
	lit string
	tok token.Token
}

var operators = []operator{
	// NOTE(ben): Longest first, at least in cases where there is ambiguity
	{"<!", DoctypeStart},
	{"</", ClosingTagStart}, {"/>", TagEndClosed},
	{"<", TagStart}, {">", TagEndUnclosed},

	{"=", Eq}, {`"`, Quote}, {"{{", GoExprStart},
}

func (p *ingotParser) Lex() (token.Pos, token.Token, string) {
	switch p.state {
	case ipGo:
		// NOTE(ben): Lex a Go token
		pos, tok, lit := p.s.Scan()
		if tokenCanPrecedeTag(p.prevTok) && tok == token.LSS {
			// NOTE(ben): Switch to Ingot and re-lex
			p.pushOuterState()
			p.state = ipTag
			p.cur = int(pos) - p.file.Base()
			return p.Lex()
		}
		p.prevTok = tok
		return pos, tok, lit
	case ipTag:
		// NOTE(ben): Advance past whitespace, maybe EOF
		for p.remaining() > 0 && isspace(p.src[p.cur]) {
			p.cur++
		}
		if p.remaining() == 0 {
			return p.eof()
		}

		for _, op := range operators {
			if p.peekLit(op.lit) {
				switch op.tok {
				case TagEndClosed:
					// NOTE(ben): Tag self-closed; back to previous state
					p.state = p.popOuterState()
				case TagEndUnclosed:
					// NOTE(ben): Now inside the tag
					p.state = ipTagContents
				}
				pos := p.pos()
				p.cur += len(op.lit)
				return pos, op.tok, op.lit
			}
		}
		if pos, ident := p.lexIdent(); ident != "" {
			return pos, Ident, ident
		}

		pos := p.pos()
		lit := string(p.src[p.cur])
		p.cur++
		return pos, token.ILLEGAL, lit
	case ipTagContents:
		// NOTE(ben): Report all text as a token, or EOF
		start := p.cur
		for p.remaining() > 0 && p.src[p.cur] != '<' {
			p.cur++
		}
		if p.remaining() == 0 && start == p.cur {
			return p.eof()
		}
		// NOTE(ben): Switch to tag or closing tag state
		if p.peekLit("</") {
			p.state = ipClosingTag
		} else {
			p.pushOuterState()
			p.state = ipTag
		}
		return p.posAt(start), Text, string(p.src[start:p.cur])
	case ipClosingTag:
		// NOTE(ben): Advance past whitespace, maybe EOF
		for p.remaining() > 0 && isspace(p.src[p.cur]) {
			p.cur++
		}
		if p.remaining() == 0 {
			return p.eof()
		}

		for _, op := range operators {
			if p.peekLit(op.lit) {
				if op.tok == TagEndClosed || op.tok == TagEndUnclosed {
					// NOTE(ben): Tag closed; back to previous state
					p.state = p.popOuterState()
				}
				pos := p.pos()
				p.cur += len(op.lit)
				if p.state == ipGo {
					// NOTE(ben): Reset Go scanner
					for i := p.blankedCur; i < p.cur; i++ {
						p.src[i] = ' '
					}
					p.src[p.cur-1] = '_' // NOTE(ben): Reset semicolon insertion
					p.s.Init(p.file, p.src, p.scanErrorHandler, scanner.ScanComments)
					_, goTok, goLit := p.s.Scan() // NOTE(ben): Consume the underscore
					utils.Assert(goTok == token.IDENT && goLit == "_")
				}
				return pos, op.tok, op.lit
			}
		}
		if pos, ident := p.lexIdent(); ident != "" {
			return pos, Ident, ident
		}

		pos := p.pos()
		lit := string(p.src[p.cur])
		p.cur++
		return pos, token.ILLEGAL, lit
	default:
		panic(fmt.Sprintf("unexpected state %v", p.state))
	}
}

func (p *ingotParser) peekLit(lit string) bool {
	if p.remaining() < len(lit) {
		return false
	}
	return string(p.src[p.cur:p.cur+len(lit)]) == lit
}

func (p *ingotParser) lexIdent() (token.Pos, string) {
	utils.Assert(p.remaining() > 0)
	start := p.cur
	if isalpha(p.src[p.cur]) {
		p.cur++
		for p.remaining() > 0 && (isalpha(p.src[p.cur]) || isdigit(p.src[p.cur])) {
			p.cur++
		}
		return p.posAt(start), string(p.src[start:p.cur])
	}
	return token.NoPos, ""
}

func (p *ingotParser) pushOuterState() {
	p.outerStates = append(p.outerStates, p.state)
}

func (p *ingotParser) popOuterState() ingotParserState {
	res := p.outerStates[len(p.outerStates)-1]
	p.outerStates = p.outerStates[:len(p.outerStates)-1]
	return res
}

func (p *ingotParser) pos() token.Pos {
	return p.posAt(p.cur)
}

func (p *ingotParser) posAt(offset int) token.Pos {
	return token.Pos(p.file.Base() + offset)
}

func (p *ingotParser) remaining() int {
	utils.Assert(p.cur <= len(p.src))
	return len(p.src) - p.cur
}

func (p *ingotParser) eof() (token.Pos, token.Token, string) {
	return p.posAt(len(p.src)), token.EOF, ""
}

func (p *ingotParser) scanErrorHandler(pos token.Position, msg string) {
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
