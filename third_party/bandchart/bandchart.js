// Copyright 2021 Observable, Inc.
// Released under the ISC license.
// https://observablehq.com/@d3/band-chart

function BandChart(data, {
	defined,
	marginTop = 30, // top margin, in pixels
	marginRight = 15, // right margin, in pixels
	marginBottom = 30, // bottom margin, in pixels
	marginLeft = 40, // left margin, in pixels
	width = 480, // outer width, in pixels
	height = 240, // outer height, in pixels
	benchmark,
	unit,
	platform,
	repository,
	minViewDeltaPercent,
	higherIsBetter,
	history,
} = {}) {
	// Compute a set of valid hashes so we can filter out any bad values.
	// This is to work around a bug where some test results have bad commits
	// attached to them.
	// TODO(mknyszek): Consider doing this data cleaning server-side.
	let historySet = new Set();
	for (let i = 0; i < history.length; i++) {
		historySet.add(history[i].Hash);
	}
	data = data.filter(d => historySet.has(d.CommitHash));

	// Compute values.
	const CT = d3.map(data, d => d.CommitDate);
	const X = d3.map(data, d => d.CommitHash);
	const Y = d3.map(data, d => d.Center);
	const Y1 = d3.map(data, d => d.Low);
	const Y2 = d3.map(data, d => d.High);
	const I = d3.range(X.length);
	if (defined === undefined) defined = (d, i) => !isNaN(Y1[i]) && !isNaN(Y2[i]);
	const D = d3.map(data, defined);

	const xRange = [marginLeft, width - marginRight]; // [left, right]
	const yRange = [height - marginBottom, marginTop]; // [bottom, top]

	// Compute default domains.
	let yDomain = d3.nice(...d3.extent([...Y1, ...Y2]), 10);

	// Determine Y domain.
	//
	// Three cases:
	// (1) All data is above Y=0 line.
	// (2) All data is below Y=0 line.
	// (3) Data crosses the Y=0 line.
	//
	// For (1) set the Y=0 line as the bottom of the domain.
	// For (2) set it at the top.
	// For (3) make sure the Y=0 line is in the middle.
	//
	// Finally, make sure we don't get closer than minViewDeltaPercent,
	// because otherwise it just looks really noisy.
	const minYDomain = [-minViewDeltaPercent, minViewDeltaPercent];
	if (yDomain[0] > 0) {
		// (1)
		yDomain[0] = 0;
		if (yDomain[1] < minYDomain[1]) {
			yDomain[1] = minYDomain[1];
		}
	} else if (yDomain[1] < 0) {
		// (2)
		yDomain[1] = 0;
		if (yDomain[0] > minYDomain[0]) {
			yDomain[0] = minYDomain[0];
		}
	} else {
		// (3)
		if (Math.abs(yDomain[1]) > Math.abs(yDomain[0])) {
			yDomain[0] = -Math.abs(yDomain[1]);
		} else {
			yDomain[1] = Math.abs(yDomain[0]);
		}
		if (yDomain[0] > minYDomain[0]) {
			yDomain[0] = minYDomain[0];
		}
		if (yDomain[1] < minYDomain[1]) {
			yDomain[1] = minYDomain[1];
		}
	}

	// Construct scales and axes.
	const xOrdTicks = d3.range(xRange[0], xRange[1], (xRange[1]-xRange[0])/(history.length-1));
	xOrdTicks.push(xRange[1]);
	const xScale = d3.scaleOrdinal(d3.map(history, d => d.Hash), xOrdTicks);
	const yScale = d3.scaleLinear(yDomain, yRange);
	const yAxis = d3.axisLeft(yScale).ticks(height / 40, "+%");

	const svg = d3.create("svg")
		.attr("width", width)
		.attr("height", height)
		.attr("viewBox", [0, 0, width, height])
		.attr("style", "max-width: 100%; height: auto; height: intrinsic;");

	// Chart area background color.
	svg.append("rect")
		.attr("fill", "white")
		.attr("x", xRange[0])
		.attr("y", yRange[1])
		.attr("width", xRange[1] - xRange[0])
		.attr("height", yRange[0] - yRange[1]);

	// Set up the params for the link to the unit page.
	let unitLinkParams = new URLSearchParams(window.location.search);
	unitLinkParams.set("unit", unit);
	unitLinkParams.set("platform", platform);
	unitLinkParams.set("benchmark", benchmark);

	// Title (unit).
	svg.append("g")
		.attr("transform", `translate(${marginLeft},0)`)
		.call(yAxis)
		.call(g => g.select(".domain").remove())
		.call(g => g.selectAll(".tick line").clone()
			.attr("x2", width - marginLeft - marginRight)
			.attr("stroke-opacity", 0.1))
		.call(g => g.append("a")
			.attr("xlink:href", "?" + unitLinkParams.toString())
			.append("text")
				.attr("x", xRange[0]-40)
				.attr("y", 24)
				.attr("fill", "currentColor")
				.attr("text-anchor", "start")
				.attr("font-size", "16px")
				.text(unit));

	const defs = svg.append("defs")

	const maxHalfColorValue = 0.10;
	const maxHalfColorOpacity = 0.5;

	// Draw top half.
	const goodColor = "#005AB5";
	const badColor = "#DC3220";

	// By default, lower is better.
	var bottomColor = goodColor;
	var topColor = badColor;
	if (higherIsBetter) {
		bottomColor = badColor;
		topColor = goodColor;
	}

	// IDs, even within SVGs, are shared across the entire page. (what?)
	// So, at least try to avoid a collision.
	const gradientIDSuffix = Math.random()*10000000.0;

	const topGradient = defs.append("linearGradient")
		.attr("id", "topGradient"+gradientIDSuffix)
		.attr("x1", "0%")
		.attr("x2", "0%")
		.attr("y1", "100%")
		.attr("y2", "0%");
	topGradient.append("stop")
		.attr("offset", "0%")
		.style("stop-color", topColor)
		.style("stop-opacity", 0);
	let topGStopOpacity = maxHalfColorOpacity;
	let topGOffsetPercent = 100.0;
	if (yDomain[1] > maxHalfColorValue) {
		topGOffsetPercent *= maxHalfColorValue/yDomain[1];
	} else {
		topGStopOpacity *= yDomain[1]/maxHalfColorValue;
	}
	topGradient.append("stop")
		.attr("offset", topGOffsetPercent+"%")
		.style("stop-color", topColor)
		.style("stop-opacity", topGStopOpacity);

	const bottomGradient = defs.append("linearGradient")
		.attr("id", "bottomGradient"+gradientIDSuffix)
		.attr("x1", "0%")
		.attr("x2", "0%")
		.attr("y1", "0%")
		.attr("y2", "100%");
	bottomGradient.append("stop")
		.attr("offset", "0%")
		.style("stop-color", bottomColor)
		.style("stop-opacity", 0);
	let bottomGStopOpacity = maxHalfColorOpacity;
	let bottomGOffsetPercent = 100.0;
	if (yDomain[0] < -maxHalfColorValue) {
		bottomGOffsetPercent *= -maxHalfColorValue/yDomain[0];
	} else {
		bottomGStopOpacity *= -yDomain[0]/maxHalfColorValue;
	}
	bottomGradient.append("stop")
		.attr("offset", bottomGOffsetPercent+"%")
		.style("stop-color", bottomColor)
		.style("stop-opacity", bottomGStopOpacity);

	// Top half color.
	svg.append("rect")
		.attr("fill", "url(#topGradient"+gradientIDSuffix+")")
		.attr("x", xRange[0])
		.attr("y", yScale(yDomain[1]))
		.attr("width", xRange[1] - xRange[0])
		.attr("height", (yDomain[1]/(yDomain[1]-yDomain[0]))*(height-marginTop-marginBottom));

	// Bottom half color.
	svg.append("rect")
		.attr("fill", "url(#bottomGradient"+gradientIDSuffix+")")
		.attr("x", xRange[0])
		.attr("y", yScale(0))
		.attr("width", xRange[1] - xRange[0])
		.attr("height", (-yDomain[0]/(yDomain[1]-yDomain[0]))*(height-marginTop-marginBottom));

	// Add a harder gridline for Y=0 to make it stand out.

	const line0 = d3.line()
		.defined(i => xRange[i])
		.x(i => xRange[i])
		.y(i => yScale(0))

	svg.append("path")
		.attr("fill", "none")
		.attr("stroke", "#999999")
		.attr("stroke-width", 2)
		.attr("d", line0(I))

	// Create CI area.

	const area = d3.area()
		.defined(i => D[i])
		.curve(d3.curveLinear)
		.x(i => xScale(X[i]))
		.y0(i => yScale(Y1[i]))
		.y1(i => yScale(Y2[i]));

	svg.append("path")
		.attr("fill", "black")
		.attr("opacity", 0.1)
		.attr("d", area(I));

	// Add X axis label.
	svg.append("text")
		.attr("x", xRange[0] + (xRange[1]-xRange[0])/2)
		.attr("y", yRange[0] + (yRange[0]-yRange[1])*0.10)
		.attr("fill", "currentColor")
		.attr("text-anchor", "middle")
		.attr("font-size", "12px")
		.text("Commits");

	// Create center line.

	const line = d3.line()
		.defined(i => D[i])
		.x(i => xScale(X[i]))
		.y(i => yScale(Y[i]))

	svg.append("path")
		.attr("fill", "none")
		.attr("stroke", "#212121")
		.attr("stroke-width", 2.5)
		.attr("d", line(I))

	// Divide the chart into columns and apply links and hover actions to them.
	svg.append("g")
		.attr("stroke", "#2074A0")
		.attr("stroke-opacity", 0)
		.attr("fill", "none")
		.selectAll("path")
		.data(I)
		.join("a")
			.attr("xlink:href", (d, i) => "?" + unitLinkParams.toString() + "#commit" + X[i])
			.append("rect")
				.attr("pointer-events", "all")
				.attr("x", (d, i) => {
					if (i == 0) {
						return xScale(X[i]);
					}
					return xScale(X[i]) - (xScale(X[i])-xScale(X[i-1]))/2;
				})
				.attr("y", marginTop)
				.attr("width", (d, i) => {
					if (i == 0) {
						return (xScale(X[i+1])-xScale(X[i]))/2;
					}
					if (i == X.length-1) {
						return (xScale(X[i])-xScale(X[i-1]))/2;
					}
					return (xScale(X[i])-xScale(X[i-1]))/2 + (xScale(X[i+1])-xScale(X[i]))/2;
				})
				.attr("height", height-marginTop-marginBottom)
				.on("mouseover", (d, i) => {
					svg.append('a')
						.attr("class", "tooltip")
						.call(g => g.append('line')
							.attr("x1", xScale(X[i]))
							.attr("y1", yRange[0])
							.attr("x2", xScale(X[i]))
							.attr("y2", yRange[1])
							.attr("stroke", "black")
							.attr("stroke-width", 1)
							.attr("stroke-dasharray", 2)
							.attr("opacity", 0.5)
							.attr("pointer-events", "none")
						)
						.call(g => g.append('text')
							// Point metadata (commit hash and date).
							// Above graph, top-right.
							.attr("x", xRange[1])
							.attr("y", yRange[1] - 6)
							.attr("pointer-events", "none")
							.attr("fill", "currentColor")
							.attr("text-anchor", "end")
							.attr("font-family", "sans-serif")
							.attr("font-size", 12)
							.text(X[i].slice(0, 7) + " ("
								+ Intl.DateTimeFormat([], {
									dateStyle: "long",
									timeStyle: "short"
								}).format(CT[i])
								+ ")")
						)
						.call(g => g.append('text')
							// Point center, low, high values.
							// Bottom-right corner, next to "Commits".
							.attr("x", xRange[1])
							.attr("y", yRange[0] + (yRange[0]-yRange[1])*0.10)
							.attr("pointer-events", "none")
							.attr("fill", "currentColor")
							.attr("text-anchor", "end")
							.attr("font-family", "sans-serif")
							.attr("font-size", 12)
							.text(Intl.NumberFormat([], {
								style: 'percent',
								signDisplay: 'always',
								minimumFractionDigits: 2,
							}).format(Y[i]) + " (" + Intl.NumberFormat([], {
								style: 'percent',
								signDisplay: 'always',
								minimumFractionDigits: 2,
							}).format(Y1[i]) + ", " + Intl.NumberFormat([], {
								style: 'percent',
								signDisplay: 'always',
								minimumFractionDigits: 2,
							}).format(Y2[i]) + ")")
						)
				})
				.on("mouseout", () => svg.selectAll('.tooltip').remove());

	return svg.node();
}
