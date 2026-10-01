package main

import (
	"strings"
	"testing"
)

func TestSummaryMetricsAreInformational(t *testing.T) {
	html, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"primary-metric", "secondary-metric"} {
		if !strings.Contains(string(html), `<div class="summary-card metric-card `+kind+`">`) {
			t.Fatalf("%s must be a static metric, not a navigation control", kind)
		}
	}
	if !strings.Contains(string(html), `class="summary-card nav-card upcoming-nav-card" type="button" data-view="upcoming"`) {
		t.Fatal("payment schedule navigation must remain available")
	}
}

func TestTabletSummaryDoesNotStretchOnEmptyDashboard(t *testing.T) {
	css, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	source := compactSource(string(css))
	start := strings.Index(source, "@media(max-width:850px){.layout{")
	if start < 0 {
		t.Fatal("tablet layout breakpoint is missing")
	}
	end := strings.Index(source[start:], "}")
	if end < 0 || !strings.Contains(source[start:start+end], "align-content:start;") {
		t.Fatal("tablet layout must keep summary and content rows intrinsic")
	}
}

func TestDashboardPaymentPanelFollowsChartAndListStartsWithName(t *testing.T) {
	css, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	source := compactSource(string(css))
	if !strings.Contains(source, "contain:size;") || strings.Contains(source, "max-height:360px;") {
		t.Fatal("desktop payment list must fit the chart row instead of capping the panel height")
	}
	if !strings.Contains(source, ".upcoming-preview{contain:none;max-height:320px;") {
		t.Fatal("stacked payment panel must retain its independent scrolling height")
	}
	js, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "service-avatar") || strings.Contains(string(css), "service-avatar") {
		t.Fatal("subscription rows must start with service names without avatar squares")
	}
}
