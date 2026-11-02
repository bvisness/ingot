package ingot

import "testing"

func TestIngot2Go(t *testing.T) {
	ParseFile("test.ingot", []byte(`package template
	
func Foo(name string, class string) {
	return <div class={{ "foo " + <foo/> }}>
		<p><b>Hello</b>, {{ name }}! I have 182448574729837438928374873837482384 apples. 1e :)</p>
	</div>
}
`))
}
