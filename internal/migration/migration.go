// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package migration holds some knobs related to the migration from the
// now-legacy build infrastructure to the new LUCI build infrastructure.
package migration

const (
	StopLegacyMiscCompileTryBots = true
	StopInternalModuleProxy      = true
	StopEC2BuildletPool          = true

	// StopPortedBuilder controls whether ported builders should be stopped,
	// instead of just made invisible in the web UI.
	StopPortedBuilder = true
)

// BuildersPortedToLUCI lists coordinator builders that have been ported
// over to LUCI and don't need to continue to run. Their results will be
// hidden from the build.golang.org page and new builds won't be started
// if StopPortedBuilder (above) is true.
//
// See go.dev/issue/65913
// and go.dev/issue/63471.
var BuildersPortedToLUCI = map[string]bool{
	// macOS builders.
	"darwin-amd64-10_15":    true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-darwin-amd64_10.15.
	"darwin-amd64-11_0":     true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-darwin-amd64_11.
	"darwin-amd64-12_0":     true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-darwin-amd64_12.
	"darwin-amd64-13":       true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-darwin-amd64_13.
	"darwin-amd64-longtest": true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-darwin-amd64-longtest.
	"darwin-amd64-nocgo":    true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-darwin-amd64-nocgo.
	"darwin-amd64-race":     true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-darwin-amd64-race.
	"darwin-arm64-11":       true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-darwin-arm64_11.
	"darwin-arm64-12":       true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-darwin-arm64_12.

	// Linux builders (just those covering first-class ports).
	"linux-386":                     true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-386.
	"linux-386-longtest":            true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-386-longtest.
	"linux-386-clang":               true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-386-clang15 (a newer clang, but we won't be adding exactly -clang7 to LUCI by now).
	"linux-386-softfloat":           true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-386-softfloat.
	"linux-arm-aws":                 true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-arm.
	"linux-amd64":                   true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64.
	"linux-amd64-longtest":          true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-longtest.
	"linux-amd64-perf":              true, // Available in the form of multiple linux-amd64_…-perf_vs_… LUCI builders.
	"linux-amd64-race":              true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-race.
	"linux-amd64-longtest-race":     true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-longtest-race.
	"linux-amd64-racecompile":       true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-racecompile.
	"linux-amd64-nocgo":             true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-nocgo.
	"linux-amd64-noopt":             true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-noopt.
	"linux-amd64-clang":             true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-clang15 (a newer clang, but we won't be adding exactly -clang7 to LUCI by now).
	"linux-amd64-goamd64v3":         true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-goamd64v3.
	"linux-amd64-boringcrypto":      true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-boringcrypto.
	"linux-amd64-ssacheck":          true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-ssacheck.
	"linux-amd64-staticlockranking": true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-staticlockranking.
	"linux-amd64-newinliner":        true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-amd64-newinliner.
	"linux-arm64":                   true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-arm64.
	"linux-arm64-longtest":          true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-arm64-longtest.
	"linux-arm64-race":              true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-arm64-race.
	"linux-arm64-boringcrypto":      true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-arm64-boringcrypto.

	// Windows builders.
	"windows-386-2016":       true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-windows-386.
	"windows-amd64-2016":     true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-windows-amd64.
	"windows-amd64-longtest": true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-windows-amd64-longtest.
	"windows-amd64-race":     true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-windows-amd64-race.
	"windows-arm64-11":       true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-windows-arm64.

	"linux-riscv64-jsing":     true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-riscv64.
	"linux-riscv64-unmatched": true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-riscv64 (this builder is testing the same port as on the line above).

	"linux-ppc64le-buildlet":   true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-ppc64le_power8.
	"linux-ppc64le-power9osu":  true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-ppc64le_power9.
	"linux-ppc64le-power10osu": true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-ppc64le_power10.
	"linux-ppc64-sid-buildlet": true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-ppc64_power8.
	"linux-ppc64-sid-power10":  true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-ppc64_power10.
	"linux-loong64-3a5000":     true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-linux-loong64.

	"netbsd-arm64-bsiegert": true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-netbsd-arm64.
	"netbsd-arm-bsiegert":   true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-netbsd-arm.

	"openbsd-amd64-72": true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-openbsd-amd64.

	"solaris-amd64-oraclerel": true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-solaris-amd64.

	// WebAssembly builders.
	"js-wasm-node18":       true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-js-wasm.
	"wasip1-wasm-wasmedge": true, // Would be 'wasip1-wasm_wasmedge' but put off until go.dev/issue/60097 picks up activity.
	"wasip1-wasm-wasmer":   true, // Would be 'wasip1-wasm_wasmer' but put off until go.dev/issue/59907 picks up activity.
	"wasip1-wasm-wasmtime": true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-wasip1-wasm_wasmtime.
	"wasip1-wasm-wazero":   true, // Available as https://ci.chromium.org/p/golang/builders/ci/gotip-wasip1-wasm_wazero.
}
