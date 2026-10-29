package ingot

import "testing"

func TestIngot2Go(t *testing.T) {
	ParseFile("test.ingot", []byte(`package template

func Foo() {
	return <div class="foo">
		Hello, world! I have 182448574729837438928374873837482384 apples. 1e :)
	</div>
}
`))
}
