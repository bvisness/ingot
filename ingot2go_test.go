package ingot

import "testing"

func TestIngot2Go(t *testing.T) {
	ParseFile("test.ingot", []byte(`package template

func Foo(name string) {
	return <div class="foo">
		<p>Hello, {{ name }}! I have 182448574729837438928374873837482384 apples. 1e :)</p>
	</div>
}
`))
}
