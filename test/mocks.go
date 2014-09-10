package test

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/iParadigms/walker"
	"github.com/stretchr/testify/mock"
)

// MockDatastore used for testing components that interact with the datastore
type MockDatastore struct {
	mock.Mock
}

func (ds *MockDatastore) StoreParsedURL(u *url.URL, res *walker.FetchResults) {
	ds.Mock.Called(u, res)
}

func (ds *MockDatastore) StoreURLFetchResults(res *walker.FetchResults) {
	ds.Mock.Called(res)
}

func (ds *MockDatastore) ClaimNewHost() string {
	args := ds.Mock.Called()
	return args.String(0)
}

func (ds *MockDatastore) LinksForHost(domain string) <-chan *url.URL {
	args := ds.Mock.Called(domain)
	urls := args.Get(0).([]*url.URL)
	ch := make(chan *url.URL, len(urls))
	for _, u := range urls {
		ch <- u
	}
	close(ch)
	return ch
}

// MockHandler used for testing that the crawl manager properly calls handlers
type MockHandler struct {
	mock.Mock
}

func (h *MockHandler) HandleResponse(res *walker.FetchResults) {
	h.Mock.Called(res)
}

// MockResponse is the source object used to build fake responses in
// MockHTTPHandler.
type MockResponse struct {
	// Status defaults to 200
	Status int

	// Status defaults to "GET"
	Method string

	// Body defaults to nil (no response body)
	Body string

	//ContentType defaults to "text/html"
	ContentType string
}

// MockHTTPHandler implements http.Handler to serve mock requests.
//
// It is not a mere mock.Mock object because using `.Return()` to return
// *http.Response objects is hard to do, and this provides conveniences in our
// tests.
//
// It should be instantiated with `NewMockRemoteServer()`
type MockHTTPHandler struct {
	// returns keeps track of mock calls and what to respond with. The top
	// level map is by method, i.e. returns["GET"]["http://test.com/"] => an
	// expected response
	returns map[string]map[string]*MockResponse
}

func NewMockHTTPHandler() *MockHTTPHandler {
	s := new(MockHTTPHandler)
	s.returns = map[string]map[string]*MockResponse{
		"DELETE":  map[string]*MockResponse{},
		"GET":     map[string]*MockResponse{},
		"HEAD":    map[string]*MockResponse{},
		"OPTIONS": map[string]*MockResponse{},
		"POST":    map[string]*MockResponse{},
		"PUT":     map[string]*MockResponse{},
		"TRACE":   map[string]*MockResponse{},
	}
	return s
}

// SetResponse sets a mock response for the server to return when it sees an
// incoming request matching the given link. The link should have a scheme and
// host (ex. "http://test.com/stuff"). Empty fields on MockResponse will be
// filled in with default values (see MockResponse)
func (s *MockHTTPHandler) SetResponse(link string, r *MockResponse) {
	if r.Method == "" {
		r.Method = "GET"
	}
	m := s.returns[r.Method]
	m[link] = r
}

func (s *MockHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		r.URL.Scheme = "http"
	} else {
		r.URL.Scheme = "https"
	}
	r.URL.Host = r.Host

	m, ok := s.returns[r.Method]
	if !ok {
		panic(fmt.Sprintf("Got an http method we didn't expect: %v", r.Method))
	}
	res, ok := m[r.URL.String()]
	if !ok {
		// No particular response requested, just return 200 OK return
		return
	}

	if res.Status == 0 {
		res.Status = 200
	}
	if res.Method == "" {
		res.Method = "GET"
	}
	if res.ContentType == "" {
		res.ContentType = "text/html"
	}

	body := strings.NewReader(res.Body)
	httpRes := &http.Response{
		Request: &http.Request{
			Method: res.Method,
		},
		StatusCode:    res.Status,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Body:          NoopCloser{body},
		ContentLength: int64(body.Len()),

		// Values also considered by Response.Write:
		//Trailer
		//TransferEncoding
		//Header -- this seems false, it does not actually do anything with
		//			Header, we need to write it ourselves to the ResponseWriter (don't
		//			know why)
	}
	w.Header().Set("Content-Type", res.ContentType)

	err := httpRes.Write(w)
	if err != nil {
		panic(fmt.Sprintf("Failed to write response for page %v, err: %v", r.URL, err))
	}
}

// MockRemoteServer wraps MockHTTPHandler to start a fake server for the user.
// Use `NewMockRemoteServer()`
type MockRemoteServer struct {
	*MockHTTPHandler
	listener net.Listener
}

// NewMockRemoteServer starts a server listening on port 80. It wraps
// MockHTTPHandler so mock return values can be set. Stop should be called at
// the end of the test to stop the server.
func NewMockRemoteServer() (*MockRemoteServer, error) {
	rs := new(MockRemoteServer)
	rs.MockHTTPHandler = NewMockHTTPHandler()
	var err error
	rs.listener, err = net.Listen("tcp", ":80")
	if err != nil {
		return nil, fmt.Errorf("Failed to listen on port 80, you probably do "+
			"not have sufficient privileges to run this test (source error: %v", err)
	}
	go http.Serve(rs.listener, rs)
	return rs, nil
}

func (rs *MockRemoteServer) Stop() {
	rs.listener.Close()
}
