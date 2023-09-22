// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package owners

import (
	"golang.org/x/build/internal/gophers"
)

func gh(githubUsername string) Owner {
	p := gophers.GetPerson("@" + githubUsername)
	if p == nil {
		panic("person with GitHub username " + githubUsername + " does not exist in the golang.org/x/build/internal/gophers package")
	}
	return Owner{GitHubUsername: githubUsername, GerritEmail: p.Gerrit}
}

// archOsTeam returns the *Entry for an architecture or OS team at github
func archOsTeam(teamName string) *Entry {
	return &Entry{Primary: []Owner{gh("golang/" + teamName)}}
}

var (
	adonovan     = gh("adonovan")
	agl          = gh("agl")
	agnivade     = gh("agnivade")
	alexbrainman = gh("alexbrainman")
	amedee       = gh("cagedmantis")
	austin       = gh("aclements")
	bcmills      = gh("bcmills")
	bradfitz     = gh("bradfitz")
	cherryyz     = gh("cherrymui")
	codyoss      = gh("codyoss")
	dmitshur     = gh("dmitshur")
	danderson    = gh("danderson")
	drakkan      = gh("drakkan")
	drchase      = gh("dr2chase")
	dvyukov      = gh("dvyukov")
	eliben       = gh("eliben")
	filippo      = gh("FiloSottile")
	findleyr     = gh("findleyr")
	gri          = gh("griesemer")
	heschi       = gh("heschi")
	hyangah      = gh("hyangah")
	iant         = gh("ianlancetaylor")
	iancottrell  = gh("ianthehat")
	jamalc       = gh("jamalc")
	jba          = gh("jba")
	jbd          = gh("rakyll")
	joetsai      = gh("dsnet")
	julieqiu     = gh("julieqiu")
	kardianos    = gh("kardianos")
	katie        = gh("katiehockman")
	kevinburke   = gh("kevinburke")
	kele         = gh("kele")
	khr          = gh("randall77")
	martisch     = gh("martisch")
	matloob      = gh("matloob")
	mdempsky     = gh("mdempsky")
	mdlayher     = gh("mdlayher")
	minux        = gh("minux")
	mknyszek     = gh("mknyszek")
	mpvl         = gh("mpvl")
	mvdan        = gh("mvdan")
	mwhudson     = gh("mwhudson")
	neelance     = gh("neelance")
	neild        = gh("neild")
	nigeltao     = gh("nigeltao")
	prattmic     = gh("prattmic")
	pjw          = gh("pjweinb")
	r            = gh("robpike")
	rakoczy      = gh("toothrot")
	roland       = gh("rolandshoemaker")
	rsc          = gh("rsc")
	sameer       = gh("Sajmani")
	shinfan      = gh("shinfan")
	suzmue       = gh("suzmue")
	taking       = gh("timothy-king")
	thanm        = gh("thanm")
	tklauser     = gh("tklauser")
	tombergan    = gh("tombergan")
	zpavlinovic  = gh("zpavlinovic")

	compilerTeam = gh("golang/compiler")
	fuzzingTeam  = gh("golang/fuzzing")
	pkgsiteTeam  = gh("golang/pkgsite")
	releaseTeam  = gh("golang/release")
	runtimeTeam  = gh("golang/runtime")
	securityTeam = gh("golang/security")
	toolsTeam    = gh("golang/tools-team")
	vulndbTeam   = gh("golang/vulndb")
)

// entries is a map of <repo name>/<path>, <domain>, or <branch> to Owner
// entries. For <repo name>/<path>, there is an implicit prefix of
// go.googlesource.com. This map should not be modified at runtime.
var entries = map[string]*Entry{
	// Go standard library.
	"go/src/archive/tar": {
		Primary: []Owner{joetsai},
	},
	"go/src/archive/zip": {
		Primary:   []Owner{joetsai},
		Secondary: []Owner{bradfitz},
	},
	"go/src/bufio": {
		Primary:   []Owner{},
		Secondary: []Owner{gri, bradfitz, iant},
	},
	"go/src/bytes": {
		Primary:   []Owner{},
		Secondary: []Owner{bradfitz, iant},
	},
	"go/src/cmd/asm": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{cherryyz},
	},
	"go/src/cmd/compile": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{khr, gri, mdempsky, martisch},
	},
	"go/src/cmd/compile/internal/amd64": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{khr, rsc, drchase, cherryyz, martisch},
	},
	"go/src/cmd/compile/internal/arm": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{khr, rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/arm64": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{khr, rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/mips": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{khr, rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/mips64": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{khr, rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/ppc64": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{khr, rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/s390x": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{khr, rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/x86": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{khr, rsc, drchase, cherryyz, martisch},
	},
	"go/src/cmd/compile/internal/syntax": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{gri, rsc, mdempsky},
	},
	"go/src/cmd/compile/internal/types": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{gri, mdempsky, rsc},
	},
	"go/src/cmd/compile/internal/types2": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{gri, findleyr},
	},
	"go/src/cmd/compile/internal/ssa": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{khr, martisch},
	},
	"go/src/cmd/compile/internal/wasm": {
		Primary:   []Owner{compilerTeam},
		Secondary: wasmOwners,
	},
	"go/src/cmd/cgo": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/covdata": {
		Primary:   []Owner{thanm},
		Secondary: []Owner{compilerTeam},
	},
	"go/src/cmd/cover": {
		Primary:   []Owner{thanm},
		Secondary: []Owner{compilerTeam},
	},
	"go/src/cmd/doc": {
		Primary:   []Owner{r},
		Secondary: []Owner{mvdan},
	},
	"go/src/cmd/go": {
		Primary:   []Owner{bcmills, matloob},
		Secondary: []Owner{rsc, iant},
	},
	"go/src/cmd/gofmt": {
		Primary:   []Owner{gri},
		Secondary: []Owner{mvdan},
	},
	"go/src/cmd/internal/archive": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/bio": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/codesign": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/cov": {
		Primary:   []Owner{thanm},
		Secondary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/dwarf": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/gcprog": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/goobj": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/notsha256": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/obj": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/objabi": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/objfile": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/src": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/sys": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/internal/obj/wasm": {
		Primary:   []Owner{compilerTeam},
		Secondary: wasmOwners,
	},
	"go/src/cmd/link": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{cherryyz, rsc, iant, mwhudson, thanm},
	},
	"go/src/cmd/link/internal/wasm": {
		Primary:   []Owner{compilerTeam},
		Secondary: wasmOwners,
	},
	"go/src/cmd/nm": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/objdump": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/pack": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/cmd/pprof": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{cherryyz},
	},
	"go/src/cmd/trace": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{mknyszek, prattmic},
	},
	"go/src/cmd/vet": {
		Primary:   []Owner{matloob},
		Secondary: []Owner{taking},
	},
	"go/src/cmp": {
		Primary:   []Owner{iant},
		Secondary: []Owner{eliben},
	},
	"go/src/compress/bzip2": {
		Primary:   []Owner{joetsai},
		Secondary: []Owner{mdempsky},
	},
	"go/src/compress/flate": {
		Primary:   []Owner{joetsai},
		Secondary: []Owner{mdempsky},
	},
	"go/src/compress/gzip": {
		Primary: []Owner{joetsai},
	},
	"go/src/compress/lzw": {
		Primary:   []Owner{joetsai},
		Secondary: []Owner{mdempsky},
	},
	"go/src/compress/zlib": {
		Primary:   []Owner{joetsai},
		Secondary: []Owner{mdempsky},
	},
	"go/src/container/heap": {
		Primary: []Owner{gri},
	},
	"go/src/container/list": {
		Primary: []Owner{gri},
	},
	"go/src/container/ring": {
		Primary: []Owner{gri},
	},
	"go/src/context": {
		Primary: []Owner{neild, sameer},
	},
	"go/src/crypto": {
		Primary: []Owner{filippo, roland, securityTeam},
	},
	"go/src/crypto/tls": {
		Primary:   []Owner{filippo, roland, securityTeam},
		Secondary: []Owner{kevinburke},
	},
	"go/src/database/sql": {
		Primary:   []Owner{bradfitz, kardianos},
		Secondary: []Owner{kevinburke},
	},
	"go/src/debug/dwarf": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{austin, thanm},
	},
	"go/src/debug/elf": {
		Primary:   []Owner{compilerTeam},
		Secondary: []Owner{iant},
	},
	"go/src/debug/pe": {
		Primary: []Owner{alexbrainman},
	},
	"go/src/embed": {
		Primary: []Owner{toolsTeam},
	},
	"go/src/encoding": {
		Primary: []Owner{rsc},
	},
	"go/src/encoding/asn1": {
		Primary: []Owner{filippo, roland, securityTeam},
	},
	"go/src/encoding/binary": {
		Primary: []Owner{gri},
	},
	"go/src/encoding/csv": {
		Primary:   []Owner{},
		Secondary: []Owner{joetsai, bradfitz, rsc},
	},
	"go/src/encoding/gob": {
		Primary: []Owner{r},
	},
	"go/src/encoding/json": {
		Primary:   []Owner{rsc},
		Secondary: []Owner{joetsai, bradfitz, mvdan},
	},
	"go/src/encoding/xml": {
		Primary: []Owner{rsc},
	},
	"go/src/expvar": {
		Primary:   []Owner{},
		Secondary: []Owner{bradfitz},
	},
	"go/src/flag": {
		Primary: []Owner{r},
	},
	"go/src/fmt": {
		Primary:   []Owner{r},
		Secondary: []Owner{martisch},
	},
	"go/src/go/ast": {
		Primary: []Owner{gri},
	},
	"go/src/go/build": {
		Primary: []Owner{rsc},
	},
	"go/src/go/constant": {
		Primary: []Owner{gri},
	},
	"go/src/go/doc": {
		Primary:   []Owner{gri},
		Secondary: []Owner{agnivade},
	},
	"go/src/go/format": {
		Primary:   []Owner{gri},
		Secondary: []Owner{mvdan},
	},
	"go/src/go/importer": {
		Primary: []Owner{gri, adonovan},
	},
	"go/src/go/internal/gccgoimporter": {
		Primary: []Owner{gri, iant},
	},
	"go/src/go/internal/gcimporter": {
		Primary: []Owner{gri},
	},
	// go/packages doesn't exist yet, but x/tools/go/packages has been proposed to
	// move there and many issues already refer to the new path.
	"go/src/go/packages": {
		Primary: []Owner{matloob},
	},
	"go/src/go/parser": {
		Primary: []Owner{gri},
	},
	"go/src/go/printer": {
		Primary:   []Owner{gri},
		Secondary: []Owner{mvdan},
	},
	"go/src/go/scanner": {
		Primary: []Owner{gri},
	},
	"go/src/go/token": {
		Primary: []Owner{gri},
	},
	"go/src/go/types": {
		Primary: []Owner{gri, findleyr},
	},
	"go/src/hash": {
		Primary: []Owner{securityTeam},
	},
	"go/src/hash/maphash": {
		Primary: []Owner{khr},
	},
	"go/src/html": {
		Primary: []Owner{securityTeam},
	},
	"go/src/html/template": {
		Primary: []Owner{securityTeam},
	},
	"go/src/image": {
		Primary:   []Owner{nigeltao},
		Secondary: []Owner{r},
	},
	"go/src/index/suffixarray": {
		Primary: []Owner{gri},
	},
	"go/src/internal/abi": {
		Primary:   []Owner{compilerTeam, runtimeTeam},
		Secondary: []Owner{mknyszek, cherryyz},
	},
	"go/src/internal/buildcfg": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/internal/bytealg": {
		Primary: []Owner{khr},
	},
	"go/src/internal/cpu": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{khr, martisch},
	},
	"go/src/internal/coverage": {
		Primary:   []Owner{thanm},
		Secondary: []Owner{compilerTeam},
	},
	"go/src/internal/fuzz": {
		Primary:   []Owner{fuzzingTeam},
		Secondary: []Owner{katie, roland},
	},
	"go/src/internal/goarch": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{mknyszek, prattmic},
	},
	"go/src/internal/godebug": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{mknyszek, prattmic},
	},
	"go/src/internal/goexperiment": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{austin, mknyszek},
	},
	"go/src/internal/goos": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{mknyszek, prattmic},
	},
	"go/src/internal/pkgbits": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/internal/poll": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{iant, prattmic},
	},
	"go/src/internal/profile": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{cherryyz, prattmic},
	},
	"go/src/internal/race": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{dvyukov, iant},
	},
	"go/src/internal/reflectlite": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{rsc, iant},
	},
	"go/src/internal/singleflight": {
		Primary: []Owner{bradfitz, iant},
	},
	"go/src/internal/syscall/unix": {
		Primary:   []Owner{iant, bradfitz},
		Secondary: []Owner{tklauser},
	},
	"go/src/internal/syscall/windows": {
		Primary:   []Owner{alexbrainman},
		Secondary: []Owner{bradfitz},
	},
	"go/src/internal/syscall/windows/registry": {
		Primary:   []Owner{alexbrainman},
		Secondary: []Owner{bradfitz},
	},
	"go/src/internal/syscall/windows/sysdll": {
		Primary:   []Owner{alexbrainman},
		Secondary: []Owner{bradfitz},
	},
	"go/src/internal/testenv": {
		Primary: []Owner{bradfitz, iant},
	},
	"go/src/internal/trace": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{mknyszek, prattmic},
	},
	"go/src/internal/xcoff": {
		Primary: []Owner{compilerTeam},
	},
	"go/src/io": {
		Primary:   []Owner{gri},
		Secondary: []Owner{iant, bradfitz},
	},
	"go/src/log": {
		Primary: []Owner{r},
	},
	"go/src/log/slog": {
		Primary: []Owner{jba},
	},
	"go/src/maps": {
		Primary: []Owner{iant},
	},
	"go/src/math": {
		Primary: []Owner{gri, rsc},
	},
	"go/src/math/big": {
		Primary:   []Owner{gri, securityTeam},
		Secondary: []Owner{filippo, roland},
	},
	"go/src/math/bits": {
		Primary:   []Owner{gri},
		Secondary: []Owner{khr, filippo, securityTeam},
	},
	"go/src/math/rand": {
		Primary:   []Owner{gri, rsc},
		Secondary: []Owner{filippo, securityTeam},
	},
	"go/src/mime": {
		Primary: []Owner{neild},
	},
	"go/src/mime/multipart": {
		Primary: []Owner{neild, minux},
	},
	"go/src/mime/quotedprintable": {
		Primary: []Owner{neild, minux},
	},
	"go/src/net": {
		Primary: []Owner{iant, neild},
	},
	"go/src/net/http": {
		Primary:   []Owner{neild},
		Secondary: []Owner{rsc},
	},
	"go/src/net/http/pprof": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{cherryyz, rsc},
	},
	"go/src/net/internal/socktest": {
		Primary: []Owner{},
	},
	"go/src/net/mail": {
		Primary:   []Owner{},
		Secondary: []Owner{bradfitz},
	},
	"go/src/net/rpc": {
		Primary: []Owner{r},
	},
	"go/src/net/rpc/jsonrpc": {
		Primary: []Owner{r},
	},
	"go/src/net/smtp": {
		Primary:   []Owner{},
		Secondary: []Owner{bradfitz},
	},
	"go/src/net/textproto": {
		Primary: []Owner{bradfitz, rsc},
	},
	"go/src/net/url": {
		Primary: []Owner{neild, rsc},
	},
	"go/src/os": {
		Primary: []Owner{rsc, r, iant, bradfitz, gri},
	},
	"go/src/os/exec": {
		Primary: []Owner{bradfitz, iant},
	},
	"go/src/os/signal": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{iant, prattmic},
	},
	"go/src/os/user": {
		Primary:   []Owner{bradfitz},
		Secondary: []Owner{kevinburke},
	},
	"go/src/path": {
		Primary: []Owner{r, rsc},
	},
	"go/src/path/filepath": {
		Primary: []Owner{r, rsc},
	},
	"go/src/plugin": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{iant, cherryyz},
	},
	"go/src/reflect": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{rsc, iant},
	},
	"go/src/regexp": {
		Primary:   []Owner{rsc},
		Secondary: []Owner{matloob},
	},
	"go/src/regexp/syntax": {
		Primary: []Owner{rsc},
	},
	"go/src/runtime": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{austin, khr, mknyszek, prattmic, iant, dvyukov, martisch},
	},
	"go/src/runtime/cgo": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{iant, cherryyz},
	},
	"go/src/runtime/coverage": {
		Primary:   []Owner{thanm},
		Secondary: []Owner{compilerTeam},
	},
	"go/src/runtime/internal/atomic": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{austin, khr, mknyszek},
	},
	"go/src/runtime/internal/sys": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{austin, khr},
	},
	"go/src/runtime/internal/syscall": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{prattmic, mknyszek, austin},
	},
	"go/src/runtime/metrics": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{mknyszek, prattmic},
	},
	"go/src/runtime/pprof": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{cherryyz, prattmic},
	},
	"go/src/runtime/race": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{dvyukov, iant},
	},
	"go/src/runtime/trace": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{mknyszek, prattmic, dvyukov},
	},
	"go/src/slices": {
		Primary:   []Owner{iant},
		Secondary: []Owner{eliben},
	},
	"go/src/sort": {
		Primary: []Owner{rsc, gri, iant, bradfitz},
	},
	"go/src/strconv": {
		Primary: []Owner{rsc, gri, iant, bradfitz},
	},
	"go/src/strings": {
		Primary:   []Owner{gri},
		Secondary: []Owner{iant, bradfitz},
	},
	"go/src/sync": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{rsc, iant, dvyukov, austin},
	},
	"go/src/sync/atomic": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{rsc, iant, dvyukov, austin},
	},
	"go/src/syscall": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{iant, bradfitz, tklauser},
	},
	"go/src/testing": {
		Primary:   []Owner{bcmills},
		Secondary: []Owner{mpvl},
	},
	"go/src/testing/quick": {
		Primary:   []Owner{},
		Secondary: []Owner{agl, katie},
	},
	"go/src/text/scanner": {
		Primary: []Owner{gri},
	},
	"go/src/text/tabwriter": {
		Primary: []Owner{gri},
	},
	"go/src/text/template": {
		Primary:   []Owner{r},
		Secondary: []Owner{mvdan},
	},
	"go/src/text/template/parse": {
		Primary:   []Owner{r},
		Secondary: []Owner{mvdan},
	},
	"go/src/time": {
		Primary: []Owner{rsc},
	},
	"go/src/unicode": {
		Primary:   []Owner{securityTeam, r},
		Secondary: []Owner{mpvl},
	},
	"go/src/unicode/utf16": {
		Primary: []Owner{r},
	},
	"go/src/unicode/utf8": {
		Primary: []Owner{r},
	},
	"go/src/unsafe": {
		Primary: []Owner{gri},
	},

	// Misc. additional tooling in the Go repository.
	"go/misc/wasm": {
		Primary: wasmOwners,
	},

	// golang.org/x/ repositories.
	"arch": {
		Primary: []Owner{cherryyz},
	},
	"benchmarks": {
		Primary: []Owner{runtimeTeam, releaseTeam},
	},
	"build": {
		Primary:   []Owner{releaseTeam},
		Secondary: []Owner{dmitshur, amedee, heschi},
	},
	"build/maintner/cmd/maintserve": {
		Primary: []Owner{dmitshur},
	},
	"crypto": {
		Primary: []Owner{filippo, roland, securityTeam},
	},
	"crypto/acme": {
		Primary:   []Owner{roland, securityTeam},
		Secondary: []Owner{filippo},
	},
	"crypto/acme/autocert": {
		Primary:   []Owner{bradfitz, roland, securityTeam},
		Secondary: []Owner{filippo},
	},
	"crypto/ssh": {
		Primary:   []Owner{drakkan, securityTeam},
		Secondary: []Owner{filippo, roland},
	},
	"debug": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{hyangah, khr},
	},
	"exp/vulncheck": {
		Primary: []Owner{vulndbTeam},
	},
	"mobile": {
		Primary: []Owner{hyangah},
	},
	"mod": {
		Primary: []Owner{bcmills, matloob},
	},
	"net": {
		Primary: []Owner{neild, iant},
	},
	"net/bpf": {
		Primary: []Owner{danderson, mdlayher},
	},
	"net/http": {
		Primary:   []Owner{neild},
		Secondary: []Owner{},
	},
	"net/http2": {
		Primary:   []Owner{neild, tombergan},
		Secondary: []Owner{},
	},
	"net/icmp": {
		Primary: []Owner{},
	},
	"net/ipv4": {
		Primary: []Owner{iant},
	},
	"net/ipv6": {
		Primary: []Owner{iant},
	},
	"oauth2": {
		Secondary: []Owner{jbd, shinfan, codyoss},
	},
	"perf": {
		Primary: []Owner{runtimeTeam, releaseTeam},
	},
	"review": {
		Secondary: []Owner{kevinburke},
	},
	"sync": {
		Primary: []Owner{bcmills},
	},
	"sys/unix": {
		Primary:   []Owner{runtimeTeam},
		Secondary: []Owner{iant, bradfitz, tklauser},
	},
	"sys/windows": {
		Primary:   []Owner{runtimeTeam, alexbrainman},
		Secondary: []Owner{bradfitz},
	},
	"text": {
		Primary: []Owner{mpvl},
	},
	"telemetry": {
		Primary:   []Owner{pjw, hyangah, findleyr},
		Secondary: []Owner{toolsTeam},
	},
	// default owners of x/tools/...
	"tools": {
		// for issue triage.
		Primary: []Owner{toolsTeam},
	},
	"tools/cmd/bundle": {
		Primary: []Owner{adonovan},
	},
	"tools/cmd/auth": {
		Primary:   []Owner{bcmills},
		Secondary: []Owner{matloob},
	},
	"tools/cmd/godoc": {
		Secondary: []Owner{agnivade, bradfitz, gri, kevinburke},
	},
	"tools/cmd/goimports": {
		Primary:   []Owner{heschi},
		Secondary: []Owner{bradfitz},
	},
	"tools/cmd/present2md": {
		Primary: []Owner{rsc},
	},
	"tools/cmd/stringer": {
		Secondary: []Owner{mvdan},
	},
	"tools/go/analysis": {
		Primary:   []Owner{taking},
		Secondary: []Owner{matloob, zpavlinovic},
	},
	"tools/go/ast": {
		Primary:   []Owner{gri},
		Secondary: []Owner{dmitshur},
	},
	"tools/go/buildutil": {
		Primary:   []Owner{bcmills, matloob},
		Secondary: []Owner{dmitshur},
	},
	"tools/go/callgraph": {
		Primary:   []Owner{zpavlinovic},
		Secondary: []Owner{taking, toolsTeam},
	},
	"tools/go/gcexportdata": {
		Primary:   []Owner{gri, findleyr},
		Secondary: []Owner{toolsTeam},
	},
	"tools/go/internal/gcimporter": {
		Primary:   []Owner{gri, findleyr},
		Secondary: []Owner{toolsTeam},
	},
	"tools/go/internal/packagesdriver": {
		Primary: []Owner{matloob},
	},
	"tools/go/loader": {
		Primary: []Owner{matloob},
	},
	"tools/go/packages": {
		Primary: []Owner{matloob},
	},
	"tools/go/ssa": {
		Primary:   []Owner{taking},
		Secondary: []Owner{findleyr},
	},
	"tools/go/vcs": {
		Secondary: []Owner{bcmills, matloob},
	},
	"tools/imports": {
		Primary: []Owner{heschi},
	},
	"tools/internal/analysisinternal": {
		Primary:   []Owner{matloob},
		Secondary: []Owner{toolsTeam},
	},
	"tools/internal/apidiff": {
		Primary:   []Owner{jba},
		Secondary: []Owner{matloob, bcmills},
	},
	"tools/internal/fastwalk": {
		Primary: []Owner{heschi},
	},
	"tools/internal/gocommand": {
		Primary: []Owner{heschi},
	},
	"tools/internal/gopathwalk": {
		Primary: []Owner{heschi},
	},
	"tools/internal/imports": {
		Primary: []Owner{heschi},
	},
	"tools/internal/jsonrpc2": {
		Primary:   []Owner{iancottrell},
		Secondary: []Owner{findleyr, jba},
	},
	"tools/internal/testenv": {
		Primary: []Owner{bcmills},
	},
	"tools/internal/tool": {
		Primary: []Owner{iancottrell},
	},
	"tools/internal/xcontext": {
		Primary: []Owner{iancottrell},
	},
	"tools/playground": {
		Primary: []Owner{rakoczy},
	},
	"tools/present": {
		Primary: []Owner{rsc},
	},
	"tools/refactor": {
		Primary:   []Owner{toolsTeam},
		Secondary: []Owner{findleyr, suzmue},
	},
	"tools/txtar": {
		Primary: []Owner{bcmills, matloob},
	},
	"pkgsite": {
		Primary: []Owner{pkgsiteTeam},
	},
	"playground": {
		Primary: []Owner{rakoczy},
	},
	"vuln": {
		Primary: []Owner{vulndbTeam},
	},
	"vulndb": {
		Primary: []Owner{vulndbTeam},
	},
	"website": {
		Primary: []Owner{toolsTeam},
	},
	"website/cmd/admingolangorg": {
		Secondary: []Owner{dmitshur},
	},
	"website/cmd/golangorg": {
		Secondary: []Owner{dmitshur},
	},
	"website/internal/dl": {
		Primary: []Owner{dmitshur},
	},
	"website/internal/history": {
		Primary: []Owner{dmitshur},
	},

	// Misc. other Go repositories.
	"gccgo": {
		Primary:   []Owner{iant},
		Secondary: []Owner{thanm, cherryyz},
	},
	"gofrontend": {
		Primary:   []Owner{iant},
		Secondary: []Owner{thanm},
	},
	"gollvm": {
		Primary:   []Owner{thanm},
		Secondary: []Owner{cherryyz},
	},
	"vscode-go": {
		Primary: []Owner{toolsTeam},
	},

	// These components are domains, not Go packages.
	"index.golang.org": modProxyOwners,
	"proxy.golang.org": modProxyOwners,
	"sum.golang.org":   modProxyOwners,
}

// archOses is a map of <architecture> or <OS> to Owner entries,
// used in the same way as entries above.
// This map should not be modified at runtime.
var archOses = map[string]*Entry{
	// OSes and architectures have teams.
	// OSes.  There is no team for "linux"
	"aix":       archOsTeam("aix"),
	"android":   archOsTeam("android"),
	"darwin":    archOsTeam("darwin"),
	"dragonfly": archOsTeam("dragonfly"),
	"freebsd":   archOsTeam("freebsd"),
	"illumos":   archOsTeam("illumos"),
	"ios":       archOsTeam("ios"),
	"js":        archOsTeam("js"),
	"netbsd":    archOsTeam("netbsd"),
	"openbsd":   archOsTeam("openbsd"),
	"plan9":     archOsTeam("plan9"),
	"solaris":   archOsTeam("solaris"), // team is empty as of 2022-10
	"wasip1":    archOsTeam("wasm"),
	"windows":   archOsTeam("windows"),

	// Architectures.  There is no team for "x86" or "amd64".
	"arm":     archOsTeam("arm"),
	"arm64":   archOsTeam("arm"),
	"mips":    archOsTeam("mips"),
	"mips64":  archOsTeam("mips"),
	"ppc64":   archOsTeam("ppc64"),
	"riscv64": archOsTeam("riscv64"),
	"loong64": archOsTeam("loong64"),
	"s390x":   archOsTeam("s390x"),
	"wasm":    archOsTeam("wasm"),
}

var wasmOwners = []Owner{neelance, cherryyz}

var modProxyOwners = &Entry{
	Primary:   []Owner{toolsTeam},
	Secondary: []Owner{suzmue, heschi, findleyr, hyangah},
}
