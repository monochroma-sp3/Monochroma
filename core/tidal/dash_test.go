package tidal

import (
	"fmt"
	"testing"
)

// Attribute order inside <S/> is not significant in XML and Tidal's CDN is free
// to reorder it, so the segment count must not depend on it. r="-1" means
// "repeat to the end of the period", not "repeat -1 times".
func TestCountDASHSegments(t *testing.T) {
	// 3m14s at timescale 44100 = 8,555,400 ticks.
	const mpd = `<MPD mediaPresentationDuration="PT0H3M14.0S">` +
		`<SegmentTemplate timescale="44100" initialization="i" media="m$Number$">%s</SegmentTemplate></MPD>`

	tests := []struct {
		name     string
		timeline string
		want     int
	}{
		{"d first", `<S d="180000" r="10"/><S d="90000"/>`, 12},
		{"t first", `<S t="0" d="180000" r="10"/><S d="90000"/>`, 12},
		{"r before d", `<S r="10" d="180000"/>`, 11},
		{"space before close", `<S d="180000" r="10" />`, 11},
		{"no repeat attr", `<S d="180000"/><S d="180000"/>`, 2},
		// ceil(8555400/180000) = 48
		{"repeat until end", `<S t="0" d="180000" r="-1"/>`, 48},
		{"repeat until end mid-timeline", `<S t="0" d="180000" r="9"/><S d="180000" r="-1"/>`, 48},
		{"empty timeline", ``, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			xml := fmt.Sprintf(mpd, "<SegmentTimeline>"+tc.timeline+"</SegmentTimeline>")
			if got := countDASHSegments(xml); got != tc.want {
				t.Errorf("countDASHSegments() = %d, want %d", got, tc.want)
			}
		})
	}

	t.Run("repeat until end without period duration", func(t *testing.T) {
		// No mediaPresentationDuration to expand against: count the one known
		// segment rather than letting r go negative.
		xml := `<SegmentTemplate timescale="44100"><SegmentTimeline><S d="180000" r="-1"/></SegmentTimeline></SegmentTemplate>`
		if got := countDASHSegments(xml); got != 1 {
			t.Errorf("countDASHSegments() = %d, want 1", got)
		}
	})
}
