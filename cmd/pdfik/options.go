package main

import (
	"fmt"
	"regexp"
	"strings"
)

// supportedFormats are the paper sizes the rendering pipeline is calibrated
// for (page size and the auto-fit width table) — the same list the API
// validates against. Checking locally gives an instant, specific message
// instead of a 422 round-trip.
var supportedFormats = []string{"A0", "A1", "A2", "A3", "A4", "A5", "A6", "Letter", "Legal", "Tabloid", "Ledger"}

// validateFormat accepts the supported names case-insensitively.
func validateFormat(f string) error {
	for _, s := range supportedFormats {
		if strings.EqualFold(s, f) {
			return nil
		}
	}
	return usagef("page format %q is not supported (supported: %s)", f, strings.Join(supportedFormats, ", "))
}

// lengthPattern is a CSS length the renderer understands: a number with one of
// mm, cm, in, px. Bare numbers, em, % and typos are refused here so the user
// gets the message immediately rather than after a round-trip.
var lengthPattern = regexp.MustCompile(`^(\d+\.?\d*|\.\d+)(mm|cm|in|px)$`)

func checkLength(flagName, v string) error {
	if lengthPattern.MatchString(v) {
		return nil
	}
	return usagef("%s %q is not a valid length — use a number with mm, cm, in or px (e.g. 10mm, 1cm, 0.5in, 20px)", flagName, v)
}

var marginSides = [4]string{"top", "right", "bottom", "left"}

// buildMargin combines --margin with --margin-<side> overrides into the API's
// margin object; nil when nothing was set. --margin takes one value for all
// sides or CSS shorthand (comma- or space-separated, in top right bottom left
// order; two values = vertical horizontal; three = top horizontal bottom). Only
// the sides the user named are sent, so the renderer's defaults cover the rest.
func buildMargin(all string, sides [4]string) (map[string]string, error) {
	m := map[string]string{}
	if all != "" {
		trimmed := strings.TrimSpace(all)
		if strings.HasPrefix(trimmed, ",") || strings.HasSuffix(trimmed, ",") || strings.Contains(all, ",,") {
			return nil, usagef("--margin %q has an empty value — use top,right,bottom,left like 10mm,1cm,0.5in,20px", all)
		}
		parts := strings.FieldsFunc(all, func(r rune) bool { return r == ',' || r == ' ' })
		var t, r, b, l string
		switch len(parts) {
		case 1:
			t, r, b, l = parts[0], parts[0], parts[0], parts[0]
		case 2:
			t, r, b, l = parts[0], parts[1], parts[0], parts[1]
		case 3:
			t, r, b, l = parts[0], parts[1], parts[2], parts[1]
		case 4:
			t, r, b, l = parts[0], parts[1], parts[2], parts[3]
		default:
			return nil, usagef("--margin %q: give 1 value for all sides or 2–4 values in CSS order (top,right,bottom,left)", all)
		}
		for _, v := range []string{t, r, b, l} {
			if err := checkLength("--margin", v); err != nil {
				return nil, err
			}
		}
		m["top"], m["right"], m["bottom"], m["left"] = t, r, b, l
	}
	for i, side := range marginSides {
		if v := sides[i]; v != "" {
			if err := checkLength("--margin-"+side, v); err != nil {
				return nil, err
			}
			m[side] = v
		}
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}

// pageOptions is the `options` object shared by the native commands.
type pageOptions struct {
	format       string
	landscape    bool
	noBackground bool
	margin       string
	marginSides  [4]string
}

func (p pageOptions) build() (map[string]any, error) {
	opts := map[string]any{}
	if p.format != "" {
		if err := validateFormat(p.format); err != nil {
			return nil, err
		}
		opts["format"] = p.format
	}
	if p.landscape {
		opts["landscape"] = true
	}
	if p.noBackground {
		opts["print_background"] = false
	}
	margin, err := buildMargin(p.margin, p.marginSides)
	if err != nil {
		return nil, err
	}
	if margin != nil {
		opts["margin"] = margin
	}
	return opts, nil
}

func formatMs(ms int) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dms", ms)
}
