package pagination

import (
	"fmt"
	"net/url"
	"strconv"
)

const DefaultSize = 10

var allowedSizes = map[int]struct{}{10: {}, 20: {}, 30: {}}

type Page struct {
	Number int
	Size   int
}

func (p Page) Offset() int {
	return (p.Number - 1) * p.Size
}

func Parse(values url.Values) (Page, error) {
	number, err := parsePositiveInt(values, "page", 1)
	if err != nil {
		return Page{}, err
	}

	size, err := parsePositiveInt(values, "page_size", DefaultSize)
	if err != nil {
		return Page{}, err
	}
	if _, ok := allowedSizes[size]; !ok {
		return Page{}, fmt.Errorf("page_size must be one of 10, 20, or 30")
	}

	return Page{Number: number, Size: size}, nil
}

func parsePositiveInt(values url.Values, name string, fallback int) (int, error) {
	raw, found := values[name]
	if !found || len(raw) == 0 {
		return fallback, nil
	}
	if len(raw) != 1 {
		return 0, fmt.Errorf("%s must be supplied at most once", name)
	}
	if raw[0] == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw[0])
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}
