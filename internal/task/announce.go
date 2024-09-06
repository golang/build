// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"text/template"
	"time"

	sendgrid "github.com/sendgrid/sendgrid-go"
	sendgridmail "github.com/sendgrid/sendgrid-go/helpers/mail"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	goldmarktext "github.com/yuin/goldmark/text"
	"golang.org/x/build/internal/gophers"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/build/maintner/maintnerd/maintapi/version"
	"golang.org/x/net/html"
)

type releaseAnnouncement struct {
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

	// Security is a list of descriptions, one for each distinct
	// security fix included in this release, in Markdown format.
	//
	// The empty list means there are no security fixes included.
	//
	// This field applies only to minor releases; it is an error
	// to try to use it another release type.
	Security []string

	// Names is an optional list of release coordinator names to
	// include in the sign-off message.
	Names []string
}

type releasePreAnnouncement struct {
	// Target is the planned date for the release.
	Target Date

	// Version is the Go version that will be released.
	//
	// The version string must use the same format as Go tags. For example, "go1.17.2".
	Version string
	// SecondaryVersion is an older Go version that will also be released.
	// This only applies when two releases are planned. For example, "go1.16.10".
	SecondaryVersion string

	// Security is the security content to be included in the
	// release pre-announcement. It should not reveal details
	// beyond what's allowed by the security policy.
	Security string

	// CVEs is the list of CVEs for PRIVATE track fixes to
	// be included in the release pre-announcement.
	CVEs []string

	// Names is an optional list of release coordinator names to
	// include in the sign-off message.
	Names []string
}

// A Date represents a single calendar day (year, month, day).
//
// This type does not include location information, and
// therefore does not describe a unique 24-hour timespan.
//
// TODO(go.dev/issue/19700): Start using time.Day or so when available.
type Date struct {
	Year  int        // Year (for example, 2009).
	Month time.Month // Month of the year (January = 1, ...).
	Day   int        // Day of the month, starting at 1.
}

func (d Date) String() string { return d.Format("2006-01-02") }
func (d Date) Format(layout string) string {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC).Format(layout)
}
func (d Date) After(year int, month time.Month, day int) bool {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC).
		After(time.Date(year, month, day, 0, 0, 0, 0, time.UTC))
}

// AnnounceMailTasks contains tasks related to the release (pre-)announcement email.
type AnnounceMailTasks struct {
	// SendMail sends an email with the given header and content
	// using an externally-provided implementation.
	//
	// Email delivery happens asynchronously, so SendMail returns a nil error
	// if the transmission was started successfully, but that error value
	// doesn't indicate anything about the status of the delivery.
	SendMail func(MailHeader, MailContent) error

	// AnnounceMailHeader is the header to use for the release (pre-)announcement email.
	AnnounceMailHeader MailHeader

	// testHookNow is optionally set by tests to override time.Now.
	testHookNow func() time.Time
}

// SentMail represents an email that was sent.
type SentMail struct {
	Subject string // Subject of the email. Expected to be unique so it can be used to identify the email.
}

// AnnounceRelease sends an email to Google Groups
// announcing that a Go release has been published.
func (t AnnounceMailTasks) AnnounceRelease(ctx *workflow.TaskContext, kind ReleaseKind, published []Published, security []string, users []string) (SentMail, error) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < time.Minute {
		return SentMail{}, fmt.Errorf("insufficient time for announce release task; a minimum of a minute left on context is required")
	}
	if len(published) < 1 || len(published) > 2 {
		return SentMail{}, fmt.Errorf("got %d published Go releases, AnnounceRelease supports only 1 or 2 at once", len(published))
	}
	names, err := coordinatorFirstNames(users)
	if err != nil {
		return SentMail{}, err
	}

	r := releaseAnnouncement{
		Kind:     kind,
		Version:  published[0].Version,
		Security: security,
		Names:    names,
	}
	if len(published) == 2 {
		r.SecondaryVersion = published[1].Version
	}

	// Generate the announcement email.
	m, err := announcementMail(r)
	if err != nil {
		return SentMail{}, err
	}
	ctx.Printf("announcement subject: %s\n\n", m.Subject)
	ctx.Printf("announcement body HTML:\n%s\n", m.BodyHTML)
	ctx.Printf("announcement body text:\n%s", m.BodyText)

	// Before sending, check to see if this announcement already exists.
	if threadURL, err := findGoogleGroupsThread(ctx, m.Subject); err != nil {
		// Proceeding would risk sending a duplicate email, so error out instead.
		return SentMail{}, fmt.Errorf("stopping early due to error checking for an existing Google Groups thread: %w", err)
	} else if threadURL != "" {
		// This should never happen since this task runs once per release.
		// It can happen under unusual circumstances, for example if the task crashes after
		// mailing but before completion, or if parts of the release workflow are restarted,
		// or if a human mails the announcement email manually out of band.
		//
		// So if we see that the email exists, consider it as "task completed successfully"
		// and pretend we were the ones that sent it, so the high level workflow can keep going.
		ctx.Printf("a Google Groups thread with matching subject %q already exists at %q, so we'll consider that as it being sent successfully", m.Subject, threadURL)
		return SentMail{m.Subject}, nil
	}

	// Send the announcement email to the destination mailing lists.
	if t.SendMail == nil {
		return SentMail{Subject: "[dry-run] " + m.Subject}, nil
	}
	ctx.DisableRetries()
	err = t.SendMail(t.AnnounceMailHeader, m)
	if err != nil {
		return SentMail{}, err
	}

	return SentMail{m.Subject}, nil
}

// PreAnnounceRelease sends an email pre-announcing a Go release
// containing PRIVATE track security fixes planned for the target date.
func (t AnnounceMailTasks) PreAnnounceRelease(ctx *workflow.TaskContext, versions []string, target Date, security string, cves []string, users []string) (SentMail, error) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < time.Minute {
		return SentMail{}, fmt.Errorf("insufficient time for pre-announce release task; a minimum of a minute left on context is required")
	}
	if len(versions) < 1 || len(versions) > 2 {
		return SentMail{}, fmt.Errorf("got %d planned Go releases, PreAnnounceRelease supports only 1 or 2 at once", len(versions))
	}
	now := time.Now().UTC()
	if t.testHookNow != nil {
		now = t.testHookNow()
	}
	if !target.After(now.Year(), now.Month(), now.Day()) { // A very simple check. Improve as needed.
		return SentMail{}, fmt.Errorf("target release date is not in the future")
	}
	if security == "" {
		return SentMail{}, fmt.Errorf("security content is not specified")
	}
	if len(cves) == 0 {
		return SentMail{}, errors.New("CVEs are not specified")
	}
	names, err := coordinatorFirstNames(users)
	if err != nil {
		return SentMail{}, err
	}

	r := releasePreAnnouncement{
		Target:   target,
		Version:  versions[0],
		Security: security,
		CVEs:     cves,
		Names:    names,
	}
	if len(versions) == 2 {
		r.SecondaryVersion = versions[1]
	}

	// Generate the pre-announcement email.
	m, err := announcementMail(r)
	if err != nil {
		return SentMail{}, err
	}
	ctx.Printf("pre-announcement subject: %s\n\n", m.Subject)
	ctx.Printf("pre-announcement body HTML:\n%s\n", m.BodyHTML)
	ctx.Printf("pre-announcement body text:\n%s", m.BodyText)

	// Before sending, check to see if this pre-announcement already exists.
	if threadURL, err := findGoogleGroupsThread(ctx, m.Subject); err != nil {
		return SentMail{}, fmt.Errorf("stopping early due to error checking for an existing Google Groups thread: %w", err)
	} else if threadURL != "" {
		ctx.Printf("a Google Groups thread with matching subject %q already exists at %q, so we'll consider that as it being sent successfully", m.Subject, threadURL)
		return SentMail{m.Subject}, nil
	}

	// Send the pre-announcement email to the destination mailing lists.
	if t.SendMail == nil {
		return SentMail{Subject: "[dry-run] " + m.Subject}, nil
	}
	ctx.DisableRetries()
	err = t.SendMail(t.AnnounceMailHeader, m)
	if err != nil {
		return SentMail{}, err
	}

	return SentMail{m.Subject}, nil
}

func coordinatorFirstNames(users []string) ([]string, error) {
	return mapCoordinators(users, func(p *gophers.Person) string {
		name, _, _ := strings.Cut(p.Name, " ")
		return name
	})
}

func coordinatorEmails(users []string) ([]string, error) {
	return mapCoordinators(users, func(p *gophers.Person) string {
		return p.Gerrit
	})
}

func mapCoordinators(users []string, f func(*gophers.Person) string) ([]string, error) {
	var outs []string
	for _, user := range users {
		person, err := lookupCoordinator(user)
		if err != nil {
			return nil, err
		}
		outs = append(outs, f(person))
	}
	return outs, nil
}

// CheckCoordinators checks that all users are known
// and have required information (name, Gerrit email).
func CheckCoordinators(users []string) error {
	var report strings.Builder
	for _, user := range users {
		if _, err := lookupCoordinator(user); err != nil {
			fmt.Fprintln(&report, err)
		}
	}
	if report.Len() != 0 {
		return errors.New(report.String())
	}
	return nil
}

func lookupCoordinator(user string) (*gophers.Person, error) {
	person := gophers.GetPerson(user + "@golang.org")
	if person == nil {
		person = gophers.GetPerson(user + "@google.com")
	}
	if person == nil {
		return nil, fmt.Errorf("unknown username %q: no @golang or @google account", user)
	} else if person.Name == "" {
		return nil, fmt.Errorf("release coordinator %q is missing a name", person.Gerrit)
	}
	return person, nil
}

// A MailHeader is an email header.
type MailHeader struct {
	From mail.Address // An RFC 5322 address. For example, "Barry Gibbs <bg@example.com>".
	To   mail.Address
	BCC  []mail.Address
}

// A MailContent holds the content of an email.
type MailContent struct {
	Subject  string
	BodyHTML string
	BodyText string
}

// announcementMail generates the (pre-)announcement email using data,
// which must be one of these types:
//   - releaseAnnouncement for a release announcement
//   - releasePreAnnouncement for a release pre-announcement
//   - goplsPrereleaseAnnouncement for a gopls pre-announcement
func announcementMail(data any) (MailContent, error) {
	// Select the appropriate template name.
	var name string
	switch r := data.(type) {
	case releaseAnnouncement:
		switch r.Kind {
		case KindBeta:
			name = "announce-beta.md"
		case KindRC:
			name = "announce-rc.md"
		case KindMajor:
			name = "announce-major.md"
		case KindMinor:
			name = "announce-minor.md"
		default:
			return MailContent{}, fmt.Errorf("unknown release kind: %v", r.Kind)
		}
		if len(r.Security) > 0 && name != "announce-minor.md" {
			// The Security field isn't supported in templates other than minor,
			// so report an error instead of silently dropping it.
			//
			// Note: Maybe in the future we'd want to consider support for including sentences like
			// "This beta release includes the same security fixes as in Go X.Y.Z and Go A.B.C.",
			// but we'll have a better idea after these initial templates get more practical use.
			return MailContent{}, fmt.Errorf("email template %q doesn't support the Security field; this field can only be used in minor releases", name)
		} else if r.SecondaryVersion != "" && name != "announce-minor.md" {
			return MailContent{}, fmt.Errorf("email template %q doesn't support more than one release; the SecondaryVersion field can only be used in minor releases", name)
		}
	case releasePreAnnouncement:
		name = "pre-announce-minor.md"
	case goplsReleaseAnnouncement:
		name = "gopls-announce.md"
	case goplsPrereleaseAnnouncement:
		name = "gopls-pre-announce.md"
	default:
		return MailContent{}, fmt.Errorf("unknown template data type %T", data)
	}

	// Render the (pre-)announcement email template.
	//
	// It'll produce a valid message with a MIME header and a body, so parse it as such.
	var buf bytes.Buffer
	if err := announceTmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return MailContent{}, err
	}
	m, err := mail.ReadMessage(&buf)
	if err != nil {
		return MailContent{}, fmt.Errorf(`email template must be formatted like a mail message, but reading it failed: %v`, err)
	}

	// Get the email subject (it's a plain string, no further processing needed).
	if _, ok := m.Header["Subject"]; !ok {
		return MailContent{}, fmt.Errorf(`email template must have a "Subject" key in its MIME header, but it's not found`)
	} else if n := len(m.Header["Subject"]); n != 1 {
		return MailContent{}, fmt.Errorf(`email template must have a single "Subject" value in its MIME header, but have %d values`, n)
	}
	subject := m.Header.Get("Subject")

	// Render the email body, in Markdown format at this point, to HTML and plain text.
	html, text, err := renderMarkdown(m.Body)
	if err != nil {
		return MailContent{}, err
	}

	return MailContent{subject, html, text}, nil
}

// announceTmpl holds templates for Go release announcement emails.
//
// Each email template starts with a MIME-style header with a Subject key,
// and the rest of it is Markdown for the email body.
var announceTmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"join": func(s []string) string {
		switch len(s) {
		case 0:
			return ""
		case 1:
			return s[0]
		case 2:
			return s[0] + " and " + s[1]
		default:
			return strings.Join(s[:len(s)-1], ", ") + ", and " + s[len(s)-1]
		}
	},
	"indent": func(s string) string { return "\t" + strings.ReplaceAll(s, "\n", "\n\t") },

	// subjectPrefix returns the email subject prefix for release r, if any.
	"subjectPrefix": func(r releaseAnnouncement) string {
		switch {
		case len(r.Security) > 0:
			// Include a security prefix as documented at https://go.dev/security#receiving-security-updates:
			//
			//	> The best way to receive security announcements is to subscribe to the golang-announce mailing list.
			//	> Any messages pertaining to a security issue will be prefixed with [security].
			//
			return "[security]"
		default:
			return ""
		}
	},

	// shortcommit is used to shorten git commit hashes.
	"shortcommit": func(v string) string {
		if len(v) > 7 {
			return v[:7]
		}
		return v
	},

	// short and helpers below manipulate valid Go version strings
	// for the current needs of the announcement templates.
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
	// atLeast reports whether v1 >= v2.
	"atLeast": func(v1, v2 string) bool {
		return CompareGoVersions(v1, v2) >= 0
	},
	// build extracts the pre-release build number of a valid Go version.
	// For example, build("go1.19beta2") == "2".
	"build": func(v string) (string, error) {
		if i := strings.Index(v, "beta"); i != -1 {
			return v[i+len("beta"):], nil
		} else if i := strings.Index(v, "rc"); i != -1 {
			return v[i+len("rc"):], nil
		}
		return "", fmt.Errorf("internal error: unhandled pre-release Go version %q", v)
	},
}).ParseFS(tmplDir, "template/announce-*.md", "template/pre-announce-minor.md", "template/gopls-announce.md", "template/gopls-pre-announce.md"))

//go:embed template
var tmplDir embed.FS

type realSendGridMailClient struct {
	sg *sendgrid.Client
}

// NewSendGridMailClient creates a SendGrid mail client
// authenticated with the given API key.
func NewSendGridMailClient(sendgridAPIKey string) realSendGridMailClient {
	return realSendGridMailClient{sg: sendgrid.NewSendClient(sendgridAPIKey)}
}

// SendMail sends an email by making an authenticated request to the SendGrid API.
func (c realSendGridMailClient) SendMail(h MailHeader, m MailContent) error {
	from, to := sendgridmail.Email(h.From), sendgridmail.Email(h.To)
	req := sendgridmail.NewSingleEmail(&from, m.Subject, &to, m.BodyText, m.BodyHTML)
	if len(req.Personalizations) != 1 {
		return fmt.Errorf("internal error: len(req.Personalizations) is %d, want 1", len(req.Personalizations))
	}
	for _, bcc := range h.BCC {
		bcc := sendgridmail.Email(bcc)
		req.Personalizations[0].AddBCCs(&bcc)
	}
	no := false
	req.TrackingSettings = &sendgridmail.TrackingSettings{
		ClickTracking:        &sendgridmail.ClickTrackingSetting{Enable: &no},
		OpenTracking:         &sendgridmail.OpenTrackingSetting{Enable: &no},
		SubscriptionTracking: &sendgridmail.SubscriptionTrackingSetting{Enable: &no},
	}
	resp, err := c.sg.Send(req)
	if err != nil {
		return err
	} else if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status %d %s, want 202 Accepted; body = %s", resp.StatusCode, http.StatusText(resp.StatusCode), resp.Body)
	}
	return nil
}

// AwaitAnnounceMail waits for an announcement email with the specified subject
// to show up on Google Groups, and returns its canonical URL.
func (t AnnounceMailTasks) AwaitAnnounceMail(ctx *workflow.TaskContext, m SentMail) (announcementURL string, _ error) {
	// Find the URL for the announcement while giving the email a chance to be received and moderated.
	check := func() (string, bool, error) {
		// See if our email is available by now.
		threadURL, err := findGoogleGroupsThread(ctx, m.Subject)
		if err != nil {
			ctx.Printf("findGoogleGroupsThread: %v", err)
			return "", false, nil
		}
		return threadURL, threadURL != "", nil

	}
	return AwaitCondition(ctx, 10*time.Second, check)
}

// findGoogleGroupsThread fetches the first page of threads from the golang-announce
// Google Groups mailing list, and looks for a thread with the matching subject line.
// It returns its URL if found or the empty string if not found.
//
// findGoogleGroupsThread returns an error that matches fetchError with
// PossiblyRetryable set to true when it has signal that repeating the
// same call after some time may succeed.
func findGoogleGroupsThread(ctx *workflow.TaskContext, subject string) (threadURL string, _ error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://groups.google.com/g/golang-announce", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fetchError{Err: err, PossiblyRetryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		possiblyRetryable := resp.StatusCode/100 == 5 // Consider a 5xx server response to possibly succeed later.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", fetchError{fmt.Errorf("did not get acceptable status code: %v body: %q", resp.Status, body), possiblyRetryable}
	}
	if ct, want := resp.Header.Get("Content-Type"), "text/html; charset=utf-8"; ct != want {
		ctx.Printf("findGoogleGroupsThread: got response with non-'text/html; charset=utf-8' Content-Type header %q\n", ct)
		if mediaType, _, err := mime.ParseMediaType(ct); err != nil {
			return "", fmt.Errorf("bad Content-Type header %q: %v", ct, err)
		} else if mediaType != "text/html" {
			return "", fmt.Errorf("got media type %q, want %q", mediaType, "text/html")
		}
	}
	doc, err := html.Parse(retryableReader{io.LimitReader(resp.Body, 5<<20)})
	if err != nil {
		return "", err
	}
	var baseHref string
	var linkHref string
	var found bool
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "base" {
			baseHref = href(n)
		} else if n.Type == html.ElementNode && n.Data == "a" {
			linkHref = href(n)
		} else if n.Type == html.TextNode && n.Data == subject {
			// Found our link. Break out.
			found = true
			return
		}
		for c := n.FirstChild; c != nil && !found; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)
	if !found {
		// Thread not found on the first page.
		return "", nil
	}
	base, err := url.Parse(baseHref)
	if err != nil {
		return "", err
	}
	link, err := url.Parse(linkHref)
	if err != nil {
		return "", err
	}
	threadURL = base.ResolveReference(link).String()
	const announcementPrefix = "https://groups.google.com/g/golang-announce/c/"
	if !strings.HasPrefix(threadURL, announcementPrefix) {
		return "", fmt.Errorf("found URL %q, but it doesn't have the expected prefix %q", threadURL, announcementPrefix)
	}
	return threadURL, nil
}

func href(n *html.Node) string {
	for _, a := range n.Attr {
		if a.Key == "href" {
			return a.Val
		}
	}
	return ""
}

// retryableReader annotates errors from
// RetryableReader as possibly retryable.
type retryableReader struct{ RetryableReader io.Reader }

func (r retryableReader) Read(p []byte) (n int, err error) {
	n, err = r.RetryableReader.Read(p)
	if err != nil && err != io.EOF {
		err = fetchError{Err: err, PossiblyRetryable: true}
	}
	return n, err
}

// fetchError records an error during a fetch operation over an unreliable network.
type fetchError struct {
	Err error // Non-nil.

	// PossiblyRetryable indicates whether Err is believed to be possibly caused by a
	// non-terminal network error, such that the caller can expect it may not happen
	// again if it simply tries the same fetch operation again after waiting a bit.
	PossiblyRetryable bool
}

func (e fetchError) Error() string { return e.Err.Error() }
func (e fetchError) Unwrap() error { return e.Err }

// renderMarkdown parses Markdown source
// and renders it to HTML and plain text.
//
// The Markdown specification and its various extensions are vast.
// Here we support a small, simple set of Markdown features
// that satisfies the needs of the announcement mail tasks.
func renderMarkdown(r io.Reader) (html, text string, _ error) {
	source, err := io.ReadAll(r)
	if err != nil {
		return "", "", err
	}
	// Configure a Markdown parser and HTML renderer fairly closely
	// to how it's done in x/website, just without raw HTML support
	// and other extensions we don't need for announcement emails.
	md := goldmark.New(
		goldmark.WithRendererOptions(goldmarkhtml.WithHardWraps()),
		goldmark.WithExtensions(
			extension.NewLinkify(extension.WithLinkifyAllowedProtocols([][]byte{[]byte("https:")})),
		),
	)
	doc := md.Parser().Parse(goldmarktext.NewReader(source))
	var htmlBuf, textBuf bytes.Buffer
	err = md.Renderer().Render(&htmlBuf, source, doc)
	if err != nil {
		return "", "", err
	}
	err = (markdownToTextRenderer{}).Render(&textBuf, source, doc)
	if err != nil {
		return "", "", err
	}
	return htmlBuf.String(), textBuf.String(), nil
}

// markdownToTextRenderer is a simple goldmark/renderer.Renderer implementation
// that renders Markdown to plain text for the needs of announcement mail tasks.
//
// It produces an output suitable for email clients that cannot (or choose not to)
// display the HTML version of the email. (It also helps a bit with the readability
// of our test data, since without a browser plain text is more readable than HTML.)
//
// The output is mostly plain text that doesn't preserve Markdown syntax (for example,
// `code` is rendered without backticks, code blocks aren't indented, and so on),
// though there is very lightweight formatting applied (links are written as "text <URL>").
//
// We can in theory choose to delete this renderer at any time if its maintenance costs
// start to outweight its benefits, since Markdown by definition is designed to be human
// readable and can be used as plain text without any processing.
type markdownToTextRenderer struct{}

func (markdownToTextRenderer) Render(w io.Writer, source []byte, n ast.Node) error {
	const debug = false
	if debug {
		n.Dump(source, 0)
	}

	var (
		markers []byte // Stack of list markers, from outermost to innermost.
	)
	walk := func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if n.Type() == ast.TypeBlock && n.PreviousSibling() != nil {
				// Print a blank line between block nodes.
				switch n.PreviousSibling().Kind() {
				default:
					fmt.Fprint(w, "\n\n")
				case ast.KindCodeBlock, ast.KindFencedCodeBlock:
					// A code block always ends with a newline, so only need one more.
					fmt.Fprintln(w)
				}

				// If we're in a list, indent accordingly.
				if n.Kind() != ast.KindListItem {
					fmt.Fprint(w, strings.Repeat("\t", len(markers)))
				}
			}

			switch n := n.(type) {
			case *ast.Text:
				fmt.Fprintf(w, "%s", n.Text(source))

				// Print a line break.
				if n.SoftLineBreak() || n.HardLineBreak() {
					fmt.Fprintln(w)

					// If we're in a list, indent accordingly.
					fmt.Fprint(w, strings.Repeat("\t", len(markers)))
				}
			case *ast.CodeBlock, *ast.FencedCodeBlock:
				// Code blocks are printed as is in plain text.
				for i := 0; i < n.Lines().Len(); i++ {
					s := n.Lines().At(i)
					if i != 0 {
						// If we're in a list, indent inner lines accordingly.
						fmt.Fprint(w, strings.Repeat("\t", len(markers)))
					}
					fmt.Fprint(w, string(source[s.Start:s.Stop]))
				}
			case *ast.AutoLink:
				// Auto-links are printed as is in plain text.
				//
				// For example, the Markdown "https://go.dev/issue/123"
				// is rendered as plain text "https://go.dev/issue/123".
				fmt.Fprint(w, string(n.Label(source)))
			case *ast.List:
				// Push list marker on the stack.
				markers = append(markers, n.Marker)
			case *ast.ListItem:
				fmt.Fprintf(w, "%s%c\t", strings.Repeat("\t", len(markers)-1), markers[len(markers)-1])
			}
		} else {
			switch n := n.(type) {
			case *ast.Link:
				// Append the link's URL after its text.
				//
				// For example, the Markdown "[security policy](https://go.dev/security)"
				// is rendered as plain text "security policy <https://go.dev/security>".
				fmt.Fprintf(w, " <%s>", n.Destination)
			case *ast.List:
				// Pop list marker off the stack.
				markers = markers[:len(markers)-1]
			}

			if n.Type() == ast.TypeDocument && n.ChildCount() != 0 {
				// Print a newline at the end of the document, if it's not empty.
				fmt.Fprintln(w)
			}
		}
		return ast.WalkContinue, nil
	}
	return ast.Walk(n, walk)
}
func (markdownToTextRenderer) AddOptions(...renderer.Option) {}
