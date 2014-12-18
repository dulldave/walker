package cmd

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iParadigms/walker"
	"github.com/iParadigms/walker/cassandra"
	"github.com/iParadigms/walker/helpers"

	"github.com/stretchr/testify/mock"
)

//TODO: we currently do not test the console command since we haven't mocked
//		the model yet

func TestCommandsReadConfig(t *testing.T) {
	orig := os.Args
	defer func() {
		os.Args = orig
		// Reset config for the remaining tests
		helpers.LoadTestConfig("test-walker.yaml")
	}()

	handler := &helpers.MockHandler{}
	Handler(handler)

	datastore := &helpers.MockDatastore{}
	datastore.On("ClaimNewHost").Return("")
	datastore.On("ClaimNewHost").Return("")
	datastore.On("StoreParsedURL", mock.Anything, mock.Anything).Return()
	datastore.On("KeepAlive").Return(nil)
	Datastore(datastore)

	dispatcher := &helpers.MockDispatcher{}
	dispatcher.On("StartDispatcher").Return(nil)
	dispatcher.On("StopDispatcher").Return(nil)
	Dispatcher(dispatcher)

	var walkerCommands = []string{"crawl", "fetch", "dispatch", "seed"}
	for _, walkerCom := range walkerCommands {
		helpers.LoadTestConfig("test-walker.yaml")
		expectedDefaultAgent := "Walker (http://github.com/iParadigms/walker)"
		if walker.Config.Fetcher.UserAgent != expectedDefaultAgent {
			t.Fatalf("Failed to reset default config value (user_agent), expected: %v\nBut got: %v",
				expectedDefaultAgent, walker.Config.Fetcher.UserAgent)
		}

		conf := path.Join(helpers.GetTestFileDir(), "test-walker2.yaml")
		switch walkerCom {
		case "seed":
			os.Args = []string{os.Args[0], walkerCom, "--url=http://test.com", "--config=" + conf}
		default:
			os.Args = []string{os.Args[0], walkerCom, "--config=" + conf}
		}

		go func() {
			time.Sleep(100 * time.Millisecond)
			syscall.Kill(os.Getpid(), syscall.SIGINT)
		}()
		Execute()

		expectedTestAgent := "Test Agent (set in yaml)"
		if walker.Config.Fetcher.UserAgent != expectedTestAgent {
			t.Errorf("Failed to set config value (user_agent) via yaml, expected: %v\nBut got: %v",
				expectedTestAgent, walker.Config.Fetcher.UserAgent)
		}
	}
}

func TestCrawlCommand(t *testing.T) {
	orig := os.Args
	defer func() { os.Args = orig }()

	args := [][]string{
		//[]string{os.Args[0], "crawl"}, // console tests not currently enabled
		[]string{os.Args[0], "crawl", "--no-console"},
	}

	for index := range args {
		handler := &helpers.MockHandler{}
		Handler(handler)

		datastore := &helpers.MockDatastore{}
		datastore.On("ClaimNewHost").Return("")
		datastore.On("KeepAlive").Return(nil)
		Datastore(datastore)

		dispatcher := &helpers.MockDispatcher{}
		dispatcher.On("StartDispatcher").Return(nil)
		dispatcher.On("StopDispatcher").Return(nil)
		Dispatcher(dispatcher)

		os.Args = args[index]

		go func() {
			time.Sleep(5 * time.Millisecond)
			syscall.Kill(os.Getpid(), syscall.SIGINT)
		}()
		Execute()

		handler.AssertExpectations(t)
		datastore.AssertExpectations(t)
		dispatcher.AssertExpectations(t)
	}
}

func TestFetchCommand(t *testing.T) {
	handler := &helpers.MockHandler{}
	Handler(handler)

	datastore := &helpers.MockDatastore{}
	datastore.On("ClaimNewHost").Return("")
	datastore.On("KeepAlive").Return(nil)
	Datastore(datastore)

	// Set the dispatcher to ensure it doesn't receive any calls
	dispatcher := &helpers.MockDispatcher{}
	Dispatcher(dispatcher)

	orig := os.Args
	defer func() { os.Args = orig }()
	os.Args = []string{os.Args[0], "fetch"}

	go func() {
		time.Sleep(5 * time.Millisecond)
		syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()
	Execute()

	handler.AssertExpectations(t)
	datastore.AssertExpectations(t)
	dispatcher.AssertExpectations(t)
}

func TestDispatchCommand(t *testing.T) {
	// Set a handler and datastore to ensure they doesn't receive any calls
	handler := &helpers.MockHandler{}
	Handler(handler)

	datastore := &helpers.MockDatastore{}
	Datastore(datastore)

	dispatcher := &helpers.MockDispatcher{}
	dispatcher.On("StartDispatcher").Return(nil)
	dispatcher.On("StopDispatcher").Return(nil)
	Dispatcher(dispatcher)

	orig := os.Args
	defer func() { os.Args = orig }()
	os.Args = []string{os.Args[0], "dispatch"}

	go func() {
		time.Sleep(5 * time.Millisecond)
		syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()
	Execute()

	handler.AssertExpectations(t)
	datastore.AssertExpectations(t)
	dispatcher.AssertExpectations(t)
}

func TestSeedCommand(t *testing.T) {
	u, _ := walker.ParseURL("http://test.com")
	datastore := &helpers.MockDatastore{}
	datastore.On("StoreParsedURL", u, mock.AnythingOfType("*walker.FetchResults")).Return("")
	Datastore(datastore)

	orig := os.Args
	defer func() { os.Args = orig }()
	os.Args = []string{os.Args[0], "seed", "--url=" + u.String()}

	go func() {
		time.Sleep(5 * time.Millisecond)
		syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()
	Execute()

	datastore.AssertExpectations(t)
}

func TestSchemaCommand(t *testing.T) {
	orig := os.Args
	defer func() { os.Args = orig }()
	os.Args = []string{os.Args[0], "schema", "--out=test.cql"}
	Execute()
	defer os.Remove("test.cql")

	f, err := ioutil.ReadFile("test.cql")
	if err != nil {
		t.Fatalf("Failed to read test.cql: %v", err)
	}
	if !strings.HasPrefix(string(f), "-- The schema file for walker") {
		t.Fatalf("test.cql has unexpected contents: %v", f)
	}
}

type ExitCarrier struct {
	stat int
}

// executeInSandbox sets the commander up so that we can capture stdout, stderr, and exit status.
// The return values are
//  (a) stdout string
//  (b) stderr string
//  (c) exit status integer (exit status is < 0 if exit was not called by the calling program)
func executeInSandbox(t *testing.T) (out string, err string, status int) {
	origPf := commander.printf
	origEf := commander.errorf
	origEx := commander.exit

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	commander.printf = func(format string, args ...interface{}) {
		stdout.WriteString(fmt.Sprintf(format, args...))
	}

	commander.errorf = func(format string, args ...interface{}) {
		stderr.WriteString(fmt.Sprintf(format, args...))
	}

	commander.exit = func(status int) {
		panic(&ExitCarrier{stat: status})
	}

	defer func() {
		out = stdout.String()
		err = stderr.String()
		status = -1

		thrown := recover()
		if thrown != nil {
			ec, ecOk := thrown.(*ExitCarrier)
			if !ecOk {
				// Forward any panics not ExitCarrier
				panic(fmt.Sprintf("Unexpected exception in executeInSandbox:\n%v", thrown))
			}
			status = ec.stat
		}

		commander.printf = origPf
		commander.errorf = origEf
		commander.exit = origEx
	}()

	Execute()
	return
}

func compareLongString(leftStr string, rightStr string) (match bool, leftLine string, rightLine string) {
	left := strings.Split(leftStr, "\n")
	right := strings.Split(rightStr, "\n")

	for i := 0; ; i++ {
		if i >= len(left) && i >= len(right) {
			match = true
			return
		} else if i >= len(left) {
			leftLine = "<<<no data>>>"
			rightLine = right[i]
			return
		} else if i >= len(right) {
			leftLine = left[i]
			rightLine = "<<<no data>>>"
			return
		}

		l := strings.TrimSpace(left[i])
		r := strings.TrimSpace(right[i])
		if l != r {
			leftLine = left[i]
			rightLine = right[i]
			return
		}
	}

	return
}

func TestReadlinkCommand(t *testing.T) {
	// Define some useful constants
	goodUrl, _ := walker.ParseURL("http://test.com/page1.com")
	crawlTime, _ := time.Parse("Mon Jan 2 15:04:05 -0700 MST 2006", "Mon Jan 2 15:04:05 -0700 MST 2006")
	body := `<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>No Links</title>
</head>
<div>
	Roses are red, violets are blue, golang is the bomb, aint it so true!
</div>
</html>`

	headers := http.Header{
		"foo": []string{"bar"},
		"baz": []string{"click", "clack"},
	}

	// Define some link infos
	notYetCrawledLinfo := cassandra.LinkInfo{
		URL:       goodUrl,
		CrawlTime: walker.NotYetCrawled,
	}

	goodLinfo := cassandra.LinkInfo{
		URL:            goodUrl,
		Status:         200,
		CrawlTime:      crawlTime,
		Error:          "A nice long\nError\nwith plenty of \nnewlines and such\n",
		RobotsExcluded: false,
		RedirectedTo:   "",
		GetNow:         true,
		Mime:           "text/html",
		Body:           body,
		Headers:        headers,
	}

	// Define test table
	tests := []struct {
		tag    string
		call   []string
		linfo  *cassandra.LinkInfo
		stdout string
		stderr string
		estat  int
	}{

		{
			tag:    "linkNotThere",
			call:   []string{os.Args[0], "readlink", "-u", goodUrl.String()},
			linfo:  nil,
			stderr: "Failed to find link http://test.com/page1.com in datastore",
			estat:  1,
		},

		{
			tag:    "notYetCrawled",
			call:   []string{os.Args[0], "readlink", "-u", goodUrl.String()},
			linfo:  &notYetCrawledLinfo,
			stdout: "Link http://test.com/page1.com is present, but has not yet been fetched",
			estat:  0,
		},

		{
			tag:   "standard",
			call:  []string{os.Args[0], "readlink", "-u", goodUrl.String()},
			linfo: &goodLinfo,
			estat: 0,
			stdout: `Url:            http://test.com/page1.com
HttpStatus:     200
CrawlTime:      2006-01-02 15:04:05 -0700 MST
Error:          
RobotsExcluded: false
RedirectedTo:   
GetNow:         true
Mime:           text/html
FnvFingerprint: 0
HEADERS:
    foo: bar
    baz: click
    baz: clack
BODY:
<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>No Links</title>
</head>
<div>
        Roses are red, violets are blue, golang is the bomb, aint it so true!
</div>
</html>`,
		},
	}

	for _, tst := range tests {
		datastore := &helpers.MockDatastore{}
		datastore.On("FindLink", goodUrl, true).Return(tst.linfo, nil)
		Datastore(datastore)
		origArgs := os.Args
		os.Args = tst.call
		stdout, stderr, estat := executeInSandbox(t)
		stdout = strings.TrimSpace(stdout)
		stderr = strings.TrimSpace(stderr)

		if estat != tst.estat {
			t.Errorf("Estat mismatch for tag %v expected %d, but got %d", tst.tag, tst.estat, estat)
		}

		ok, l, r := compareLongString(tst.stdout, stdout)
		if !ok {
			t.Errorf("Stdout mismatch for tag %v expected difference line:\n%v\ngot difference line:\n%v\n", tst.tag, l, r)
		}

		ok, l, r = compareLongString(tst.stderr, stderr)
		if !ok {
			t.Errorf("Stderr mismatch for tag %v expected difference line:\n%v\ngot difference line:\n%v\n", tst.tag, l, r)
		}

		os.Args = origArgs
	}

}
