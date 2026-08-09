package webfetch

import "testing"

func TestClassifyHTML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		html string
		want shellClass
	}{
		{
			name: "article is static",
			html: `<html><body><article><h1>Article</h1><p>` +
				`A long enough paragraph with real visible content that should be extracted without a browser. ` +
				`A second sentence makes this representative of an ordinary server rendered page.</p></article></body></html>`,
			want: shellClassStatic,
		},
		{
			name: "next marker is likely shell",
			html: `<html><body><div id="__next"></div><script src="/_next/app.js"></script></body></html>`,
			want: shellClassLikely,
		},
		{
			name: "short script page is ambiguous",
			html: `<html><body><noscript>Please enable JavaScript</noscript><script>boot()</script></body></html>`,
			want: shellClassAmbiguous,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyHTML(tt.html); got != tt.want {
				t.Fatalf("classifyHTML() = %v, want %v", got, tt.want)
			}
		})
	}
}
