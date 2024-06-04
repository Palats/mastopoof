// This file contains the implementation of the `testserve`
// CLI command. This starts a Mastopoof server with a fake Mastodon server.
package cmds

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Palats/mastopoof/backend/mastodon/testserver"
	"github.com/c-bata/go-prompt"
	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

var spaces = regexp.MustCompile(`\s+`)

type OpDesc struct {
	Text        string
	Description string
	Op          OpFunc
}

type OpFunc func(args []string) error

type TestServe struct {
	mux      *http.ServeMux
	port     int
	testData fs.FS

	mastodonServer *testserver.Server
	suggest        []prompt.Suggest
	ops            map[string]*OpDesc
}

func NewTestServe(mux *http.ServeMux, port int, testData fs.FS) *TestServe {
	s := &TestServe{
		mux:      mux,
		port:     port,
		testData: testData,

		ops: map[string]*OpDesc{},
	}

	ops := []*OpDesc{
		{Text: "fake-statuses", Op: s.opFakeStatus, Description: "Add fake statuses; opt: number of statuses"},
		{Text: "fake-notifications", Op: s.opFakeNotifications, Description: "Add notifications statuses; opt: number of notifications"},
		{Text: "clear-notifications", Op: s.opClearNotifications, Description: "Clear all notifications"},
		{Text: "set-list-delay", Op: s.opSetListDelay, Description: "Introduce delay when listing statuses from Mastodon"},
		{Text: "set-status-favourite", Op: s.opSetStatusFavourite, Description: "Mark the status (by ID) as favourite"},
		{Text: "set-status-unfavourite", Op: s.opSetStatusUnfavourite, Description: "Remove favourite from the status (by ID)"},
		{Text: "exit", Op: s.opExit, Description: "Shutdown"},
	}
	for _, op := range ops {
		s.suggest = append(s.suggest, prompt.Suggest{Text: op.Text, Description: op.Description})
		s.ops[op.Text] = op
	}
	return s
}

func (s *TestServe) Run(ctx context.Context) error {
	s.mastodonServer = testserver.New()
	if err := s.mastodonServer.AddJSONStatuses(s.testData); err != nil {
		return err
	}
	s.mastodonServer.RegisterOn(s.mux)

	addr := fmt.Sprintf(":%d", s.port)
	fmt.Printf("Listening on %s...\n", addr)
	go func() {
		err := http.ListenAndServe(addr, h2c.NewHandler(s.mux, &http2.Server{}))
		glog.Exit(err)
	}()

	p := prompt.New(
		s.executor,
		s.completer,
		prompt.OptionPrefix(">>> "),
		prompt.OptionAddKeyBind(prompt.KeyBind{
			Key: prompt.ControlC,
			Fn:  func(b *prompt.Buffer) { os.Exit(0) },
		}),
	)

	fmt.Println()
	fmt.Println("<tab> to see command list")
	p.Run()
	return nil
}

func (s *TestServe) completer(d prompt.Document) []prompt.Suggest {
	return prompt.FilterHasPrefix(s.suggest, d.GetWordBeforeCursor(), true)
}

func (s *TestServe) executor(text string) {
	glog.Infof("prompt input: %v", text)
	var cmds [][]string
	// Support multiple commands separated by semi-colon.
	for _, sub := range strings.Split(text, ";") {
		// Remove comments.
		if idx := strings.Index(sub, "#"); idx >= 0 {
			sub = sub[:idx]
		}
		var words []string
		for _, w := range spaces.Split(sub, -1) {
			if w != "" {
				words = append(words, w)
			}
		}
		if len(words) > 0 {
			cmds = append(cmds, words)
		}
	}

	for _, words := range cmds {
		op := words[0]
		args := words[1:]

		opErr := fmt.Errorf("unknown command %q", op)
		if desc := s.ops[op]; desc != nil {
			opErr = desc.Op(args)
		}
		if opErr != nil {
			fmt.Fprintf(os.Stderr, "failed: %v\n", opErr)
		}
	}
}

func (s *TestServe) opFakeStatus(args []string) error {
	if len(args) > 1 {
		return errors.New("at most one parameter allowed")
	}
	var err error
	count := int64(10)
	if len(args) > 0 {
		count, err = strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("unable to parse %s: %w", args[0], err)
		}
	}
	for i := int64(0); i < count; i++ {
		s.mastodonServer.AddFakeStatus()
	}
	fmt.Printf("Added %d fake statuses.\n", count)
	return nil
}

func (s *TestServe) opFakeNotifications(args []string) error {
	if len(args) > 1 {
		return errors.New("at most one parameter allowed")
	}
	count := int64(10)
	var err error
	if len(args) > 0 {
		count, err = strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("unable to parse %s: %w", args[0], err)
		}
	}
	for i := int64(0); i < count; i++ {
		s.mastodonServer.AddFakeNotification()
	}
	fmt.Printf("Added %d fake notifications.\n", count)
	return nil
}

func (s *TestServe) opClearNotifications(args []string) error {
	if len(args) > 0 {
		return errors.New("no parameters allowed")
	}
	s.mastodonServer.ClearNotifications()
	return nil
}

func (s *TestServe) opSetListDelay(args []string) error {
	if len(args) != 1 {
		return errors.New("one parameter needed to specify the delay, as Go ParseDuration format (e.g., '3s')")
	}
	d, err := time.ParseDuration(args[0])
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	s.mastodonServer.SetListDelay(d)
	return nil
}

func (s *TestServe) opExit(args []string) error {
	if len(args) > 0 {
		return errors.New("no parameters allowed")
	}
	glog.Exit(0)
	return nil
}

func (s *TestServe) opSetStatusFavourite(args []string) error {
	if len(args) != 1 {
		return errors.New("status ID required")
	}
	return s.mastodonServer.SetStatusFavourite(mastodon.ID(args[0]))
}

func (s *TestServe) opSetStatusUnfavourite(args []string) error {
	if len(args) != 1 {
		return errors.New("status ID required")
	}
	return s.mastodonServer.SetStatusUnfavourite(mastodon.ID(args[0]))
}
