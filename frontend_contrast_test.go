package main

import (
	"math"
	"regexp"
	"strconv"
	"testing"
)

func TestThemeTextTokensMeetNormalTextContrast(t *testing.T) {
	css, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile(`(?s):root(\[data-theme=light\])?\s*\{([^}]+)\}`).FindAllStringSubmatch(string(css), -1)
	if len(blocks) != 2 {
		t.Fatal("expected dark and light theme token blocks")
	}
	tokenPattern := regexp.MustCompile(`--([a-z-]+):\s*#([0-9a-fA-F]{6})\s*;`)
	for _, block := range blocks {
		name := "dark"
		if block[1] != "" {
			name = "light"
		}
		t.Run(name, func(t *testing.T) {
			colors := make(map[string]string)
			for _, token := range tokenPattern.FindAllStringSubmatch(block[2], -1) {
				colors[token[1]] = token[2]
			}
			check := func(foreground, background string) {
				t.Helper()
				if colors[foreground] == "" || colors[background] == "" {
					t.Fatalf("missing color tokens %s / %s", foreground, background)
				}
				a := cssRelativeLuminance(t, colors[foreground])
				b := cssRelativeLuminance(t, colors[background])
				ratio := (math.Max(a, b) + .05) / (math.Min(a, b) + .05)
				if ratio < 4.5 {
					t.Errorf("%s / %s contrast %.3f must be at least 4.5", foreground, background, ratio)
				}
			}
			for _, text := range []string{"text", "muted", "dim", "accent-strong"} {
				for _, surface := range []string{"bg", "card"} {
					check(text, surface)
				}
			}
			check("accent-ink", "accent")
			check("accent-ink", "accent-strong")
		})
	}
}

// WCAG relative luminance uses linear sRGB channel values, not raw hex levels.
func cssRelativeLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	value, err := strconv.ParseUint(hex, 16, 24)
	if err != nil {
		t.Fatal(err)
	}
	linear := func(channel uint64) float64 {
		v := float64(channel) / 255
		if v <= .04045 {
			return v / 12.92
		}
		return math.Pow((v+.055)/1.055, 2.4)
	}
	return .2126*linear((value>>16)&255) + .7152*linear((value>>8)&255) + .0722*linear(value&255)
}
