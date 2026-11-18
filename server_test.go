package cards_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold"
	cards "github.com/ny4rl4th0t3p/playhold-crazy-eights"
	"github.com/ny4rl4th0t3p/playhold-crazy-eights/pb"
	sdk "github.com/ny4rl4th0t3p/playhold-sdk-go"
	"github.com/ny4rl4th0t3p/playhold/playholdtest"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

// These tests run the game on a real server, through the Go SDK, for what only a server does: the handshake and its
// schema check, refusing a seat, recording, and a game coming back from the store. The game's rules are tested without a
// server, in cards_test.go.

const (
	testTimeout = 5 * time.Second
	testRoom    = "game"
)

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// startServer returns the URL of a server for the game, saving to records if it isn't nil.
func startServer(t *testing.T, records store.Store) string {
	t.Helper()
	url, _ := startServerWith(t, records, "")
	return url
}

// startServerWith is startServer, recording every game in recordings if it isn't empty. It also returns a function that
// stops the server, which writes everything waiting to be written.
func startServerWith(t *testing.T, records store.Store, recordings string) (url string, stop func()) {
	t.Helper()
	rt := playhold.NewRuntime(playhold.Config{
		Logger:  slog.New(slog.DiscardHandler),
		NewSeed: func() [32]byte { return testSeed },
	})
	typ, err := cards.NewRoomTypeWith(cards.Options{Record: recordings != ""})
	require.NoError(t, err)
	off := playhold.RateLimit{Off: true}
	server, err := playhold.NewServer(rt, &playhold.ServerConfig{
		RoomType:         typ,
		Store:            records,
		Recording:        playhold.RecordingConfig{Dir: recordings},
		MemoryLimit:      -1,
		Development:      true, // plain HTTP
		AllowClientCodes: true, // the tests pick their rooms' codes
		ConnectRate:      off,
		GuestRate:        off,
		MessageRate:      off,
		Admission:        playhold.Admission{Off: true},
	})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	var once sync.Once
	stop = func() {
		once.Do(func() {
			server.Close()
			httpServer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			assert.NoError(t, rt.Shutdown(ctx))
		})
	}
	t.Cleanup(stop)
	return "ws" + strings.TrimPrefix(httpServer.URL, "http"), stop
}

// connect says hello as a new guest, as this game's page would.
func connect(t *testing.T, url string) *sdk.Client {
	t.Helper()
	return connectAs(t, url, sdk.Identity{})
}

// connectAs says hello as this guest.
func connectAs(t *testing.T, url string, identity sdk.Identity) *sdk.Client {
	t.Helper()
	c, err := sdk.Connect(testContext(t), &sdk.Options{
		URL: url, Identity: identity, SchemaVersions: cards.ClientSchemaVersions(),
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

// join joins the test room, and returns the views that follow.
func join(t *testing.T, c *sdk.Client) []sdk.ModuleView {
	t.Helper()
	require.NoError(t, c.Join(testContext(t), testRoom))
	return next(t, c)
}

// next waits for the player's next views.
func next(t *testing.T, c *sdk.Client) []sdk.ModuleView {
	t.Helper()
	views, err := c.Next(testContext(t))
	require.NoError(t, err)
	return views
}

// move sends one of the hands module's messages, and reads the views its turn sends back.
func move(t *testing.T, c *sdk.Client, id pb.HandsMessage, msg proto.Message) {
	t.Helper()
	_, err := c.Send(testContext(t), "hands", uint32(id), msg) //nolint:gosec // a small enum
	require.NoError(t, err)
	next(t, c)
}

// hand returns the player's own cards from their views.
func hand(t *testing.T, views []sdk.ModuleView) []string {
	t.Helper()
	for _, view := range views {
		if view.Module == "hands" {
			got := &pb.HandsView{}
			require.NoError(t, proto.Unmarshal(view.View, got))
			return got.GetCards()
		}
	}
	return nil
}

// requireCode checks the error is one the server reported, with this code.
func requireCode(t *testing.T, err error, want sdk.ErrorCode) {
	t.Helper()
	var got *sdk.Error
	require.ErrorAs(t, err, &got)
	assert.Equal(t, want, got.Code)
}

// startServerGame seats two new guests, which starts the game, and returns the first, with the hand they were dealt.
func startServerGame(t *testing.T, url string) (first *sdk.Client, firstHand []string) {
	t.Helper()
	first = connect(t, url)
	join(t, first)
	join(t, connect(t, url))
	return first, hand(t, next(t, first)) // the game has started
}

func TestGameComesBackFromTheStore(t *testing.T) {
	records := memory.New()
	url, stop := startServerWith(t, records, "")
	first, dealt := startServerGame(t, url)
	require.NotEmpty(t, dealt)
	identity := first.Identity()
	stop() // saves every room

	back := connectAs(t, startServer(t, records), identity)

	assert.Equal(t, dealt, hand(t, join(t, back)), "a new server on the same store gives the player back their hand")
}

func TestARecordedGameReplaysTurnForTurn(t *testing.T) {
	recordings := t.TempDir()
	url, stop := startServerWith(t, nil, recordings)
	first, _ := startServerGame(t, url)
	move(t, first, pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{}) // whoever arrived first goes first
	move(t, first, pb.HandsMessage_HANDS_MESSAGE_PASS, &pb.Pass{})
	stop()

	files, err := filepath.Glob(filepath.Join(recordings, "crazy-eights", "*.done.rec"))
	require.NoError(t, err)
	require.Len(t, files, 1, "one room, in the room type's folder, finished: nothing saves it to be restored")
	// #region replay
	typ, err := cards.NewRoomTypeWith(cards.Options{Record: true})
	require.NoError(t, err)

	report := playholdtest.Replay(t, typ, files[0]) // fails the test at the first turn that differs

	assert.Equal(t, 4, report.Turns, "two seats, a draw and a pass")
	assert.True(t, report.SchemaMatches)
	// #endregion replay
}

func TestPageFromBeforeSchemasIsToldToUpdate(t *testing.T) {
	_, err := sdk.Connect(testContext(t), &sdk.Options{URL: startServer(t, nil)}) // no schema versions in its Hello

	requireCode(t, err, sdk.ErrorUpdateRequired)
}

func TestThirdPlayerIsRefused(t *testing.T) {
	url := startServer(t, nil)
	startServerGame(t, url)

	requireCode(t, connect(t, url).Join(testContext(t), testRoom), sdk.ErrorRoomFull)
}
