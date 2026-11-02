package ingot

import (
	"bytes"
	"testing"
)

func TestIngot2Go(t *testing.T) {
	t.Run("simple", func(t *testing.T) {
		var out bytes.Buffer
		ParseFile("test.ingot", []byte(`package template
	
func Foo(name string, class string) {
	return <div class={{ "foo " + <foo/> }}>
		<p><b>Hello</b>, {{ name }}! I have 182448574729837438928374873837482384 apples. 1e :)</p>
	</div>
}
`), &out)
		t.Log(out.String())
	})
	t.Run("loop", func(t *testing.T) {
		var out bytes.Buffer
		ParseFile("test.ingot", []byte(`package template
	
func Foo(names []string) {
	return <div>
		Hello
		{{ for i, name := range names }}
		 	{{ if i > 0 }}, {{ end }}{{ name }}
		{{ end }}
		!
	</div>
}
`), &out)
		t.Log(out.String())
	})
}
