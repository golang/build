// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Releaseschedule generates the release schedule diagram used
// on the release schedule wiki.
//
// When this program is updated, regenerate the SVG and replace the old version
// on the Go Release Cycle wiki page
//
// https://go.dev/s/release
// https://golang.org/wiki/Go-Release-Cycle
package main

import (
	"fmt"
	"math"
	"os"
	"strings"

	svg "github.com/ajstarks/svgo"
)

func main() {
	if err := doMain(); err != nil {
		panic(err)
	}
}

func doMain() error {
	f, err := os.OpenFile("release.svg", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		return err
	}
	defer f.Close()
	canvas := svg.New(f)
	canvas.Start(600, 400)
	canvas.Style("text/css",
		`text {
	fill: black;
}
line, path {
	stroke: black;
	fill: none;
}
@media (prefers-color-scheme: dark) {
	text {
		fill: white;
	}
	line, path {
		stroke: white;
	}
}`)
	canvas.Translate(300, 200)
	for i, month := range strings.Split("Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec", " ") {
		angle := func(midx int) float64 {
			return (float64(midx) - 3) * 2 * math.Pi / 12
		}
		begin, end := angle(i), angle(i+1)

		// Draw a single black wedge of the calendar.
		path := fmt.Sprintf("M 0,0 L %v,%v A 100,100 0 0 0 %v,%v L 0 0",
			100*math.Sin(begin), 100*math.Cos(begin), 100*math.Sin(end), 100*math.Cos(end))
		canvas.Path(path)

		// Draw the text. Spin it around for readability in the second half.
		canvas.RotateTranslate(50, 0, angle(i)*360/(2*math.Pi)+20)
		if i < 6 {
			canvas.Text(0, 0, month)
		} else {
			canvas.Group(`transform="rotate(180)"`, `transform-origin="13 -3"`)
			canvas.Text(0, 0, month)
			canvas.Gend()
		}
		canvas.Gend()
	}

	type milestone struct {
		month, week int
		name        string
	}
	milestones := []milestone{
		{1, 1, "Planning"},
		{1, 3, "General development"},
		{5, 4, "Freeze"},
		{6, 2, "First RC"},
		{8, 2, "Release"},
	}
	for relIdx, relName := range []string{"Summer", "Winter"} {
		angle := func(m milestone) float64 {
			return (float64(m.month-1) - 3 + (float64(m.week)-0.5)/4) * 2 * math.Pi / 12
		}

		// Shift the milestones 6 months for the winter release.
		milestones := milestones
		for i := range milestones {
			milestones[i].month = (milestones[i].month + 6*relIdx) % 12
		}

		frozen := false
		for i, m := range milestones {
			x, y := math.Cos(angle(m)), math.Sin(angle(m))
			// Align the text away from the center of the circle.
			textAnchor := "start"
			if x < 0 {
				textAnchor = "end"
			}
			// Color the arc depending on the freeze state.
			if m.name == "Freeze" {
				frozen = true
			}
			color := "green"
			if frozen {
				color = "blue"
			}

			// Center radius of the release arc.
			arcRadius := float64(120 + 20*relIdx)
			// Length of the line to the label. Vary a bit to avoid text overlap.
			lineLength := float64(30 + 5*((i+1)%2))
			// Distance from the end of the line to the text.
			textoff := float64(10)

			// Draw the arc to the next milestone.
			if i+1 < len(milestones) {
				nx, ny := math.Cos(angle(milestones[i+1])), math.Sin(angle(milestones[i+1]))
				canvas.Arc(int(x*arcRadius), int(y*arcRadius), int(arcRadius), int(arcRadius), 0, false, true, int(nx*arcRadius), int(ny*arcRadius), "stroke-width:10; fill:none; stroke: "+color)
			}
			// Draw the line from the inner edge of the arc.
			canvas.Line(int(x*(arcRadius-5)), int(y*(arcRadius-5)), int(x*(arcRadius+lineLength)), int(y*(arcRadius+lineLength)))
			canvas.Text(int(x*(arcRadius+lineLength+textoff)), int(y*(arcRadius+lineLength+textoff)), relName+": "+m.name, "text-anchor: "+textAnchor)
		}
	}
	canvas.Gend()
	canvas.End()
	return f.Close()
}
