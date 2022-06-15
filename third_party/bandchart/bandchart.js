// Copyright 2021 Observable, Inc.
// Released under the ISC license.
// https://observablehq.com/@d3/band-chart

function BandChart(data, {
	defined,
	marginTop = 50, // top margin, in pixels
	marginRight = 15, // right margin, in pixels
	marginBottom = 50, // bottom margin, in pixels
	marginLeft = 30, // left margin, in pixels
	width = 480, // outer width, in pixels
	height = 240, // outer height, in pixels
	unit,
} = {}) {
	// Compute values.
	const C = d3.map(data, d => d.CommitHash);
	const X = d3.map(data, d => d.CommitDate);
	const Y = d3.map(data, d => d.Center);
	const Y1 = d3.map(data, d => d.Low);
	const Y2 = d3.map(data, d => d.High);
	const I = d3.range(X.length);
	if (defined === undefined) defined = (d, i) => !isNaN(X[i]) && !isNaN(Y1[i]) && !isNaN(Y2[i]);
	const D = d3.map(data, defined);

	const xRange = [marginLeft, width - marginRight]; // [left, right]
	const yRange = [height - marginBottom, marginTop]; // [bottom, top]

	// Compute default domains.
	let yDomain = d3.nice(...d3.extent([...Y1, ...Y2]), 10);
	// Don't show <2.5% up-close because it just looks extremely noisy.
	const minYDomain = [-0.025, 0.025];
	if (yDomain[0] > minYDomain[0]) {
		yDomain[0] = minYDomain[0];
	}
	if (yDomain[1] < minYDomain[1]) {
		yDomain[1] = minYDomain[1];
	}

	// Construct scales and axes.
	const xOrdTicks = d3.range(xRange[0], xRange[1], (xRange[1]-xRange[0])/(X.length-1));
	xOrdTicks.push(xRange[1]);
	const xScale = d3.scaleOrdinal(X, xOrdTicks);
	const yScale = d3.scaleLinear(yDomain, yRange);
	const yAxis = d3.axisLeft(yScale).ticks(height / 40, "+%");

	const svg = d3.create("svg")
		.attr("width", width)
		.attr("height", height)
		.attr("viewBox", [0, 0, width, height])
		.attr("style", "max-width: 100%; height: auto; height: intrinsic;");

	svg.append("g")
		.attr("transform", `translate(${marginLeft},0)`)
		.call(yAxis)
		.call(g => g.select(".domain").remove())
		.call(g => g.selectAll(".tick line").clone()
			.attr("x2", width - marginLeft - marginRight)
			.attr("stroke-opacity", 0.1))
		.call(g => g.append("text")
			.attr("x", (xRange[1]-xRange[0])/2)
			.attr("y", 40)
			.attr("fill", "currentColor")
			.attr("text-anchor", "middle")
			.attr("font-size", "20px")
			.attr("font-weight", "bold")
			.text(unit));

	const defs = svg.append("defs")

	const maxHalfColorValue = 0.10;
	const maxHalfColorOpacity = 0.25;

	// Draw top half.
	const goodColor = "blue";
	const badColor = "red";

	// By default, lower is better.
	var bottomColor = goodColor;
	var topColor = badColor;
	const higherIsBetter = {
		"B/s": true,
		"ops/s": true
	};
	if (unit in higherIsBetter) {
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
		.attr("y", yRange[0] + (yRange[0]-yRange[1])*0.12)
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
			.attr("xlink:href", (d, i) => "https://go.googlesource.com/go/+show/"+C[i])
			.append("rect")
				.attr("pointer-events", "all")
				.attr("x", (d, i) => {
					if (i == 0) {
						return xOrdTicks[i];
					}
					return xOrdTicks[i-1]+(xOrdTicks[i]-xOrdTicks[i-1])/2;
				})
				.attr("y", marginTop)
				.attr("width", (d, i) => {
					if (i == 0 || i == X.length-1) {
						return (xOrdTicks[1]-xOrdTicks[0]) / 2;
					}
					return xOrdTicks[1]-xOrdTicks[0];
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
						)
						.call(g => g.append('text')
							.attr("x", (() => {
								let base = xScale(X[i]);
								if (base < marginLeft+100) {
									base += 10;
								} else if (base > width-marginRight-100) {
									base -= 10;
								}
								return base;
							})())
							.attr("y", (() => {
								let base = yScale(Y[i]);
								if (base < marginTop+100) {
									base += 30;
								} else if (base > height-marginBottom-100) {
									base -= 30;
								}
								return base;
							}))
							.attr("pointer-events", "none")
							.attr("fill", "currentColor")
							.attr("text-anchor", (() => {
								let base = xScale(X[i]);
								if (base < marginLeft+100) {
									return "start";
								} else if (base > width-marginRight-100) {
									return "end";
								}
								return "middle";
							})())
							.attr("font-family", "sans-serif")
							.attr("font-size", 12)
							.text(C[i].slice(0, 7) + " ("
								+ Intl.DateTimeFormat([], {
									dateStyle: "long",
									timeStyle: "short"
								}).format(X[i])
								+ ")")
						)
				})
				.on("mouseout", () => svg.selectAll('.tooltip').remove());

	return svg.node();
}
