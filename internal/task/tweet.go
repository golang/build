// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/dghubble/oauth1"
	"github.com/esimov/stackblur-go"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// ReleaseTweet describes a tweet that announces a Go release.
type ReleaseTweet struct {
	// Version is the Go version that has been released.
	//
	// The version string must use the same format as Go tags. For example:
	// 	• "go1.17.2" for a minor Go release
	// 	• "go1.18" for a major Go release
	// 	• "go1.18beta1" or "go1.18rc1" for a pre-release
	Version string
	// SecondaryVersion is an older Go version that was also released.
	// This only applies to minor releases when two releases are made.
	// For example, "go1.16.10".
	SecondaryVersion string

	// Security is an optional sentence describing security fixes
	// included in this release.
	//
	// The empty string means there are no security fixes to highlight.
	// Past examples:
	// 	• "Includes a security fix for the Wasm port (CVE-2021-38297)."
	// 	• "Includes a security fix for archive/zip (CVE-2021-39293)."
	// 	• "Includes a security fix for crypto/tls (CVE-2021-34558)."
	// 	• "Includes security fixes for archive/zip, net, net/http/httputil, and math/big packages."
	Security string

	// Announcement is the announcement URL.
	//
	// It's applicable to all release types other than major,
	// since major releases point to release notes instead.
	// For example, "https://groups.google.com/g/golang-announce/c/wB1fph5RpsE/m/ZGwOsStwAwAJ".
	Announcement string

	// RandomSeed is the pseudo-random number generator seed to use for presentational
	// choices, such as selecting one out of many available emoji or release archives.
	// The zero value means to use time.Now().UnixNano().
	RandomSeed int64
}

func (r ReleaseTweet) seed() int64 {
	if r.RandomSeed == 0 {
		return time.Now().UnixNano()
	}
	return r.RandomSeed
}

// TweetTasks contains tasks related to the release tweet.
type TweetTasks struct {
	// TwitterClient can be used to post a tweet.
	TwitterClient interface {
		// PostTweet posts a tweet with the given text and PNG image,
		// both of which must be non-empty, and returns the tweet URL.
		//
		// ErrTweetTooLong error is returned if posting fails
		// due to the tweet text length exceeding Twitter's limit.
		PostTweet(text string, imagePNG []byte) (tweetURL string, _ error)
	}
}

// TweetRelease posts a tweet announcing a Go release.
// ErrTweetTooLong is returned if the inputs result in a tweet that's too long.
func (t TweetTasks) TweetRelease(ctx *workflow.TaskContext, r ReleaseTweet) (tweetURL string, _ error) {
	if err := verifyGoVersions(r.Version); err != nil {
		return "", err
	} else if err := verifyGoVersions(r.SecondaryVersion); r.SecondaryVersion != "" && err != nil {
		return "", err
	}

	rnd := rand.New(rand.NewSource(r.seed()))

	// Generate tweet text.
	tweetText, err := tweetText(r, rnd)
	if err != nil {
		return "", err
	}
	ctx.Printf("tweet text:\n%s\n", tweetText)

	// Generate tweet image.
	imagePNG, imageText, err := tweetImage(r.Version, rnd)
	if err != nil {
		return "", err
	}
	ctx.Printf("tweet image:\n%s\n", imageText)

	// Post a tweet via the Twitter API.
	if t.TwitterClient == nil {
		return "(dry-run)", nil
	}
	ctx.DisableRetries()
	tweetURL, err = t.TwitterClient.PostTweet(tweetText, imagePNG)
	return tweetURL, err
}

// tweetText generates the text to use in the announcement
// tweet for release r.
func tweetText(r ReleaseTweet, rnd *rand.Rand) (string, error) {
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
	}).Parse(tweetTextTmpl)
	if err != nil {
		return "", err
	}

	// Pick a template name and populate template data
	// for this type of release.
	var (
		name string
		data interface{}
	)
	if i := strings.Index(r.Version, "beta"); i != -1 { // A beta release.
		name, data = "beta", struct {
			Maj, Beta string
			ReleaseTweet
		}{
			Maj:          r.Version[len("go"):i],
			Beta:         r.Version[i+len("beta"):],
			ReleaseTweet: r,
		}
	} else if i := strings.Index(r.Version, "rc"); i != -1 { // Release Candidate.
		name, data = "rc", struct {
			Maj, RC string
			ReleaseTweet
		}{
			Maj:          r.Version[len("go"):i],
			RC:           r.Version[i+len("rc"):],
			ReleaseTweet: r,
		}
	} else if strings.Count(r.Version, ".") == 1 { // Major release like "go1.X".
		name, data = "major", struct {
			Maj string
			ReleaseTweet
		}{
			Maj:          r.Version[len("go"):],
			ReleaseTweet: r,
		}
	} else if strings.Count(r.Version, ".") == 2 { // Minor release like "go1.X.Y".
		name, data = "minor", struct {
			Curr, Prev string
			ReleaseTweet
		}{
			Curr:         r.Version[len("go"):],
			Prev:         strings.TrimPrefix(r.SecondaryVersion, "go"),
			ReleaseTweet: r,
		}
	} else {
		return "", fmt.Errorf("unknown version format: %q", r.Version)
	}

	if r.SecondaryVersion != "" && name != "minor" {
		return "", fmt.Errorf("tweet template %q doesn't support more than one release; the SecondaryVersion field can only be used in minor releases", name)
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

const tweetTextTmpl = `{{define "minor" -}}
{{emoji "release"}} Go {{.Curr}} {{with .Prev}}and {{.}} are{{else}}is{{end}} released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "announce"}} Announcement: {{.Announcement}}

{{emoji "download"}} Download: https://go.dev/dl/#{{.Version}}

#golang{{end}}


{{define "beta" -}}
{{emoji "beta-release"}} Go {{.Maj}} Beta {{.Beta}} is released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "try"}} Try it! File bugs! https://go.dev/issue/new

{{emoji "announce"}} Announcement: {{.Announcement}}

{{emoji "download"}} Download: https://go.dev/dl/#{{.Version}}

#golang{{end}}


{{define "rc" -}}
{{emoji "rc-release"}} Go {{.Maj}} Release Candidate {{.RC}} is released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "run"}} Run it in dev! Run it in prod! File bugs! https://go.dev/issue/new

{{emoji "announce"}} Announcement: {{.Announcement}}

{{emoji "download"}} Download: https://go.dev/dl/#{{.Version}}

#golang{{end}}


{{define "major" -}}
{{emoji "release"}} Go {{.Maj}} is released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "notes"}} Release notes: https://go.dev/doc/{{.Version}}

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
// tweet for goVersion. It returns the image encoded as PNG,
// and the text displayed in the image.
//
// tweetImage makes an HTTP GET request to the go.dev/dl/?mode=json
// read-only API to select a random release archive to highlight.
func tweetImage(goVersion string, rnd *rand.Rand) (imagePNG []byte, imageText string, _ error) {
	a, err := fetchRandomArchive(goVersion, rnd)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	if err := goCmdTmpl.Execute(&buf, map[string]string{
		"GoVer":    goVersion,
		"GOOS":     a.OS,
		"GOARCH":   a.GOARCH(),
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

// fetchRandomArchive downloads all release archives for Go version goVer,
// and selects a random archive to showcase in the image that displays
// sample output from the 'go install golang.org/dl/...@latest' command.
func fetchRandomArchive(goVer string, rnd *rand.Rand) (archive golangorgDLFile, _ error) {
	archives, err := fetchReleaseArchives(goVer)
	if err != nil {
		return golangorgDLFile{}, err
	}
	return archives[rnd.Intn(len(archives))], nil
}

func fetchReleaseArchives(goVer string) (archives []golangorgDLFile, _ error) {
	url := "https://go.dev/dl/?mode=json"
	if strings.Contains(goVer, "beta") || strings.Contains(goVer, "rc") ||
		goVer == "go1.17" || goVer == "go1.17.1" || goVer == "go1.11.1" /* For TestTweetRelease. */ {

		url += "&include=all"
	}
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("non-200 OK status code: %v", resp.Status)
	} else if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		return nil, fmt.Errorf("got Content-Type %q, want %q", ct, "application/json")
	}
	var releases []golangorgDLRelease
	err = json.NewDecoder(resp.Body).Decode(&releases)
	if err != nil {
		return nil, err
	}
	for _, r := range releases {
		if r.Version != goVer {
			continue
		}
		var archives []golangorgDLFile
		for _, f := range r.Files {
			if f.Kind != "archive" {
				continue
			}
			archives = append(archives, f)
		}
		if len(archives) == 0 {
			return nil, fmt.Errorf("release version %q has 0 archive files", goVer)
		}
		// Return archives.
		return archives, nil
	}
	return nil, fmt.Errorf("release version %q not found", goVer)
}

// golangorgDLRelease represents a release on the go.dev downloads page.
type golangorgDLRelease struct {
	Version string
	Files   []golangorgDLFile
}

// golangorgDLFile represents a file on the go.dev downloads page.
// It should be kept in sync with code in x/build/cmd/release and x/website/internal/dl.
type golangorgDLFile struct {
	Filename string
	OS       string
	Arch     string
	Size     int64
	Kind     string // One of "archive", "installer", "source".
}

func (f golangorgDLFile) GOARCH() string {
	if f.OS == "linux" && f.Arch == "armv6l" {
		return "arm"
	}
	return f.Arch
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

type realTwitterClient struct {
	twitterAPI *http.Client
}

// PostTweet implements the TweetTasks.TwitterClient interface.
func (c realTwitterClient) PostTweet(text string, imagePNG []byte) (tweetURL string, _ error) {
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
		return "", fmt.Errorf("POST media/upload: non-200 OK status code: %v body: %q", resp.Status, body)
	}
	var media struct {
		ID string `json:"media_id_string"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&media); err != nil {
		return "", err
	}

	// Make a Twitter API call to update status with uploaded image.
	// See https://developer.twitter.com/en/docs/twitter-api/v1/tweets/post-and-engage/api-reference/post-statuses-update.
	resp, err = c.twitterAPI.PostForm("https://api.twitter.com/1.1/statuses/update.json", url.Values{
		"status":    []string{text},
		"media_ids": []string{media.ID},
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if isTweetTooLong(resp, body) {
			// A friendlier error for a common error type.
			return "", ErrTweetTooLong
		}
		return "", fmt.Errorf("POST statuses/update: non-200 OK status code: %v body: %q", resp.Status, body)
	}
	var tweet struct {
		ID   string `json:"id_str"`
		User struct {
			ScreenName string `json:"screen_name"`
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&tweet); err != nil {
		return "", err
	}
	return "https://twitter.com/" + tweet.User.ScreenName + "/status/" + tweet.ID, nil
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
