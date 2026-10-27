package provider

import (
	"strings"
	"testing"
)

// TestExtractMessageSummarisesHTMLErrorPage is the regression test for the raw
// HTML dump users saw when a CDN answered with an error page: the client must
// receive a short one-line description, never markup.
func TestExtractMessageSummarisesHTMLErrorPage(t *testing.T) {
	body := []byte(`<!DOCTYPE html>
<!--[if lt IE 7]> <html class="no-js ie6 oldie" lang="en-US"> <![endif]-->
<html class="no-js" lang="en-US">
<head>
<title>opencode.ai | 502: Bad gateway</title>
</head>
<body>
<h1>Bad gateway</h1>
</body>
</html>`)

	msg := errorMessage(502, extractMessage(body))
	if strings.Contains(msg, "<") || strings.Contains(msg, "DOCTYPE") {
		t.Fatalf("message still contains markup: %q", msg)
	}
	if !strings.Contains(msg, "502: Bad gateway") {
		t.Fatalf("message lost the page title: %q", msg)
	}
	if strings.Contains(msg, "\n") {
		t.Fatalf("message must be a single line, got %q", msg)
	}
}

func TestExtractMessageFallsBackWhenHTMLHasNoTitle(t *testing.T) {
	msg := extractMessage([]byte("<html><body>blocked</body></html>"))
	if !strings.Contains(msg, "HTML error page") {
		t.Fatalf("message = %q, want an HTML error page summary", msg)
	}
}

func TestExtractMessageKeepsJSONMessages(t *testing.T) {
	body := []byte(`{"error":{"message":"Free usage limit reached.","type":"rate_limit"}}`)
	if got := extractMessage(body); got != "Free usage limit reached." {
		t.Fatalf("extractMessage = %q", got)
	}
}

func TestExtractMessageCollapsesMultilineText(t *testing.T) {
	body := []byte("line one\nline two\r\n\tline three")
	got := extractMessage(body)
	if got != "line one line two line three" {
		t.Fatalf("extractMessage = %q", got)
	}
}

func TestLooksLikeHTML(t *testing.T) {
	cases := map[string]bool{
		"<!DOCTYPE html><html></html>": true,
		"  <html><body>x</body></html>": true,
		`{"error":"x"}`:                 false,
		"":                              false,
		"<title>only</title>":           true,
	}
	for in, want := range cases {
		if got := looksLikeHTML(in); got != want {
			t.Fatalf("looksLikeHTML(%q) = %v, want %v", in, got, want)
		}
	}
}
