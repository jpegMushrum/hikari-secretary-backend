package pagination

import (
	"net/url"
	"testing"
)

func TestParse(t *testing.T) {
	page, err := Parse(url.Values{"page": {"2"}, "page_size": {"20"}})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if page.Number != 2 || page.Size != 20 || page.Offset() != 20 {
		t.Errorf("page = %#v offset=%d, want number 2, size 20, offset 20", page, page.Offset())
	}
}

func TestParseRejectsUnsupportedSize(t *testing.T) {
	if _, err := Parse(url.Values{"page_size": {"31"}}); err == nil {
		t.Fatal("Parse() error = nil, want error")
	}
}

func TestParseRejectsRepeatedParameterEvenWhenFirstValueIsEmpty(t *testing.T) {
	if _, err := Parse(url.Values{"page": {"", "2"}}); err == nil {
		t.Fatal("Parse() error = nil, want error")
	}
}
