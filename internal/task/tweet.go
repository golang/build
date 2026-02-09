// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	goversion "go/version"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"math/rand"
	"mime/multipart"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/McKael/madon/v3"
	"github.com/dghubble/oauth1"
	"github.com/esimov/stackblur-go"
	"github.com/fflewddur/ltbsky"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/build/maintner/maintnerd/maintapi/version"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/net/context/ctxhttp"
)

// releaseTweet describes a tweet that announces a Go release.
type releaseTweet struct {
	// Kind is the kind of release being announced.
	Kind ReleaseKind

	// Version is the Go version that has been released.
	//
	// The version string must use the same format as Go tags. For example:
	//   - "go1.21rc2" for a pre-release
	//   - "go1.21.0" for a major Go release
	//   - "go1.21.1" for a minor Go release
	Version string
	// SecondaryVersion is an older Go version that was also released.
	// This only applies to minor releases when two releases are made.
	// For example, "go1.20.9".
	SecondaryVersion string

	// Security is an optional sentence describing security fixes
	// included in this release.
	//
	// The empty string means there are no security fixes to highlight.
	// Past examples:
	//   - "Includes a security fix for the Wasm port (CVE-2021-38297)."
	//   - "Includes a security fix for archive/zip (CVE-2021-39293)."
	//   - "Includes a security fix for crypto/tls (CVE-2021-34558)."
	//   - "Includes security fixes for archive/zip, net, net/http/httputil, and math/big packages."
	Security string

	// Announcement is the announcement URL.
	//
	// It's applicable to all release types other than major,
	// since major releases point to release notes instead.
	// For example, "https://groups.google.com/g/golang-announce/c/wB1fph5RpsE/m/ZGwOsStwAwAJ".
	Announcement string
}

type Poster interface {
	// Post posts a tweet with the given text, and returns the tweet URL.
	Post(text string) (tweetURL string, _ error)
	// PostTweet posts a tweet with the given text and PNG image,
	// both of which must be non-empty, and returns the tweet URL.
	//
	// ErrTweetTooLong error is returned if posting fails
	// due to the tweet text length exceeding Twitter's limit.
	PostTweet(text string, imagePNG []byte, altText string) (tweetURL string, _ error)
}

// SocialMediaTasks contains tasks related to the release tweet.
type SocialMediaTasks struct {
	// TwitterClient can be used to post a tweet.
	TwitterClient  Poster
	MastodonClient Poster
	BlueskyClient  Poster

	// OverrideGoBlogPostAtomURL can be set when you want to override the default Go
	// atom feed URL. This is useful for tests.
	OverrideGoBlogPostAtomURL string

	// RandomSeed is the pseudo-random number generator seed to use for presentational
	// choices, such as selecting one out of many available emoji or release archives.
	// The zero value means to use time.Now().UnixNano().
	RandomSeed int64
}

func (t SocialMediaTasks) textAndImage(ctx *workflow.TaskContext, kind ReleaseKind, published []Published, security string, announcement string) (tweetText string, imagePNG []byte, imageText string, err error) {
	if len(published) < 1 || len(published) > 2 {
		return "", nil, "", fmt.Errorf("got %d published Go releases, TweetRelease supports only 1 or 2 at once", len(published))
	}

	r := releaseTweet{
		Kind:         kind,
		Version:      published[0].Version,
		Security:     security,
		Announcement: announcement,
	}
	if len(published) == 2 {
		r.SecondaryVersion = published[1].Version
	}

	seed := t.RandomSeed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	rnd := rand.New(rand.NewSource(seed))

	// Generate tweet text.
	tweetText, err = r.tweetText(rnd)
	if err != nil {
		return "", nil, "", err
	}
	ctx.Printf("tweet text:\n%s\n", tweetText)

	// Generate tweet image.
	imagePNG, imageText, err = tweetImage(published[0], rnd)
	if err != nil {
		return "", nil, "", err
	}
	ctx.Printf("tweet image:\n%s\n", imageText)
	return tweetText, imagePNG, imageText, nil
}

// TweetRelease posts a tweet announcing that a Go release has been published.
// ErrTweetTooLong is returned if the inputs result in a tweet that's too long.
func (t SocialMediaTasks) TweetRelease(ctx *workflow.TaskContext, kind ReleaseKind, published []Published, security string, announcement string) (_ string, _ error) {
	tweetText, imagePNG, imageText, err := t.textAndImage(ctx, kind, published, security, announcement)
	if err != nil {
		return "", err
	}

	// Post a tweet via the Twitter API.
	if t.TwitterClient == nil {
		return "(dry-run)", nil
	}
	ctx.DisableRetries()
	return t.TwitterClient.PostTweet(tweetText, imagePNG, imageText)
}

// TrumpetRelease posts a tweet announcing that a Go release has been published.
func (t SocialMediaTasks) TrumpetRelease(ctx *workflow.TaskContext, kind ReleaseKind, published []Published, security string, announcement string) (_ string, _ error) {
	tweetText, imagePNG, imageText, err := t.textAndImage(ctx, kind, published, security, announcement)
	if err != nil {
		return "", err
	}

	// Post via the Mastodon API.
	if t.MastodonClient == nil {
		return "(dry-run)", nil
	}
	ctx.DisableRetries()
	return t.MastodonClient.PostTweet(tweetText, imagePNG, imageText)
}

// SkeetRelease posts an announcement to Bluesky that a Go release has been published.
func (t SocialMediaTasks) SkeetRelease(ctx *workflow.TaskContext, kind ReleaseKind, published []Published, security string, announcement string) (_ string, _ error) {
	postText, imagePNG, imageText, err := t.textAndImage(ctx, kind, published, security, announcement)
	if err != nil {
		return "", err
	}

	// When running tests, don't actually post
	if t.BlueskyClient == nil {
		return "(dry-run)", nil
	}
	ctx.DisableRetries()
	return t.BlueskyClient.PostTweet(postText, imagePNG, imageText)
}

func (t SocialMediaTasks) blogPostText(ctx *workflow.TaskContext, url, title, author string) (string, error) {
	b := struct {
		URL    string
		Title  string
		Author string
	}{
		URL:    url,
		Title:  title,
		Author: author,
	}
	tpl, err := template.New("").Parse(blogPostTmpl)
	if err != nil {
		return "", err
	}
	var tweetText bytes.Buffer
	if err := tpl.Execute(&tweetText, b); err != nil {
		return "", err
	}
	ctx.Printf("post text:\n%s\n", tweetText.String())
	return tweetText.String(), nil
}

// TweetBlogPost posts a tweet announcing a blog post.
func (t SocialMediaTasks) TweetBlogPost(ctx *workflow.TaskContext, bp BlogPost) (string, error) {
	tweetText, err := t.blogPostText(ctx, bp.URL, bp.Title, bp.Author)
	if err != nil {
		return "", err
	}
	if t.TwitterClient == nil {
		return "(dry-run)", nil
	}
	ctx.DisableRetries()
	return t.TwitterClient.Post(tweetText)
}

// TrumpetBlogPost posts a message on Mastodon announcing a blog post.
func (t SocialMediaTasks) TrumpetBlogPost(ctx *workflow.TaskContext, bp BlogPost) (string, error) {
	tweetText, err := t.blogPostText(ctx, bp.URL, bp.Title, bp.Author)
	if err != nil {
		return "", err
	}
	if t.MastodonClient == nil {
		return "(dry-run)", nil
	}
	ctx.DisableRetries()
	return t.MastodonClient.Post(tweetText)
}

// SkeetBlogPost posts a message on Bluesky announcing a blog post.
func (t SocialMediaTasks) SkeetBlogPost(ctx *workflow.TaskContext, bp BlogPost) (string, error) {
	tweetText, err := t.blogPostText(ctx, bp.URL, bp.Title, bp.Author)
	if err != nil {
		return "", err
	}
	if t.BlueskyClient == nil {
		return "(dry-run)", nil
	}
	ctx.DisableRetries()
	return t.BlueskyClient.Post(tweetText)
}

// tweetText generates the text to use in the announcement
// tweet for release r.
func (r *releaseTweet) tweetText(rnd *rand.Rand) (string, error) {
	// Parse the tweet text template
	// using rnd for emoji selection.
	t, err := template.New("").Funcs(template.FuncMap{
		"emoji": func(category string) (string, error) {
			es, ok := emoji[category]
			if !ok {
				return "", fmt.Errorf("unknown emoji category %q", category)
			}
			return es[rnd.Intn(len(es))], nil
		},

		// short and helpers below manipulate valid Go version strings
		// for the current needs of the tweet templates.
		"short": func(v string) string { return strings.TrimPrefix(v, "go") },
		// major extracts the major prefix of a valid Go version.
		// For example, major("go1.18.4") == "1.18".
		"major": func(v string) (string, error) {
			x, ok := version.Go1PointX(v)
			if !ok {
				return "", fmt.Errorf("internal error: version.Go1PointX(%q) is not ok", v)
			}
			return fmt.Sprintf("1.%d", x), nil
		},
		// build extracts the pre-release build number of a valid Go version.
		// For example, build("go1.19beta2") == "2".
		"build": func(v string) (string, error) {
			if _, after, ok := strings.Cut(v, "beta"); ok {
				return after, nil
			} else if _, after, ok := strings.Cut(v, "rc"); ok {
				return after, nil
			}
			return "", fmt.Errorf("internal error: unhandled pre-release Go version %q", v)
		},
	}).Parse(tweetTextTmpl)
	if err != nil {
		return "", err
	}

	// Select the appropriate template name.
	var name string
	switch r.Kind {
	case KindBeta:
		name = "beta"
	case KindRC:
		name = "rc"
	case KindMajor:
		name = "major"
	case KindMinor:
		name = "minor"
	default:
		return "", fmt.Errorf("unknown release kind: %v", r.Kind)
	}
	if r.SecondaryVersion != "" && name != "minor" {
		return "", fmt.Errorf("tweet template %q doesn't support more than one release; the SecondaryVersion field can only be used in minor releases", name)
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

const blogPostTmpl = `“{{.Title}}”{{with .Author}} by {{.}}{{end}} — {{.URL}}

#golang`

const tweetTextTmpl = `{{define "minor" -}}
{{emoji "release"}} Go {{.Version|short}} {{with .SecondaryVersion}}and {{.|short}} are{{else}}is{{end}} released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "announce"}} Announcement: {{.Announcement}}

{{emoji "download"}} Download: https://go.dev/dl/#{{.Version}}

#golang{{end}}


{{define "beta" -}}
{{emoji "beta-release"}} Go {{.Version|major}} Beta {{.Version|build}} is released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "try"}} Try it! File bugs! https://go.dev/issue/new

{{emoji "announce"}} Announcement: {{.Announcement}}

{{emoji "download"}} Download: https://go.dev/dl/#{{.Version}}

#golang{{end}}


{{define "rc" -}}
{{emoji "rc-release"}} Go {{.Version|major}} Release Candidate {{.Version|build}} is released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "run"}} Run it in dev! Run it in prod! File bugs! https://go.dev/issue/new

{{emoji "announce"}} Announcement: {{.Announcement}}

{{emoji "download"}} Download: https://go.dev/dl/#{{.Version}}

#golang{{end}}


{{define "major" -}}
{{emoji "release"}} Go {{.Version|short}} is released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "notes"}} Release notes: https://go.dev/doc/go{{.Version|major}}

{{emoji "download"}} Download: https://go.dev/dl/#{{.Version}}

#golang{{end}}`

// emoji is an atlas of emoji for different categories.
//
// The more often an emoji is included in a category,
// the more likely it is to be randomly chosen.
var emoji = map[string][]string{
	"release": {
		"🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳",
		"🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉",
		"🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊",
		"🌟", "🌟", "🌟", "🌟", "🌟", "🌟", "🌟", "🌟",
		"🎆", "🎆", "🎆", "🎆", "🎆", "🎆",
		"🆒",
		"🕶",
		"🤯",
		"🧨",
		"💃",
		"🐕",
		"👩🏽‍🔬",
		"🌞",
	},
	"beta-release": {
		"🧪", "🧪", "🧪", "🧪", "🧪", "🧪", "🧪", "🧪", "🧪", "🧪",
		"⚡️", "⚡️", "⚡️", "⚡️", "⚡️", "⚡️", "⚡️", "⚡️", "⚡️", "⚡️",
		"💥",
	},
	"rc-release": {
		"🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳",
		"🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉",
		"🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊",
		"🌞",
	},
	"security": {
		"🔐", "🔐", "🔐", "🔐", "🔐",
		"🔏", "🔏",
		"🔒",
	},
	"try": {
		"⚙️",
	},
	"run": {
		"🏃‍♀️",
		"🏃‍♂️",
		"🏖",
	},
	"announce": {
		"🗣", "🗣", "🗣", "🗣", "🗣", "🗣",
		"📣", "📣", "📣", "📣", "📣", "📣",
		"📢", "📢", "📢", "📢", "📢", "📢",
		"🔈", "🔈", "🔈", "🔈", "🔈",
		"📡", "📡", "📡", "📡",
		"📰",
	},
	"notes": {
		"📝", "📝", "📝", "📝", "📝",
		"🗒️", "🗒️", "🗒️", "🗒️", "🗒️",
		"📰",
	},
	"download": {
		"⬇️", "⬇️", "⬇️", "⬇️", "⬇️", "⬇️", "⬇️", "⬇️", "⬇️",
		"📦", "📦", "📦", "📦", "📦", "📦", "📦", "📦", "📦",
		"🗃",
		"🚚",
	},
}

// tweetImage generates an image to use in the announcement
// tweet for published. It returns the image encoded as PNG,
// and the text displayed in the image.
//
// tweetImage selects a random release archive to highlight.
func tweetImage(published Published, rnd *rand.Rand) (imagePNG []byte, imageText string, _ error) {
	a, err := pickRandomArchive(published, rnd)
	if err != nil {
		return nil, "", err
	}
	goarch := a.GOARCH()
	if goversion.Compare(a.Version, "go1.23") == -1 && a.OS != "linux" { // TODO: Delete this after Go 1.24.0 is out and this becomes dead code.
		goarch = strings.TrimSuffix(goarch, "v6l")
	}
	var buf bytes.Buffer
	if err := goCmdTmpl.Execute(&buf, map[string]string{
		"GoVer":    published.Version,
		"GOOS":     a.OS,
		"GOARCH":   goarch,
		"Filename": a.Filename,
		"ZeroSize": fmt.Sprintf("%*d", digits(a.Size), 0),
		"HalfSize": fmt.Sprintf("%*d", digits(a.Size), a.Size/2),
		"FullSize": fmt.Sprint(a.Size),
	}); err != nil {
		return nil, "", err
	}
	imageText = buf.String()
	m, err := drawTerminal(imageText)
	if err != nil {
		return nil, "", err
	}
	// Encode the image in PNG format.
	buf.Reset()
	err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, m)
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), imageText, nil
}

var goCmdTmpl = template.Must(template.New("").Parse(`$ go install golang.org/dl/{{.GoVer}}@latest
$ {{.GoVer}} download
Downloaded   0.0% ({{.ZeroSize}} / {{.FullSize}} bytes) ...
Downloaded  50.0% ({{.HalfSize}} / {{.FullSize}} bytes) ...
Downloaded 100.0% ({{.FullSize}} / {{.FullSize}} bytes)
Unpacking {{.Filename}} ...
Success. You may now run '{{.GoVer}}'
$ {{.GoVer}} version
go version {{.GoVer}} {{.GOOS}}/{{.GOARCH}}`))

// digits reports the number of digits in the integer i. i must be non-zero.
func digits(i int64) int {
	var n int
	for ; i != 0; i /= 10 {
		n++
	}
	return n
}

// pickRandomArchive picks one random release archive
// to showcase in an image showing sample output from
// 'go install golang.org/dl/...@latest'.
func pickRandomArchive(published Published, rnd *rand.Rand) (archive WebsiteFile, _ error) {
	var archives []WebsiteFile
	for _, f := range published.Files {
		if f.Kind != "archive" {
			// Not an archive type of file, skip. The golang.org/dl commands use archives only.
			continue
		}
		archives = append(archives, f)
	}
	if len(archives) == 0 {
		return WebsiteFile{}, fmt.Errorf("release version %q has 0 archive files", published.Version)
	}
	return archives[rnd.Intn(len(archives))], nil
}

// drawTerminal draws an image of a terminal window
// with the given text displayed.
func drawTerminal(text string) (image.Image, error) {
	// Load font from TTF data.
	f, err := opentype.Parse(gomono.TTF)
	if err != nil {
		return nil, err
	}

	// Keep image width within 900 px, so that Twitter doesn't convert it to a lossy JPEG.
	// See https://twittercommunity.com/t/upcoming-changes-to-png-image-support/118695.
	const width, height = 900, 520
	m := image.NewNRGBA(image.Rect(0, 0, width, height))

	// Background.
	draw.Draw(m, m.Bounds(), image.NewUniform(gopherBlue), image.Point{}, draw.Src)

	// Shadow.
	draw.DrawMask(m, m.Bounds(), image.NewUniform(shadowColor), image.Point{},
		roundedRect(image.Rect(50, 80, width-50, height-80).Add(image.Point{Y: 20}), 10), image.Point{}, draw.Over)

	// Blur.
	m, err = stackblur.Process(m, 80)
	if err != nil {
		return nil, err
	}

	// Terminal.
	draw.DrawMask(m, m.Bounds(), image.NewUniform(terminalColor), image.Point{},
		roundedRect(image.Rect(50, 80, width-50, height-80), 10), image.Point{}, draw.Over)

	// Text.
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 24, DPI: 72})
	if err != nil {
		return nil, err
	}
	d := font.Drawer{Dst: m, Src: image.White, Face: face}
	const lineHeight = 32
	for n, line := range strings.Split(text, "\n") {
		d.Dot = fixed.P(80, 135+n*lineHeight)
		d.DrawString(line)
	}

	return m, nil
}

// roundedRect returns a rounded rectangle with the specified border radius.
func roundedRect(r image.Rectangle, borderRadius int) image.Image {
	return roundedRectangle{
		r:  r,
		i:  r.Inset(borderRadius),
		br: borderRadius,
	}
}

type roundedRectangle struct {
	r  image.Rectangle // Outer bounds.
	i  image.Rectangle // Inner bounds, border radius away from outer.
	br int             // Border radius.
}

func (roundedRectangle) ColorModel() color.Model   { return color.Alpha16Model }
func (r roundedRectangle) Bounds() image.Rectangle { return r.r }
func (r roundedRectangle) At(x, y int) color.Color {
	switch {
	case x < r.i.Min.X && y < r.i.Min.Y:
		return circle(x-r.i.Min.X, y-r.i.Min.Y, r.br)
	case x > r.i.Max.X-1 && y < r.i.Min.Y:
		return circle(x-(r.i.Max.X-1), y-r.i.Min.Y, r.br)
	case x < r.i.Min.X && y > r.i.Max.Y-1:
		return circle(x-r.i.Min.X, y-(r.i.Max.Y-1), r.br)
	case x > r.i.Max.X-1 && y > r.i.Max.Y-1:
		return circle(x-(r.i.Max.X-1), y-(r.i.Max.Y-1), r.br)
	default:
		return color.Opaque
	}
}
func circle(x, y, r int) color.Alpha16 {
	xxyy := float64(x)*float64(x) + float64(y)*float64(y)
	if xxyy > float64((r+1)*(r+1)) {
		return color.Transparent
	} else if xxyy > float64(r*r) {
		return color.Alpha16{uint16(0xFFFF * (1 - math.Sqrt(xxyy) - float64(r)))}
	}
	return color.Opaque
}

var (
	// gopherBlue is the Gopher Blue primary color from the Go color palette.
	//
	// Reference: https://go.dev/s/brandbook.
	gopherBlue = color.NRGBA{0, 173, 216, 255} // #00add8.

	// terminalColor is the color used as the terminal color.
	terminalColor = color.NRGBA{52, 61, 70, 255} // #343d46.

	// shadowColor is the color used as the shadow color.
	shadowColor = color.NRGBA{0, 0, 0, 140} // #0000008c.
)

type realBlueskyClient struct {
	client *ltbsky.Client
}

func (c realBlueskyClient) Post(text string) (postURI string, _ error) {
	postBuilder := ltbsky.NewPostBuilder(text)
	postBuilder.AddLang("en")
	postURI, err := c.client.Post(postBuilder)
	if err != nil {
		return "", err
	}
	return postURI, nil
}

func (c realBlueskyClient) PostTweet(text string, imagePNG []byte, altText string) (postURI string, _ error) {
	postBuilder := ltbsky.NewPostBuilder(text)
	postBuilder.AddLang("en")
	postBuilder.AddImageFromBytes(imagePNG, altText)
	postURI, err := c.client.Post(postBuilder)
	if err != nil {
		return "", err
	}
	return postURI, nil
}

type realMastodonClient struct {
	client        *madon.Client
	testRecipient string
}

// Post implements the Poster interface.
func (c realMastodonClient) Post(text string) (tweetURL string, _ error) {
	visibility := "public"
	if c.testRecipient != "" {
		text = c.testRecipient + "\n" + text
		visibility = "direct"
	}
	status, err := c.client.PostStatus(madon.PostStatusParams{
		Text:        text,
		Visibility:  visibility,
		Sensitive:   false,
		SpoilerText: "",
	})
	if err != nil {
		return "", err
	}
	return status.URL, nil
}

// PostTweet posts a message to a Mastodon account, with specified text, image, and image alt text.
// If the "client" includes a non-empty test recipient, direct the message to that recipient in a
// "direct message", also known as a "private mention".
func (c realMastodonClient) PostTweet(text string, imagePNG []byte, altText string) (tweetURL string, _ error) {
	client := c.client

	visibility := "public"

	// For end-to-end hand testing, send a message to a designated recipient
	if c.testRecipient != "" {
		text = c.testRecipient + "\n" + text
		visibility = "direct"
	}

	// The documentation says that the name parameter can be empty, but at least one
	// Mastodon server disagrees.  Make the name match the media format, just in case
	// that matters.
	att, err := client.UploadMediaReader(bytes.NewReader(imagePNG), "upload.png", altText, "")
	if err != nil {
		return "", fmt.Errorf("upload failure: %w", err)
	}
	postParams := madon.PostStatusParams{
		Text:        text,
		MediaIDs:    []string{att.ID},
		Visibility:  visibility,
		Sensitive:   false,
		SpoilerText: "",
	}

	status, err := client.PostStatus(postParams)
	if err != nil {
		return "", fmt.Errorf("post failure: %w", err)
	}
	return status.URL, nil
}

type realTwitterClient struct {
	twitterAPI *http.Client
}

// Post implements the Poster interface.
func (c realTwitterClient) Post(text string) (string, error) {
	var tweetReq struct {
		Text string `json:"text"`
	}
	tweetReq.Text = text
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(tweetReq); err != nil {
		return "", err
	}
	resp, err := c.twitterAPI.Post("https://api.twitter.com/2/tweets", "application/json", &buf)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if isTweetTooLong(resp, body) {
			// A friendlier error for a common error type.
			return "", ErrTweetTooLong
		}
		return "", fmt.Errorf("POST /2/tweets: non-201 Created status code: %v body: %q", resp.Status, body)
	}
	var tweetResp struct {
		Data struct {
			ID string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&tweetResp); err != nil {
		return "", err
	}
	// Use a generic "username" in the URL since finding it needs another API call.
	// As long as the URL has this format, it will redirect to the canonical username.
	return "https://twitter.com/username/status/" + tweetResp.Data.ID, nil
}

// PostTweet implements the TweetTasks.TwitterClient interface.
func (c realTwitterClient) PostTweet(text string, imagePNG []byte, altText string) (tweetURL string, _ error) {
	// Make a Twitter API call to upload PNG to upload.twitter.com.
	// See https://developer.twitter.com/en/docs/twitter-api/v1/media/upload-media/api-reference/post-media-upload.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if f, err := w.CreateFormFile("media", "image.png"); err != nil {
		return "", err
	} else if _, err := f.Write(imagePNG); err != nil {
		return "", err
	} else if err := w.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, "https://upload.twitter.com/1.1/media/upload.json?media_category=tweet_image", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.twitterAPI.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("POST /1.1/media/upload.json: non-200 OK status code: %v body: %q", resp.Status, body)
	}
	var media struct {
		ID string `json:"media_id_string"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&media); err != nil {
		return "", err
	}

	// Make a Twitter API call to post a tweet with the uploaded image.
	// See https://developer.twitter.com/en/docs/twitter-api/tweets/manage-tweets/api-reference/post-tweets.
	var tweetReq struct {
		Text  string `json:"text"`
		Media struct {
			MediaIDs []string `json:"media_ids"`
		} `json:"media"`
	}
	tweetReq.Text, tweetReq.Media.MediaIDs = text, []string{media.ID}
	buf.Reset()
	if err := json.NewEncoder(&buf).Encode(tweetReq); err != nil {
		return "", err
	}
	resp, err = c.twitterAPI.Post("https://api.twitter.com/2/tweets", "application/json", &buf)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if isTweetTooLong(resp, body) {
			// A friendlier error for a common error type.
			return "", ErrTweetTooLong
		}
		return "", fmt.Errorf("POST /2/tweets: non-201 Created status code: %v body: %q", resp.Status, body)
	}
	var tweetResp struct {
		Data struct {
			ID string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&tweetResp); err != nil {
		return "", err
	}
	// Use a generic "username" in the URL since finding it needs another API call.
	// As long as the URL has this format, it will redirect to the canonical username.
	return "https://twitter.com/username/status/" + tweetResp.Data.ID, nil
}

// ErrTweetTooLong is the error when a tweet is too long.
var ErrTweetTooLong = fmt.Errorf("tweet text length exceeded Twitter's limit")

// isTweetTooLong reports whether the Twitter API response is
// known to represent a "Tweet needs to be a bit shorter." error.
// See https://developer.twitter.com/en/support/twitter-api/error-troubleshooting.
func isTweetTooLong(resp *http.Response, body []byte) bool {
	if resp.StatusCode != http.StatusForbidden {
		return false
	}
	var r struct{ Errors []struct{ Code int } }
	if err := json.Unmarshal(body, &r); err != nil {
		return false
	}
	return len(r.Errors) == 1 && r.Errors[0].Code == 186
}

// NewTwitterClient creates a Twitter API client authenticated
// to make Twitter API calls using the provided credentials.
func NewTwitterClient(t secret.TwitterCredentials) realTwitterClient {
	config := oauth1.NewConfig(t.ConsumerKey, t.ConsumerSecret)
	token := oauth1.NewToken(t.AccessTokenKey, t.AccessTokenSecret)
	return realTwitterClient{twitterAPI: config.Client(context.Background(), token)}
}

// NewMastodonClient creates a Mastodon API client authenticated
// to make Mastodon API calls using the provided credentials.
// The resulting client may have been permission-limited at its creation
// (e.g., only allowed to upload media and write posts).
// For tests, use NewTestMastodonClient, which creates private messages
// instead.
func NewMastodonClient(config secret.MastodonCredentials) (realMastodonClient, error) {
	tok := madon.UserToken{AccessToken: config.AccessToken}
	client, err := madon.RestoreApp(config.Application, config.Instance, config.ClientKey, config.ClientSecret, &tok)
	if err != nil {
		return realMastodonClient{}, err
	}
	return realMastodonClient{client, ""}, nil
}

// NewTestMastodonClient creates a client that will DM the announcement to the
// designated recipient for end-to-end testing. config.TestRecipient cannot be empty;
// that would result in a public message, which should not happen unintentionally.
func NewTestMastodonClient(config secret.MastodonCredentials, pmTarget string) (realMastodonClient, error) {
	if pmTarget == "" {
		panic("private message target to NewTestMastodonClient cannot be empty")
	}
	mc, err := NewMastodonClient(config)
	mc.testRecipient = pmTarget
	return mc, err
}

func NewBlueskyClient(config secret.BlueskyCredentials) (realBlueskyClient, error) {
	client, err := ltbsky.NewClient(config.Server, config.Handle, config.AccessToken)
	return realBlueskyClient{client: client}, err
}

type BlogPost struct {
	Author string
	Title  string
	URL    string
}

func GetBlogPostMetadata(ctx *workflow.TaskContext, atomURL string, blogPostURL string) (blogPost BlogPost, err error) {
	resp, err := ctxhttp.Get(ctx, http.DefaultClient, atomURL)
	if err != nil {
		return BlogPost{}, fmt.Errorf("unable to query atom feed for blog post entries: %s", err)
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return BlogPost{}, fmt.Errorf("unexpected status for GET %q: %d", blogPostURL, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return BlogPost{}, fmt.Errorf("unable to read the body from HTTP get request: %s", err)
	}
	var feed struct {
		Entry []struct {
			Title string `xml:"title"`
			Link  struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
			Author struct {
				Name string `xml:"name"`
			} `xml:"author"`
		} `xml:"entry"`
	}
	if err = xml.Unmarshal(b, &feed); err != nil {
		return BlogPost{}, fmt.Errorf("unable to unmarshal XML: %s", err)
	}
	for _, entry := range feed.Entry {
		if blogPostURL == entry.Link.Href {
			return BlogPost{entry.Author.Name, entry.Title, blogPostURL}, nil
		}
	}
	ctx.DisableRetries()
	return BlogPost{}, fmt.Errorf("blog post %q not found", blogPostURL)
}
