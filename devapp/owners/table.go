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

var (
	adonovan     = gh("alandonovan")
	agl          = gh("agl")
	agnivade     = gh("agnivade")
	alexbrainman = gh("alexbrainman")
	amedee       = gh("cagedmantis")
	austin       = gh("aclements")
	bcmills      = gh("bcmills")
	bradfitz     = gh("bradfitz")
	carmen       = gh("Lyoness")
	cbro         = gh("broady")
	cherryyz     = gh("cherrymui")
	cnoellekb    = gh("cnoellekb")
	codyoss      = gh("codyoss")
	dmitshur     = gh("dmitshur")
	danderson    = gh("danderson")
	drchase      = gh("dr2chase")
	dvyukov      = gh("dvyukov")
	empijei      = gh("empijei")
	filippo      = gh("FiloSottile")
	findleyr     = gh("findleyr")
	guodongli    = gh("guodongli-google")
	gri          = gh("griesemer")
	hanwen       = gh("hanwen")
	heschi       = gh("heschi")
	hyangah      = gh("hyangah")
	iant         = gh("ianlancetaylor")
	iancottrell  = gh("ianthehat")
	jamalc       = gh("jamalc")
	jayconrod    = gh("jayconrod")
	jba          = gh("jba")
	jbd          = gh("rakyll")
	joetsai      = gh("dsnet")
	josharian    = gh("josharian")
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
	mikesamuel   = gh("mikesamuel")
	mikioh       = gh("mikioh")
	minux        = gh("minux")
	mknyszek     = gh("mknyszek")
	mpvl         = gh("mpvl")
	mvdan        = gh("mvdan")
	mwhudson     = gh("mwhudson")
	neelance     = gh("neelance")
	neild        = gh("neild")
	nigeltao     = gh("nigeltao")
	pearring     = gh("pearring")
	prattmic     = gh("prattmic")
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

	fuzzingTeam = gh("golang/fuzzing")
	pkgsiteTeam = gh("golang/pkgsite")
	toolsTeam   = gh("golang/tools-team")
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
		Primary: []Owner{cherryyz},
	},
	"go/src/cmd/compile": {
		Primary:   []Owner{khr, gri},
		Secondary: []Owner{josharian, mdempsky, martisch},
	},
	"go/src/cmd/compile/internal/amd64": {
		Primary:   []Owner{khr},
		Secondary: []Owner{josharian, rsc, drchase, cherryyz, martisch},
	},
	"go/src/cmd/compile/internal/arm": {
		Primary:   []Owner{khr},
		Secondary: []Owner{rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/arm64": {
		Primary:   []Owner{khr},
		Secondary: []Owner{rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/mips": {
		Primary:   []Owner{khr},
		Secondary: []Owner{rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/mips64": {
		Primary:   []Owner{khr},
		Secondary: []Owner{rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/ppc64": {
		Primary:   []Owner{khr},
		Secondary: []Owner{rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/s390x": {
		Primary:   []Owner{khr},
		Secondary: []Owner{rsc, drchase, cherryyz},
	},
	"go/src/cmd/compile/internal/x86": {
		Primary:   []Owner{khr},
		Secondary: []Owner{josharian, rsc, drchase, cherryyz, martisch},
	},
	"go/src/cmd/compile/internal/syntax": {
		Primary:   []Owner{gri},
		Secondary: []Owner{rsc, mdempsky},
	},
	"go/src/cmd/compile/internal/types": {
		Primary:   []Owner{gri},
		Secondary: []Owner{josharian, mdempsky, rsc},
	},
	"go/src/cmd/compile/internal/types2": {
		Primary: []Owner{gri, findleyr},
	},
	"go/src/cmd/compile/internal/ssa": {
		Primary:   []Owner{khr},
		Secondary: []Owner{josharian, martisch},
	},
	"go/src/cmd/compile/internal/wasm": wasmOwners,
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
	"go/src/cmd/internal/obj/wasm": wasmOwners,
	"go/src/cmd/link": {
		Primary:   []Owner{cherryyz, rsc, iant},
		Secondary: []Owner{mwhudson, thanm},
	},
	"go/src/cmd/link/internal/wasm": wasmOwners,
	"go/src/cmd/pprof": {
		Primary: []Owner{cherryyz},
	},
	"go/src/cmd/trace": {
		Primary: []Owner{mknyszek, prattmic},
	},
	"go/src/cmd/vet": {
		Primary:   []Owner{matloob},
		Secondary: []Owner{taking},
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
		Primary:   []Owner{filippo},
		Secondary: []Owner{agl, katie, roland},
	},
	"go/src/crypto/tls": {
		Primary:   []Owner{filippo},
		Secondary: []Owner{agl, katie, roland, kevinburke},
	},
	"go/src/database/sql": {
		Primary:   []Owner{bradfitz, kardianos},
		Secondary: []Owner{kevinburke},
	},
	"go/src/debug/dwarf": {
		Primary:   []Owner{austin},
		Secondary: []Owner{thanm},
	},
	"go/src/debug/elf": {
		Primary: []Owner{iant},
	},
	"go/src/debug/pe": {
		Primary: []Owner{alexbrainman},
	},
	"go/src/encoding": {
		Primary: []Owner{rsc},
	},
	"go/src/encoding/asn1": {
		Primary:   []Owner{filippo},
		Secondary: []Owner{agl, katie, roland},
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
		Primary:   []Owner{gri},
		Secondary: []Owner{josharian},
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
	"go/src/hash/maphash": {
		Primary: []Owner{khr},
	},
	"go/src/html/template": {
		Primary:   []Owner{empijei},
		Secondary: []Owner{kele},
	},
	"go/src/image": {
		Primary:   []Owner{nigeltao},
		Secondary: []Owner{r},
	},
	"go/src/index/suffixarray": {
		Primary: []Owner{gri},
	},
	"go/src/internal/bytealg": {
		Primary:   []Owner{khr},
		Secondary: []Owner{josharian},
	},
	"go/src/internal/cpu": {
		Primary: []Owner{khr, martisch},
	},
	"go/src/internal/fuzz": {
		Primary: []Owner{katie, roland},
	},
	"go/src/internal/profile": {
		Primary: []Owner{cherryyz},
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
		Primary:   []Owner{bradfitz, iant},
		Secondary: []Owner{josharian},
	},
	"go/src/internal/trace": {
		Primary: []Owner{mknyszek, prattmic},
	},
	"go/src/io": {
		Primary:   []Owner{gri},
		Secondary: []Owner{iant, bradfitz},
	},
	"go/src/log": {
		Primary: []Owner{r},
	},
	"go/src/math": {
		Primary: []Owner{gri, rsc},
	},
	"go/src/math/big": {
		Primary:   []Owner{gri, filippo},
		Secondary: []Owner{katie, roland},
	},
	"go/src/math/bits": {
		Primary:   []Owner{gri},
		Secondary: []Owner{khr, josharian, filippo},
	},
	"go/src/math/rand": {
		Primary:   []Owner{gri, rsc},
		Secondary: []Owner{josharian, filippo},
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
		Primary: []Owner{cherryyz, rsc},
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
		Primary: []Owner{iant},
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
		Primary:   []Owner{iant},
		Secondary: []Owner{cherryyz},
	},
	"go/src/reflect": {
		Primary: []Owner{rsc, iant},
	},
	"go/src/regexp": {
		Primary:   []Owner{rsc},
		Secondary: []Owner{matloob},
	},
	"go/src/regexp/syntax": {
		Primary: []Owner{rsc},
	},
	"go/src/runtime": {
		Primary:   []Owner{austin, khr, mknyszek, prattmic},
		Secondary: []Owner{iant, dvyukov, martisch},
	},
	"go/src/runtime/cgo": {
		Primary: []Owner{iant},
	},
	"go/src/runtime/internal/atomic": {
		Primary: []Owner{austin, khr},
	},
	"go/src/runtime/internal/sys": {
		Primary: []Owner{austin, khr},
	},
	"go/src/runtime/internal/syscall": {
		Primary: []Owner{prattmic, mknyszek, austin},
	},
	"go/src/runtime/pprof": {
		Primary: []Owner{cherryyz, prattmic},
	},
	"go/src/runtime/pprof/internal/protopprof": {
		Primary: []Owner{cherryyz},
	},
	"go/src/runtime/race": {
		Primary: []Owner{dvyukov},
	},
	"go/src/runtime/trace": {
		Primary: []Owner{mknyszek, prattmic, dvyukov},
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
		Primary: []Owner{rsc, iant, dvyukov, austin},
	},
	"go/src/sync/atomic": {
		Primary: []Owner{rsc, iant, dvyukov, austin},
	},
	"go/src/syscall": {
		Primary:   []Owner{iant, bradfitz},
		Secondary: []Owner{tklauser},
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
		Primary:   []Owner{r},
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
	"go/misc/wasm": wasmOwners,

	// golang.org/x/ repositories.
	"arch": {
		Primary: []Owner{cherryyz},
	},
	"build": {
		Primary: []Owner{dmitshur, bradfitz, amedee, heschi},
	},
	"build/maintner/cmd/maintserve": {
		Primary: []Owner{dmitshur},
	},
	"crypto": {
		Primary:   []Owner{filippo},
		Secondary: []Owner{agl, katie, roland},
	},
	"crypto/acme": {
		Primary:   []Owner{roland},
		Secondary: []Owner{filippo},
	},
	"crypto/acme/autocert": {
		Primary:   []Owner{bradfitz},
		Secondary: []Owner{roland, filippo},
	},
	"debug": {
		Secondary: []Owner{hyangah, khr},
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
		Primary:   []Owner{bradfitz},
		Secondary: []Owner{jbd, cbro, shinfan, codyoss},
	},
	"review": {
		Secondary: []Owner{kevinburke},
	},
	"sync": {
		Primary: []Owner{bcmills},
	},
	"sys/unix": {
		Primary: []Owner{iant, bradfitz, tklauser},
	},
	"sys/windows": {
		Primary: []Owner{alexbrainman, bradfitz},
	},
	"text": {
		Primary: []Owner{mpvl},
	},
	// default owners of x/tools/...
	"tools": {
		// for issue triage.
		Primary:   []Owner{toolsTeam},
		Secondary: []Owner{findleyr, hyangah},
	},
	"tools/cmd/compilebench": {
		Secondary: []Owner{josharian},
	},
	"tools/cmd/bundle": {
		Primary: []Owner{adonovan},
	},
	"tools/cmd/auth": {
		Primary:   []Owner{bcmills},
		Secondary: []Owner{matloob},
	},
	"tools/cmd/godoc": {
		Primary:   []Owner{dmitshur},
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
		Primary:   []Owner{matloob},
		Secondary: []Owner{taking, guodongli, zpavlinovic},
	},
	"tools/go/ast": {
		Primary:   []Owner{gri},
		Secondary: []Owner{josharian, dmitshur},
	},
	"tools/go/buildutil": {
		Primary:   []Owner{bcmills, matloob},
		Secondary: []Owner{dmitshur},
	},
	"tools/go/callgraph": {
		Primary:   []Owner{toolsTeam},
		Secondary: []Owner{taking, guodongli, zpavlinovic},
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
		Primary:   []Owner{dmitshur},
		Secondary: []Owner{bcmills, matloob},
	},
	"tools/godoc": {
		Primary: []Owner{dmitshur},
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
	"vulndb": {
		Primary: []Owner{filippo, katie, roland},
	},
	"website/cmd/admingolangorg": {
		Primary: []Owner{dmitshur},
	},
	"website/cmd/golangorg": {
		Primary: []Owner{dmitshur},
	},
	"website/internal/dl": {
		Primary: []Owner{dmitshur},
	},
	"website/internal/history": {
		Primary: []Owner{dmitshur},
	},

	// Branches in the Go repository.
	"dev.fuzz": {
		Primary: []Owner{fuzzingTeam},
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
		Primary:   []Owner{toolsTeam},
		Secondary: []Owner{hyangah, suzmue, jamalc},
	},

	// These components are domains, not Go packages.
	"learn.go.dev": {
		Primary: []Owner{carmen, pearring},
	},
	"go.dev": {
		Primary: []Owner{pearring},
	},
	"index.golang.org": modProxyOwners,
	"proxy.golang.org": modProxyOwners,
	"sum.golang.org":   modProxyOwners,
}

var wasmOwners = &Entry{
	Primary: []Owner{neelance, cherryyz},
}

var modProxyOwners = &Entry{
	Primary:   []Owner{katie, heschi, hyangah},
	Secondary: []Owner{findleyr},
}
