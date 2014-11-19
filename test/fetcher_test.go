// +build sudo

package test

import (
	"fmt"
	"hash/fnv"
	"io"
	"io/ioutil"
	"net/http"
	"testing"
	"time"

	"github.com/iParadigms/walker"
	"github.com/iParadigms/walker/helpers"
	"github.com/stretchr/testify/mock"
)

const defaultSleep time.Duration = time.Millisecond * 40

const html_body string = `<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>Test norobots site</title>
</head>

<div id="menu">
	<a href="/dir1/">Dir1</a>
	<a href="/dir2/">Dir2</a>
	<a id="other" href="http://other.com/" title="stuff">Other</a>
</div>
</html>`

const html_body_nolinks string = `<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>No Links</title>
</head>
<div id="menu">
</div>
</html>`

const html_test_links string = `<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>Test links page</title>
</head>

<div id="menu">
	<a href="relative-dir/">link</a>
	<a href="relative-page/page.html">link</a>
	<a href="/abs-relative-dir/">link</a>
	<a href="/abs-relative-page/page.html">link</a>
	<a href="https://other.org/abs-dir/">link</a>
	<a href="https://other.org/abs-page/page.html">link</a>
	<a href="javascript:doStuff();">link</a>
	<a href="ftp:ignoreme.zip;">link</a>
	<a href="ftP:ignoreme.zip;">link</a>
	<a href="hTTP:donot/ignore.html">link</a>
</div>
</html>`

func init() {
	helpers.LoadTestConfig("test-walker.yaml")
}

type PerLink struct {
	url      string
	response *helpers.MockResponse
	hidden   bool
}

type PerHost struct {
	domain string
	links  []PerLink
}
type TestSpec struct {
	perHost []PerHost

	// Flags that control how runFetcher does it's job
	hasParsedLinks    bool
	hasNoLinks        bool
	suppressTransport bool
	transport         http.RoundTripper
}

type TestResults struct {
	server    *helpers.MockRemoteServer
	datastore *helpers.MockDatastore
	handler   *helpers.MockHandler
	manager   *walker.FetchManager
}

func (self *TestResults) handlerCalls() []*walker.FetchResults {
	var ret []*walker.FetchResults
	for _, call := range self.handler.Calls {
		fr := call.Arguments.Get(0).(*walker.FetchResults)
		ret = append(ret, fr)
	}
	return ret
}

func (self *TestResults) dsStoreParsedURLCalls() ([]*walker.URL, []*walker.FetchResults) {
	var r1 []*walker.URL
	var r2 []*walker.FetchResults
	for _, call := range self.datastore.Calls {
		if call.Method == "StoreParsedURL" {
			u := call.Arguments.Get(0).(*walker.URL)
			fr := call.Arguments.Get(1).(*walker.FetchResults)
			r1 = append(r1, u)
			r2 = append(r2, fr)
		}
	}
	return r1, r2
}

func (self *TestResults) dsStoreURLFetchResultsCalls() []*walker.FetchResults {
	var r1 []*walker.FetchResults
	for _, call := range self.datastore.Calls {
		if call.Method == "StoreURLFetchResults" {
			fr := call.Arguments.Get(0).(*walker.FetchResults)
			r1 = append(r1, fr)
		}
	}
	return r1
}

func (self *TestResults) assertExpectations(t *testing.T) {
	self.datastore.AssertExpectations(t)
	self.handler.AssertExpectations(t)
}

func runFetcher(test TestSpec, duration time.Duration, t *testing.T) TestResults {

	//
	// Build mocks
	//
	h := &helpers.MockHandler{}

	rs, err := helpers.NewMockRemoteServer()
	if err != nil {
		t.Fatal(err)
	}
	ds := &helpers.MockDatastore{}

	//
	// Configure mocks
	//
	if !test.hasNoLinks {
		ds.On("StoreURLFetchResults", mock.AnythingOfType("*walker.FetchResults")).Return()
	}
	if test.hasParsedLinks {
		ds.On("StoreParsedURL",
			mock.AnythingOfType("*walker.URL"),
			mock.AnythingOfType("*walker.FetchResults")).Return()

	}

	if !test.hasNoLinks {
		h.On("HandleResponse", mock.Anything).Return()
	}
	for _, host := range test.perHost {
		ds.On("ClaimNewHost").Return(host.domain).Once()
		var urls []*walker.URL
		for _, link := range host.links {
			if !link.hidden {
				urls = append(urls, helpers.Parse(link.url))
			}
			if link.response != nil {
				rs.SetResponse(link.url, link.response)
			}
		}
		if !test.hasNoLinks {
			ds.On("LinksForHost", host.domain).Return(urls)
		}
		ds.On("UnclaimHost", host.domain).Return()

	}
	// This last call will make ClaimNewHost return "" on each subsequent call,
	// which will put the fetcher to sleep.
	ds.On("ClaimNewHost").Return("")

	//
	// Run the manager
	//
	var manager *walker.FetchManager
	if test.suppressTransport {
		manager = &walker.FetchManager{
			Datastore: ds,
			Handler:   h,
		}
	} else {
		trans := test.transport
		if trans == nil {
			trans = helpers.GetFakeTransport()
		}

		manager = &walker.FetchManager{
			Datastore: ds,
			Handler:   h,
			Transport: trans,
		}
	}

	go manager.Start()
	time.Sleep(duration)
	manager.Stop()
	rs.Stop()

	//
	// Return the mocks
	//
	return TestResults{
		handler:   h,
		datastore: ds,
		manager:   manager,
		server:    rs,
	}
}

func TestUrlParsing(t *testing.T) {
	orig := walker.Config.PurgeSidList
	defer func() {
		walker.Config.PurgeSidList = orig
		walker.PostConfigHooks()
	}()
	walker.Config.PurgeSidList = []string{"jsessionid", "phpsessid"}
	walker.PostConfigHooks()

	tests := []struct {
		tag    string
		input  string
		expect string
	}{
		{
			tag:    "UpCase",
			input:  "HTTP://A.com/page1.com",
			expect: "http://a.com/page1.com",
		},
		{
			tag:    "Fragment",
			input:  "http://a.com/page1.com#Fragment",
			expect: "http://a.com/page1.com",
		},
		{
			tag:    "PathSID",
			input:  "http://a.com/page1.com;jsEssIoniD=436100313FAFBBB9B4DC8BA3C2EC267B",
			expect: "http://a.com/page1.com",
		},
		{
			tag:    "PathSID2",
			input:  "http://a.com/page1.com;phPseSsId=436100313FAFBBB9B4DC8BA3C2EC267B",
			expect: "http://a.com/page1.com",
		},
		{
			tag:    "QuerySID",
			input:  "http://a.com/page1.com?foo=bar&jsessionID=436100313FAFBBB9B4DC8BA3C2EC267B&baz=niffler",
			expect: "http://a.com/page1.com?baz=niffler&foo=bar",
		},
		{
			tag:    "QuerySID2",
			input:  "http://a.com/page1.com?PHPSESSID=436100313FAFBBB9B4DC8BA3C2EC267B",
			expect: "http://a.com/page1.com",
		},
	}

	for _, tst := range tests {
		u, err := walker.ParseURL(tst.input)
		if err != nil {
			t.Fatalf("For tag %q ParseURL failed %v", tst.tag, err)
		}
		got := u.String()
		if got != tst.expect {
			t.Errorf("For tag %q link mismatch got %q, expected %q", tst.tag, got, tst.expect)
		}
	}
}
func TestBasicNoRobots(t *testing.T) {
	walker.Config.AcceptFormats = []string{"text/html", "text/plain"}

	tests := TestSpec{
		hasParsedLinks: true,
		perHost: []PerHost{
			PerHost{
				domain: "norobots.com",
				links: []PerLink{
					PerLink{
						url:      "http://norobots.com/robots.txt",
						response: &helpers.MockResponse{Status: 404},
						hidden:   true,
					},
					PerLink{
						url:      "http://norobots.com/page1.html",
						response: &helpers.MockResponse{Body: html_body},
					},
					PerLink{
						url: "http://norobots.com/page2.html",
					},
					PerLink{
						url: "http://norobots.com/page3.html",
					},
				},
			},
		},
	}

	//
	// Run the fetcher
	//
	results := runFetcher(tests, 1*time.Second, t)

	//
	// Make sure expected results are there
	//

	for _, fr := range results.handlerCalls() {
		switch fr.URL.String() {
		case "http://norobots.com/page1.html":
			contents, _ := ioutil.ReadAll(fr.Response.Body)
			if string(contents) != html_body {
				t.Errorf("For %v, expected:\n%v\n\nBut got:\n%v\n",
					fr.URL, html_body, string(contents))
			}
		case "http://norobots.com/page2.html":
		case "http://norobots.com/page3.html":
		default:
			t.Errorf("Got a Handler.HandleResponse call we didn't expect: %v", fr)
		}
	}

	results.assertExpectations(t)
}

func TestBasicRobots(t *testing.T) {
	tests := TestSpec{
		hasParsedLinks: false,
		perHost: []PerHost{
			PerHost{
				domain: "robotsdelay1.com",
				links: []PerLink{

					PerLink{
						url: "http://robotsdelay1.com/robots.txt",
						response: &helpers.MockResponse{
							Body: "User-agent: *\nCrawl-delay: 1\n",
						},
						hidden: true,
					},

					PerLink{
						url: "http://robotsdelay1.com/page4.html",
					},
					PerLink{
						url: "http://robotsdelay1.com/page5.html",
					},
				},
			},
		},
	}

	//
	// Run the fetcher
	//
	results := runFetcher(tests, 3*time.Second, t)

	//
	// Make sure expected results are there
	//
	count := 0
	for _, fr := range results.handlerCalls() {
		count++
		switch fr.URL.String() {
		case "http://robotsdelay1.com/page4.html":
		case "http://robotsdelay1.com/page5.html":
		default:
			t.Errorf("Got a Handler.HandleResponse call we didn't expect: %v", fr)
		}
	}
	if count != 2 {
		t.Errorf("Got %d handlerCalls, expected 2", count)
	}

	results.assertExpectations(t)
}

func TestBasicMimeType(t *testing.T) {
	orig := walker.Config.AcceptFormats
	defer func() {
		walker.Config.AcceptFormats = orig
	}()
	walker.Config.AcceptFormats = []string{"text/html", "text/plain"}

	tests := TestSpec{
		hasParsedLinks: false,
		perHost: []PerHost{
			PerHost{
				domain: "accept.com",
				links: []PerLink{
					PerLink{
						url:      "http://accept.com/robots.txt",
						response: &helpers.MockResponse{Status: 404},
						hidden:   true,
					},
					PerLink{
						url: "http://accept.com/accept_html.html",
						response: &helpers.MockResponse{
							ContentType: "text/html; charset=ISO-8859-4",
							Body:        html_body_nolinks,
						},
					},
					PerLink{
						url: "http://accept.com/accept_text.txt",
						response: &helpers.MockResponse{
							ContentType: "text/plain",
						},
					},
					PerLink{
						url: "http://accept.com/donthandle",
						response: &helpers.MockResponse{
							ContentType: "foo/bar",
						},
					},
				},
			},
		},
	}

	//
	// Run the fetcher
	//
	results := runFetcher(tests, 3*time.Second, t)

	//
	// Make sure expected results are there
	//
	recvTextHtml := false
	recvTextPlain := false
	for _, fr := range results.handlerCalls() {
		switch fr.URL.String() {
		case "http://accept.com/accept_html.html":
			recvTextHtml = true
		case "http://accept.com/accept_text.txt":
			recvTextPlain = true
		default:
			t.Errorf("Got a Handler.HandleResponse call we didn't expect: %v", fr)
		}
	}
	if !recvTextHtml {
		t.Errorf("Failed to handle explicit Content-Type: text/html")
	}
	if !recvTextPlain {
		t.Errorf("Failed to handle Content-Type: text/plain")
	}

	// Link tests to ensure we resolve URLs to proper absolute forms
	expectedMimesFound := map[string]string{
		"http://accept.com/donthandle":       "foo/bar",
		"http://accept.com/accept_text.txt":  "text/plain",
		"http://accept.com/accept_html.html": "text/html",
	}

	for _, fr := range results.dsStoreURLFetchResultsCalls() {
		link := fr.URL.String()
		mime, mimeOk := expectedMimesFound[link]
		if mimeOk {
			delete(expectedMimesFound, link)
			if fr.MimeType != mime {
				t.Errorf("StoreURLFetchResults for link %v, got mime type %q, expected %q",
					link, fr.MimeType, mime)
			}
		}
	}

	for link := range expectedMimesFound {
		t.Errorf("StoreURLFetchResults expected to find mime type for link %v, but didn't", link)
	}

	results.assertExpectations(t)
}

func TestBasicLinkTest(t *testing.T) {
	walker.Config.AcceptFormats = []string{"text/html", "text/plain"}

	tests := TestSpec{
		hasParsedLinks: true,

		perHost: []PerHost{
			PerHost{
				domain: "linktests.com",
				links: []PerLink{
					PerLink{
						url: "http://linktests.com/links/test.html",
						response: &helpers.MockResponse{
							Body: html_test_links,
						},
					},
				},
			},
		},
	}

	//
	// Run the fetcher
	//
	results := runFetcher(tests, 3*time.Second, t)

	//
	// Make sure expected results are there
	//
	for _, fr := range results.handlerCalls() {
		switch fr.URL.String() {
		case "http://linktests.com/links/test.html":
		default:
			t.Errorf("Got a Handler.HandleResponse call we didn't expect: %v", fr)
		}
	}

	ulst, frlst := results.dsStoreParsedURLCalls()
	count := 0
	for i := range ulst {
		u := ulst[i]
		fr := frlst[i]
		if fr.URL.String() != "http://linktests.com/links/test.html" {
			t.Fatalf("Expected linktest source only")
		}
		count++
		switch u.String() {
		case "http://linktests.com/links/relative-dir/":
		case "http://linktests.com/links/relative-page/page.html":
		case "http://linktests.com/abs-relative-dir/":
		case "http://linktests.com/abs-relative-page/page.html":
		case "https://other.org/abs-dir/":
		case "https://other.org/abs-page/page.html":
		case "http:donot/ignore.html":
		default:
			t.Errorf("StoreParsedURL call we didn't expect: %v", u)
		}
	}

	if count != 7 {
		t.Errorf("Got %d results from dsStoreParsedURLCalls, expected 7")
	}

	results.assertExpectations(t)
}

func TestFetcherBlacklistsPrivateIPs(t *testing.T) {
	orig := walker.Config.BlacklistPrivateIPs
	defer func() { walker.Config.BlacklistPrivateIPs = orig }()
	walker.Config.BlacklistPrivateIPs = true

	tests := TestSpec{
		hasNoLinks: true,
		perHost: []PerHost{
			PerHost{
				domain: "private.com",
				links: []PerLink{
					PerLink{
						url: "http://private.com/page1.html",
						response: &helpers.MockResponse{
							Body: html_test_links,
						},
					},
				},
			},
		},
	}

	results := runFetcher(tests, defaultSleep, t)

	if len(results.handlerCalls()) != 0 || len(results.dsStoreURLFetchResultsCalls()) != 0 {
		t.Error("Did not expect any handler calls due to host resolving to private IP")
	}

	results.assertExpectations(t)
	results.datastore.AssertNotCalled(t, "LinksForHost", "private.com")
}

func TestStillCrawlWhenDomainUnreachable(t *testing.T) {
	orig := walker.Config.BlacklistPrivateIPs
	defer func() { walker.Config.BlacklistPrivateIPs = orig }()
	walker.Config.BlacklistPrivateIPs = true

	tests := TestSpec{
		hasNoLinks: true,
		perHost: []PerHost{
			PerHost{
				domain: "a1234567890bcde.com",
				links: []PerLink{
					PerLink{
						url:      "http://a1234567890bcde.com/",
						response: &helpers.MockResponse{Status: 404},
					},
				},
			},
		},
	}

	results := runFetcher(tests, defaultSleep, t)
	results.assertExpectations(t)
}

func TestFetcherCreatesTransport(t *testing.T) {
	orig := walker.Config.BlacklistPrivateIPs
	defer func() { walker.Config.BlacklistPrivateIPs = orig }()
	walker.Config.BlacklistPrivateIPs = false

	tests := TestSpec{
		hasParsedLinks:    false,
		suppressTransport: true,
		perHost: []PerHost{
			PerHost{
				domain: "localhost.localdomain",
				links: []PerLink{
					PerLink{
						url:      "http://localhost.localdomain/",
						response: &helpers.MockResponse{Status: 404},
					},
				},
			},
		},
	}

	results := runFetcher(tests, defaultSleep, t)

	if results.manager.Transport == nil {
		t.Fatalf("Expected Transport to get set")
	}
	_, ok := results.manager.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Expected Transport to get set to a *http.Transport")
	}

	// It would be great to check that the DNS cache actually got used here,
	// but with the current design there seems to be no way to check it

	results.assertExpectations(t)
}

func TestRedirects(t *testing.T) {
	link := func(index int) string {
		return fmt.Sprintf("http://sub.dom.com/page%d.html", index)
	}

	roundTriper := helpers.MapRoundTrip{
		Responses: map[string]*http.Response{
			link(1): helpers.Response307(link(2)),
			link(2): helpers.Response307(link(3)),
			link(3): helpers.Response200(),
		},
	}

	tests := TestSpec{
		hasParsedLinks: false,
		transport:      &roundTriper,
		perHost: []PerHost{
			PerHost{
				domain: "dom.com",
				links: []PerLink{
					PerLink{
						url: link(1),
					},
				},
			},
		},
	}

	results := runFetcher(tests, defaultSleep, t)

	frs := results.handlerCalls()
	if len(frs) < 1 {
		t.Fatalf("Expected to find calls made to handler, but didn't")
	}
	fr := frs[0]

	if fr.URL.String() != link(1) {
		t.Errorf("URL mismatch, got %q, expected %q", fr.URL.String(), link(1))
	}
	if len(fr.RedirectedFrom) != 2 {
		t.Errorf("RedirectedFrom length mismatch, got %d, expected %d", len(fr.RedirectedFrom), 2)
	}
	if fr.RedirectedFrom[0].String() != link(2) {
		t.Errorf("RedirectedFrom[0] mismatch, got %q, expected %q", fr.RedirectedFrom[0].String(), link(2))
	}
	if fr.RedirectedFrom[1].String() != link(3) {
		t.Errorf("RedirectedFrom[0] mismatch, got %q, expected %q", fr.RedirectedFrom[1].String(), link(3))
	}

	results.assertExpectations(t)

}

func TestHrefWithSpace(t *testing.T) {
	testPage := "http://t.com/page1.html"
	const html_with_href_space = `<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>Test links page</title>
</head>

<div id="menu">
	<a href=" relative-dir/">link</a>
	<a href=" relative-page/page.html">link</a>
	<a href=" /abs-relative-dir/">link</a>
	<a href=" /abs-relative-page/page.html">link</a>
	<a href=" https://other.org/abs-dir/">link</a>
	<a href=" https://other.org/abs-page/page.html">link</a>
</div>
</html>`

	tests := TestSpec{
		hasParsedLinks: true,
		perHost: []PerHost{
			PerHost{
				domain: "t.com",
				links: []PerLink{
					PerLink{
						url: testPage,
						response: &helpers.MockResponse{
							ContentType: "text/html",
							Body:        html_with_href_space,
						},
					},
				},
			},
		},
	}

	results := runFetcher(tests, defaultSleep, t)

	foundTCom := false
	for _, fr := range results.handlerCalls() {
		if fr.URL.String() == testPage {
			foundTCom = true
			break
		}
	}
	if !foundTCom {
		t.Fatalf("Failed to find pushed link %q", testPage)
	}

	expected := map[string]bool{
		"http://t.com/relative-dir/":               true,
		"http://t.com/relative-page/page.html":     true,
		"http://t.com/abs-relative-dir/":           true,
		"http://t.com/abs-relative-page/page.html": true,
		"https://other.org/abs-dir/":               true,
		"https://other.org/abs-page/page.html":     true,
	}

	ulst, frlst := results.dsStoreParsedURLCalls()
	for i := range ulst {
		u := ulst[i]
		fr := frlst[i]
		if fr.URL.String() == testPage {
			if expected[u.String()] {
				delete(expected, u.String())
			} else {
				t.Errorf("StoreParsedURL mismatch found unexpected link %q", u.String())
			}
		}
	}

	for link := range expected {
		t.Errorf("StoreParsedURL didn't find link %q", link)
	}

	results.assertExpectations(t)
}

func TestHttpTimeout(t *testing.T) {
	origTimeout := walker.Config.HttpTimeout
	defer func() {
		walker.Config.HttpTimeout = origTimeout
	}()
	walker.Config.HttpTimeout = "200ms"

	for _, timeoutType := range []string{"wontConnect", "stalledRead"} {

		var transport *helpers.CancelTrackingTransport
		var closer io.Closer
		if timeoutType == "wontConnect" {
			transport, closer = helpers.GetWontConnectTransport()
		} else {
			transport, closer = helpers.GetStallingReadTransport()
		}

		tests := TestSpec{
			hasParsedLinks: true,
			transport:      transport,
			perHost: []PerHost{
				PerHost{
					domain: "t1.com",
					links: []PerLink{
						PerLink{
							url:      "http://t1.com/page1.html",
							response: &helpers.MockResponse{Status: 404},
						},
					},
				},

				PerHost{
					domain: "t2.com",
					links: []PerLink{
						PerLink{
							url:      "http://t2.com/page1.html",
							response: &helpers.MockResponse{Status: 404},
						},
					},
				},

				PerHost{
					domain: "t3.com",
					links: []PerLink{
						PerLink{
							url:      "http://t3.com/page1.html",
							response: &helpers.MockResponse{Status: 404},
						},
					},
				},
			},
		}

		// ds := &helpers.MockDatastore{}
		// ds.On("ClaimNewHost").Return("t1.com").Once()
		// ds.On("LinksForHost", "t1.com").Return([]*walker.URL{
		// 	helpers.Parse("http://t1.com/page1.html"),
		// })
		// ds.On("UnclaimHost", "t1.com").Return()

		// ds.On("ClaimNewHost").Return("t2.com").Once()
		// ds.On("LinksForHost", "t2.com").Return([]*walker.URL{
		// 	helpers.Parse("http://t2.com/page1.html"),
		// })
		// ds.On("UnclaimHost", "t2.com").Return()

		// ds.On("ClaimNewHost").Return("t3.com").Once()
		// ds.On("LinksForHost", "t3.com").Return([]*walker.URL{
		// 	helpers.Parse("http://t3.com/page1.html"),
		// })
		// ds.On("UnclaimHost", "t3.com").Return()

		// ds.On("ClaimNewHost").Return("")

		// ds.On("StoreURLFetchResults", mock.AnythingOfType("*walker.FetchResults")).Return()
		// ds.On("StoreParsedURL",
		// 	mock.AnythingOfType("*walker.URL"),
		// 	mock.AnythingOfType("*walker.FetchResults")).Return()

		// h := &helpers.MockHandler{}
		// h.On("HandleResponse", mock.Anything).Return()

		// manager := &walker.FetchManager{
		// 	Datastore: ds,
		// 	Handler:   h,
		// 	Transport: transport,
		// }

		// go manager.Start()
		// time.Sleep(time.Second * 2)
		// manager.Stop()

		results := runFetcher(tests, defaultSleep, t)
		closer.Close()

		canceled := map[string]bool{}
		for k := range transport.Canceled {
			canceled[k] = true
		}

		expected := map[string]bool{
			"http://t1.com/page1.html": true,
			"http://t2.com/page1.html": true,
			"http://t3.com/page1.html": true,
		}

		for k := range expected {
			if !canceled[k] {
				t.Errorf("For timeoutType %q Expected to find canceled http get for %q, but didn't", timeoutType, k)
			}
		}

		if len(results.handlerCalls()) > 0 {
			t.Fatalf("For timeoutType %q Fetcher shouldn't have been able to connect, but did", timeoutType)
		}
	}
}

func TestMetaNos(t *testing.T) {
	origHonorNoindex := walker.Config.HonorMetaNoindex
	origHonorNofollow := walker.Config.HonorMetaNofollow
	defer func() {
		walker.Config.HonorMetaNoindex = origHonorNoindex
		walker.Config.HonorMetaNofollow = origHonorNofollow
	}()
	walker.Config.HonorMetaNoindex = true
	walker.Config.HonorMetaNofollow = true

	const nofollowHtml string = `<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<meta name="ROBOTS" content="NoFollow">
<title>No Links</title>
</head>
<div id="menu">
	<a href="relative-dir/">link</a>
	<a href="relative-page/page.html">link</a>
	<a href="/abs-relative-dir/">link</a>
	<a href="/abs-relative-page/page.html">link</a>
	<a href="https://other.org/abs-dir/">link</a>
	<a href="https://other.org/abs-page/page.html">link</a>
</div>
</html>`

	const noindexHtml string = `<!DOCTYPE html>
<html>
<head>
<meta name="ROBOTS" content="noindex">
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>No Links</title>
</head>
</html>`

	const bothHtml string = `<!DOCTYPE html>
<html>
<head>
<meta name="ROBOTS" content="noindeX, nofoLLow">
<title>No Links</title>
</head>
<div id="menu">
	<a href="relative-dirX/">link</a>
	<a href="relative-pageX/page.html">link</a>
	<a href="/abs-relative-dirX/">link</a>
	<a href="/abs-relative-pageX/page.html">link</a>
	<a href="https://other.org/abs-dirX/">link</a>
	<a href="https://other.org/abs-pageX/page.html">link</a>
</div>
</html>`

	ds := &helpers.MockDatastore{}
	ds.On("ClaimNewHost").Return("t1.com").Once()
	ds.On("LinksForHost", "t1.com").Return([]*walker.URL{
		helpers.Parse("http://t1.com/nofollow.html"),
		helpers.Parse("http://t1.com/noindex.html"),
		helpers.Parse("http://t1.com/both.html"),
	})
	ds.On("UnclaimHost", "t1.com").Return()
	ds.On("ClaimNewHost").Return("")

	ds.On("StoreURLFetchResults", mock.AnythingOfType("*walker.FetchResults")).Return()
	ds.On("StoreParsedURL",
		mock.AnythingOfType("*walker.URL"),
		mock.AnythingOfType("*walker.FetchResults")).Return()

	h := &helpers.MockHandler{}
	h.On("HandleResponse", mock.Anything).Return()

	rs, err := helpers.NewMockRemoteServer()
	if err != nil {
		t.Fatal(err)
	}
	rs.SetResponse("http://t1.com/nofollow.html", &helpers.MockResponse{
		Body: nofollowHtml,
	})
	rs.SetResponse("http://t1.com/noindex.html", &helpers.MockResponse{
		Body: noindexHtml,
	})
	rs.SetResponse("http://t1.com/both.html", &helpers.MockResponse{
		Body: bothHtml,
	})

	manager := &walker.FetchManager{
		Datastore: ds,
		Handler:   h,
		Transport: helpers.GetFakeTransport(),
	}

	go manager.Start()
	time.Sleep(defaultSleep)
	manager.Stop()

	rs.Stop()

	// Did the fetcher honor noindex (if noindex is set
	// the handler shouldn't be called)
	callCount := 0
	for _, call := range h.Calls {
		fr := call.Arguments.Get(0).(*walker.FetchResults)
		link := fr.URL.String()
		switch link {
		case "http://t1.com/nofollow.html":
			callCount++
		default:
			t.Errorf("Fetcher did not honor noindex in meta link = %s", link)
		}
	}
	if callCount != 1 {
		t.Errorf("Expected call to handler for nofollow.html, but didn't get it")
	}

	// Did the fetcher honor nofollow (if nofollow is set fetcher
	// shouldn't follow any links)
	callCount = 0
	for _, call := range ds.Calls {
		if call.Method == "StoreParsedURL" {
			callCount++
		}
	}
	if callCount != 0 {
		t.Errorf("Fetcher did not honor nofollow in meta: expected 0 callCount, found %d", callCount)
	}
}

func TestFetchManagerFastShutdown(t *testing.T) {
	origDefaultCrawlDelay := walker.Config.DefaultCrawlDelay
	defer func() {
		walker.Config.DefaultCrawlDelay = origDefaultCrawlDelay
	}()
	walker.Config.DefaultCrawlDelay = "1s"

	ds := &helpers.MockDatastore{}
	ds.On("ClaimNewHost").Return("test.com").Once()
	ds.On("LinksForHost", "test.com").Return([]*walker.URL{
		helpers.Parse("http://test.com/page1.html"),
		helpers.Parse("http://test.com/page2.html"),
	})
	ds.On("UnclaimHost", "test.com").Return()
	ds.On("ClaimNewHost").Return("")

	ds.On("StoreURLFetchResults", mock.AnythingOfType("*walker.FetchResults")).Return()

	manager := &walker.FetchManager{
		Datastore: ds,
		Handler:   &helpers.MockHandler{},
		Transport: helpers.GetFakeTransport(),
	}

	go manager.Start()
	time.Sleep(defaultSleep)
	manager.Stop()

	expectedCall := false
	for _, call := range ds.Calls {
		switch call.Method {
		case "StoreURLFetchResults":
			fr := call.Arguments.Get(0).(*walker.FetchResults)
			link := fr.URL.String()
			switch link {
			case "http://test.com/page1.html":
				expectedCall = true
			default:
				t.Errorf("Got unexpected StoreURLFetchResults call for %v", link)
			}
		}
	}
	if !expectedCall {
		t.Errorf("Did not get expected StoreURLFetchResults call for http://test.com/page1.html")
	}

	ds.AssertExpectations(t)
}

func TestObjectEmbedIframeTags(t *testing.T) {
	origHonorNoindex := walker.Config.HonorMetaNoindex
	origHonorNofollow := walker.Config.HonorMetaNofollow
	defer func() {
		walker.Config.HonorMetaNoindex = origHonorNoindex
		walker.Config.HonorMetaNofollow = origHonorNofollow
	}()
	walker.Config.HonorMetaNoindex = true
	walker.Config.HonorMetaNofollow = true

	const html string = `<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>No Links</title>
</head>
<body>
	<object data="/object_data/page.html" />
	<iframe src="/iframe_src/page.html"> </iframe>
	<embed src="/embed_src/page.html" />
	<iframe srcdoc="<a href=/iframe_srcdoc/page.html > Link </a>" />
</body>
</html>`

	// The ifframe that looks like this
	//    <iframe srcdoc="<a href = \"/iframe_srcdoc/page.html\" > Link </a>" />
	// does not appear to be handled correctly by golang-html. The embedded quotes
	// are failing. But the version I have above does work (even though it's wonky)

	ds := &helpers.MockDatastore{}
	ds.On("ClaimNewHost").Return("t1.com").Once()
	ds.On("LinksForHost", "t1.com").Return([]*walker.URL{
		helpers.Parse("http://t1.com/target.html"),
	})
	ds.On("UnclaimHost", "t1.com").Return()
	ds.On("ClaimNewHost").Return("")

	ds.On("StoreURLFetchResults", mock.AnythingOfType("*walker.FetchResults")).Return()
	ds.On("StoreParsedURL",
		mock.AnythingOfType("*walker.URL"),
		mock.AnythingOfType("*walker.FetchResults")).Return()

	h := &helpers.MockHandler{}
	h.On("HandleResponse", mock.Anything).Return()

	rs, err := helpers.NewMockRemoteServer()
	if err != nil {
		t.Fatal(err)
	}
	rs.SetResponse("http://t1.com/target.html", &helpers.MockResponse{
		Body: html,
	})
	rs.SetResponse("http://t1.com/object_data/page.html", &helpers.MockResponse{Status: 404})
	rs.SetResponse("http://t1.com/iframe_srcdoc/page.html", &helpers.MockResponse{Status: 404})
	rs.SetResponse("http://t1.com/iframe_src/page.html", &helpers.MockResponse{Status: 404})
	rs.SetResponse("http://t1.com/embed_src/page.html", &helpers.MockResponse{Status: 404})

	manager := &walker.FetchManager{
		Datastore: ds,
		Handler:   h,
		Transport: helpers.GetFakeTransport(),
	}

	go manager.Start()
	time.Sleep(defaultSleep)
	manager.Stop()

	rs.Stop()

	expectedStores := map[string]bool{
		"http://t1.com/object_data/page.html":   true,
		"http://t1.com/iframe_srcdoc/page.html": true,
		"http://t1.com/iframe_src/page.html":    true,
		"http://t1.com/embed_src/page.html":     true,
	}

	for _, call := range ds.Calls {
		if call.Method == "StoreParsedURL" {
			u := call.Arguments.Get(0).(*walker.URL)
			if expectedStores[u.String()] {
				delete(expectedStores, u.String())
			}
		}
	}

	for link := range expectedStores {
		t.Errorf("Expected to encounter link %q, but didn't", link)
	}
}

func TestPathInclusion(t *testing.T) {
	origHonorNoindex := walker.Config.ExcludeLinkPatterns
	origHonorNofollow := walker.Config.IncludeLinkPatterns
	defer func() {
		walker.Config.ExcludeLinkPatterns = origHonorNoindex
		walker.Config.IncludeLinkPatterns = origHonorNofollow
	}()
	walker.Config.ExcludeLinkPatterns = []string{`\.mov$`, "janky", `\/foo\/bang`, `^\/root$`}
	walker.Config.IncludeLinkPatterns = []string{`\.keep$`}

	const html string = `<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>No Links</title>
</head>
<body>
	<div id="menu">
		<a href="/foo/bar.html">yes</a>
		<a href="/foo/bar.mov">no</a>
		<a href="/foo/mov.bar">yes</a>
		<a href="/janky/page.html">no</a>
		<a href="/foo/janky.html">no</a>
		<a href="/foo/bang/baz.html">no</a>
		<a href="/foo/bang/baz.keep">yes</a>
		<a href="/root">no</a>
		<a href="/root/more">yes</a>
	</div>
</body>
</html>`

	ds := &helpers.MockDatastore{}
	ds.On("ClaimNewHost").Return("t1.com").Once()
	ds.On("LinksForHost", "t1.com").Return([]*walker.URL{
		helpers.Parse("http://t1.com/target.html"),
	})
	ds.On("UnclaimHost", "t1.com").Return()
	ds.On("ClaimNewHost").Return("")

	ds.On("StoreURLFetchResults", mock.AnythingOfType("*walker.FetchResults")).Return()
	ds.On("StoreParsedURL",
		mock.AnythingOfType("*walker.URL"),
		mock.AnythingOfType("*walker.FetchResults")).Return()

	h := &helpers.MockHandler{}
	h.On("HandleResponse", mock.Anything).Return()

	rs, err := helpers.NewMockRemoteServer()
	if err != nil {
		t.Fatal(err)
	}
	rs.SetResponse("http://t1.com/target.html", &helpers.MockResponse{
		Body: html,
	})
	expectedPaths := map[string]bool{
		"/foo/bar.html":      true,
		"/foo/mov.bar":       true,
		"/foo/bang/baz.keep": true,
		"/root/more":         true,
	}
	for path := range expectedPaths {
		rs.SetResponse(fmt.Sprintf("http://t1.com%s", path), &helpers.MockResponse{Status: 404})
	}

	manager := &walker.FetchManager{
		Datastore: ds,
		Handler:   h,
		Transport: helpers.GetFakeTransport(),
	}

	go manager.Start()
	time.Sleep(defaultSleep)
	manager.Stop()

	rs.Stop()

	for _, call := range ds.Calls {
		if call.Method == "StoreParsedURL" {
			u := call.Arguments.Get(0).(*walker.URL)
			if expectedPaths[u.RequestURI()] {
				delete(expectedPaths, u.RequestURI())
			} else {
				t.Errorf("Unexected call to StoreParsedURL for link %v", u)
			}
		}
	}

	for path := range expectedPaths {
		t.Errorf("StoreParsedURL not called for %v, but should have been", path)
	}

}

func TestMaxCrawlDealy(t *testing.T) {
	// The approach to this test is simple. Set a very high Crawl-delay from
	// the host, and set a small MaxCrawlDelay in config. Then only allow the
	// fetcher to run long enough to get all the links IF the fetcher is honoring
	// the MaxCrawlDelay
	origDefaultCrawlDelay := walker.Config.DefaultCrawlDelay
	origMaxCrawlDelay := walker.Config.MaxCrawlDelay
	defer func() {
		walker.Config.DefaultCrawlDelay = origDefaultCrawlDelay
		walker.Config.MaxCrawlDelay = origMaxCrawlDelay
	}()
	walker.Config.MaxCrawlDelay = "100ms" //compare this with the Crawl-delay below
	walker.Config.DefaultCrawlDelay = "0s"

	ds := &helpers.MockDatastore{}
	ds.On("ClaimNewHost").Return("a.com").Once()
	ds.On("LinksForHost", "a.com").Return([]*walker.URL{
		helpers.Parse("http://a.com/page1.html"),
		helpers.Parse("http://a.com/page2.html"),
		helpers.Parse("http://a.com/page3.html"),
	})
	ds.On("UnclaimHost", "a.com").Return()

	// This last call will make ClaimNewHost return "" on each subsequent call,
	// which will put the fetcher to sleep.
	ds.On("ClaimNewHost").Return("")

	ds.On("StoreURLFetchResults", mock.AnythingOfType("*walker.FetchResults")).Return()
	ds.On("StoreParsedURL",
		mock.AnythingOfType("*walker.URL"),
		mock.AnythingOfType("*walker.FetchResults")).Return()

	h := &helpers.MockHandler{}
	h.On("HandleResponse", mock.Anything).Return()

	rs, err := helpers.NewMockRemoteServer()
	if err != nil {
		t.Fatal(err)
	}
	rs.SetResponse("http://a.com/robots.txt", &helpers.MockResponse{
		Body: "User-agent: *\nCrawl-delay: 120\n", // this is 120 seconds, compare to MaxCrawlDelay above
	})
	rs.SetResponse("http://a.com/page1.html", &helpers.MockResponse{Status: 404})
	rs.SetResponse("http://a.com/page2.html", &helpers.MockResponse{Status: 404})
	rs.SetResponse("http://a.com/page3.html", &helpers.MockResponse{Status: 404})

	manager := &walker.FetchManager{
		Datastore: ds,
		Handler:   h,
		Transport: helpers.GetFakeTransport(),
	}

	go manager.Start()
	time.Sleep(time.Second * 1)
	manager.Stop()
	rs.Stop()

	expectedPages := map[string]bool{
		"/page1.html": true,
		"/page2.html": true,
		"/page3.html": true,
	}

	for _, call := range ds.Calls {
		if call.Method == "StoreURLFetchResults" {
			fr := call.Arguments.Get(0).(*walker.FetchResults)
			domain, err := fr.URL.ToplevelDomainPlusOne()
			if err != nil {
				panic(err)
			}
			path := fr.URL.RequestURI()
			if domain != "a.com" {
				t.Fatalf("Domain mismatch -- this shouldn't happen")
			}
			if !expectedPages[path] {
				t.Errorf("Path mistmatch, didn't find path %q in expectedPages", path)
			} else {
				delete(expectedPages, path)
			}
		}
	}

	for path := range expectedPages {
		t.Errorf("Didn't find expected page %q in mock data store", path)
	}

}

func TestFnvFingerprint(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>No Links</title>
</head>
<div>
	Roses are red, violets are blue, golang is the bomb, aint it so true!
</div>
</html>`

	ds := &helpers.MockDatastore{}
	ds.On("ClaimNewHost").Return("a.com").Once()
	ds.On("LinksForHost", "a.com").Return([]*walker.URL{
		helpers.Parse("http://a.com/page1.html"),
	})
	ds.On("UnclaimHost", "a.com").Return()

	// This last call will make ClaimNewHost return "" on each subsequent call,
	// which will put the fetcher to sleep.
	ds.On("ClaimNewHost").Return("")

	ds.On("StoreURLFetchResults", mock.AnythingOfType("*walker.FetchResults")).Return()
	ds.On("StoreParsedURL",
		mock.AnythingOfType("*walker.URL"),
		mock.AnythingOfType("*walker.FetchResults")).Return()

	h := &helpers.MockHandler{}
	h.On("HandleResponse", mock.Anything).Return()

	rs, err := helpers.NewMockRemoteServer()
	if err != nil {
		t.Fatal(err)
	}
	rs.SetResponse("http://a.com/robots.txt", &helpers.MockResponse{Status: 404})
	rs.SetResponse("http://a.com/page1.html", &helpers.MockResponse{
		Body: html,
	})

	manager := &walker.FetchManager{
		Datastore: ds,
		Handler:   h,
		Transport: helpers.GetFakeTransport(),
	}

	go manager.Start()
	time.Sleep(time.Second * 1)
	manager.Stop()
	rs.Stop()

	fnv := fnv.New64()
	fnv.Write([]byte(html))
	fp := int64(fnv.Sum64())

	expectedFps := map[string]int64{
		"/page1.html": fp,
	}

	for _, call := range ds.Calls {
		if call.Method == "StoreURLFetchResults" {
			fr := call.Arguments.Get(0).(*walker.FetchResults)
			path := fr.URL.RequestURI()
			expFp, expFpOk := expectedFps[path]
			if !expFpOk {
				t.Errorf("Path mistmatch, didn't find path %q in expectedFps", path)
				continue
			}

			if expFp != fr.FnvFingerprint {
				t.Errorf("Fingerprint mistmatch, got %x, expected %x", fr.FnvFingerprint, expFp)
			}

			delete(expectedFps, path)
		}
	}

	for path := range expectedFps {
		t.Errorf("Didn't find expected page %q in mock data store", path)
	}
}

func TestIfModifiedSince(t *testing.T) {
	url := helpers.Parse("http://a.com/page1.html")
	url.LastCrawled = time.Now()

	ds := &helpers.MockDatastore{}
	ds.On("ClaimNewHost").Return("a.com").Once()
	ds.On("LinksForHost", "a.com").Return([]*walker.URL{
		url,
	})
	ds.On("UnclaimHost", "a.com").Return()

	ds.On("ClaimNewHost").Return("")

	ds.On("StoreURLFetchResults", mock.AnythingOfType("*walker.FetchResults")).Return()
	ds.On("StoreParsedURL",
		mock.AnythingOfType("*walker.URL"),
		mock.AnythingOfType("*walker.FetchResults")).Return()

	h := &helpers.MockHandler{}
	h.On("HandleResponse", mock.Anything).Return()

	rs, err := helpers.NewMockRemoteServer()
	if err != nil {
		t.Fatal(err)
	}
	rs.SetResponse("http://a.com/robots.txt", &helpers.MockResponse{Status: 404})
	rs.SetResponse("http://a.com/page1.html", &helpers.MockResponse{Status: 304})

	manager := &walker.FetchManager{
		Datastore: ds,
		Handler:   h,
		Transport: helpers.GetFakeTransport(),
	}

	go manager.Start()
	time.Sleep(time.Second * 1)
	manager.Stop()
	rs.Stop()

	//
	// Did the server see the header
	//
	headers, err := rs.Headers("GET", url.String(), -1)
	if err != nil {
		t.Fatalf("rs.Headers failed %v", err)
	}
	mod, modOk := headers["If-Modified-Since"]
	if !modOk {
		t.Fatalf("Failed to find If-Modified-Since in request header for link %q", url.String())
	} else if lm := url.LastCrawled.Format(time.RFC1123); lm != mod[0] {
		t.Errorf("If-Modified-Since has bad format, got %q, expected %q", mod[0], lm)
	}

	//
	// Did the data store get called correctly
	//
	count := 0
	for _, call := range ds.Calls {
		if call.Method == "StoreURLFetchResults" {
			count++
			fr := call.Arguments.Get(0).(*walker.FetchResults)
			if fr.URL.String() != url.String() {
				t.Errorf("DS URL link mismatch: got %q, expected %q", fr.URL.String(), url.String())
			}
			if fr.Response.StatusCode != 304 {
				t.Errorf("DS StatusCode mismatch: got %d, expected %d", fr.Response.StatusCode, 304)
			}
		}
	}
	if count < 1 {
		t.Errorf("Expected to find DS call, but didn't")
	}

	//
	// Did the handler get called
	//
	count = 0
	for _, call := range h.Calls {
		count++
		fr := call.Arguments.Get(0).(*walker.FetchResults)
		if fr.URL.String() != url.String() {
			t.Errorf("Handler URL link mismatch: got %q, expected %q", fr.URL.String(), url.String())
		}
		if fr.Response.StatusCode != 304 {
			t.Errorf("Handler StatusCode mismatch: got %d, expected %d", fr.Response.StatusCode, 304)
		}
	}
	if count < 1 {
		t.Errorf("Expected to find Handler call, but didn't")
	}
}

func TestNestedRobots(t *testing.T) {
	ds := &helpers.MockDatastore{}
	ds.On("ClaimNewHost").Return("dom.com").Once()
	ds.On("LinksForHost", "dom.com").Return([]*walker.URL{
		helpers.Parse("http://dom.com/page1.html"),
		helpers.Parse("http://ok.dom.com/page1.html"),
		helpers.Parse("http://blocked.dom.com/page1.html"),
	})
	ds.On("UnclaimHost", "dom.com").Return()

	ds.On("ClaimNewHost").Return("")

	ds.On("StoreURLFetchResults", mock.AnythingOfType("*walker.FetchResults")).Return()
	ds.On("StoreParsedURL",
		mock.AnythingOfType("*walker.URL"),
		mock.AnythingOfType("*walker.FetchResults")).Return()

	h := &helpers.MockHandler{}
	h.On("HandleResponse", mock.Anything).Return()

	rs, err := helpers.NewMockRemoteServer()
	if err != nil {
		t.Fatal(err)
	}

	rs.SetResponse("http://dom.com/robots.txt", &helpers.MockResponse{
		Body: "User-agent: *\n",
	})
	rs.SetResponse("http://ok.dom.com/robots.txt", &helpers.MockResponse{Status: 404})
	rs.SetResponse("http://blocked.dom.com/robots.txt", &helpers.MockResponse{
		Body: "User-agent: *\nDisallow: /\n",
	})

	rs.SetResponse("http://dom.com/page1.html", &helpers.MockResponse{Status: 404})
	rs.SetResponse("http://ok.dom.com/page1.html", &helpers.MockResponse{Status: 404})
	rs.SetResponse("http://blocked.dom.com/page1.html", &helpers.MockResponse{Status: 404})

	manager := &walker.FetchManager{
		Datastore: ds,
		Handler:   h,
		Transport: helpers.GetFakeTransport(),
	}

	go manager.Start()
	time.Sleep(time.Second * 1)
	manager.Stop()
	rs.Stop()

	//
	// Now check that the correct requests where made
	//
	tests := []struct {
		link    string
		fetched bool
	}{
		{"http://notinvolved.com/page1.html", false},

		{"http://dom.com/robots.txt", true},
		{"http://ok.dom.com/robots.txt", true},
		{"http://blocked.dom.com/robots.txt", true},

		{"http://dom.com/page1.html", true},
		{"http://ok.dom.com/page1.html", true},
		{"http://blocked.dom.com/page1.html", false},
	}

	for _, tst := range tests {
		req := rs.Requested("GET", tst.link)
		if tst.fetched && !req {
			t.Errorf("Expected to have requested link %q, but didn't", tst.link)
		} else if !tst.fetched && req {
			t.Errorf("Expected NOT to have requested link %q, but did", tst.link)
		}
	}
}

func TestMaxContentSize(t *testing.T) {
	orig := walker.Config.MaxHTTPContentSizeBytes
	defer func() {
		walker.Config.MaxHTTPContentSizeBytes = orig
	}()
	walker.Config.MaxHTTPContentSizeBytes = 10

	html := `<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>No Links</title>
</head>
<div>
	Roses are red, violets are blue, golang is the bomb, aint it so true!
</div>
</html>`

	ds := &helpers.MockDatastore{}
	ds.On("ClaimNewHost").Return("a.com").Once()
	ds.On("LinksForHost", "a.com").Return([]*walker.URL{
		helpers.Parse("http://a.com/page1.html"),
		helpers.Parse("http://a.com/page2.html"),
	})
	ds.On("UnclaimHost", "a.com").Return()

	// This last call will make ClaimNewHost return "" on each subsequent call,
	// which will put the fetcher to sleep.
	ds.On("ClaimNewHost").Return("")

	ds.On("StoreURLFetchResults", mock.AnythingOfType("*walker.FetchResults")).Return()
	ds.On("StoreParsedURL",
		mock.AnythingOfType("*walker.URL"),
		mock.AnythingOfType("*walker.FetchResults")).Return()

	h := &helpers.MockHandler{}
	h.On("HandleResponse", mock.Anything).Return()

	rs, err := helpers.NewMockRemoteServer()
	if err != nil {
		t.Fatal(err)
	}

	rs.SetResponse("http://a.com/robots.txt", &helpers.MockResponse{Status: 404})
	rs.SetResponse("http://a.com/page1.html", &helpers.MockResponse{
		Body: html,
	})
	rs.SetResponse("http://a.com/page2.html", &helpers.MockResponse{
		Body:          "0123456789 ",
		ContentType:   "text/html",
		ContentLength: 11,
	})

	manager := &walker.FetchManager{
		Datastore: ds,
		Handler:   h,
		Transport: helpers.GetFakeTransport(),
	}

	go manager.Start()
	time.Sleep(time.Second * 1)
	manager.Stop()
	rs.Stop()

	if len(h.Calls) != 0 {
		links := ""
		for _, call := range h.Calls {
			fr := call.Arguments.Get(0).(*walker.FetchResults)
			links += "\t"
			links += fr.URL.String()
			links += "\n"
		}
		t.Fatalf("Expected handler to be called 0 times, instead it was called %d times for links\n%s\n", len(h.Calls), links)
	}

	page1Ok := false
	page2Ok := false
	for _, call := range ds.Calls {
		if call.Method == "StoreURLFetchResults" {
			fr := call.Arguments.Get(0).(*walker.FetchResults)
			link := fr.URL.String()
			switch link {
			case "http://a.com/page1.html":
				page1Ok = true
			case "http://a.com/page2.html":
				page2Ok = true
			default:
				t.Errorf("Unexpected stored url %q", link)
			}
		}
	}
	if !page1Ok {
		t.Errorf("Didn't find link http://a.com/page1.html in datastore calls, but expected too")
	}
	if !page2Ok {
		t.Errorf("Didn't find link http://a.com/page2.html in datastore calls, but expected too")
	}
}
