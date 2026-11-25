package obs_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"classrec/internal/obs"
	"classrec/internal/obs/obstest"
)

const password = "test-pass"

func connect(t *testing.T, fake *obstest.Server, pass string) (*obs.Client, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return obs.Connect(ctx, fake.URL(), pass)
}

func newFake(t *testing.T) *obstest.Server {
	fake := obstest.New(password)
	t.Cleanup(fake.Close)
	return fake
}

func TestRecordStartStop(t *testing.T) {
	fake := newFake(t)
	client, err := connect(t, fake, password)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()

	if err := client.StartRecord(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := client.RecordStatus(ctx)
	if err != nil || !status.Active {
		t.Fatalf("status %+v, err %v", status, err)
	}
	path, err := client.StopRecord(ctx)
	if err != nil || path == "" {
		t.Fatalf("path %q, err %v", path, err)
	}
	if fake.Recording() {
		t.Fatal("fake still recording")
	}
	want := []string{"StartRecord", "GetRecordStatus", "StopRecord"}
	if got := fake.Requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests %v, want %v", got, want)
	}
}

func TestStartWhileRecordingIsARequestError(t *testing.T) {
	fake := newFake(t)
	client, _ := connect(t, fake, password)
	defer client.Close()
	client.StartRecord(context.Background())

	err := client.StartRecord(context.Background())
	var requestErr *obs.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != 500 {
		t.Fatalf("got %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	fake := newFake(t)
	if _, err := connect(t, fake, "wrong"); !errors.Is(err, obs.ErrAuthFailed) {
		t.Fatalf("got %v, want ErrAuthFailed", err)
	}
}

func TestNoAuthWhenOBSHasItOff(t *testing.T) {
	fake := obstest.New("")
	defer fake.Close()
	client, err := connect(t, fake, "anything")
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
}

func TestStats(t *testing.T) {
	fake := newFake(t)
	fake.SetSkippedFrames(3, 7)
	client, _ := connect(t, fake, password)
	defer client.Close()

	stats, err := client.Stats(context.Background())
	if err != nil || stats.RenderSkippedFrames != 3 || stats.OutputSkippedFrames != 7 || stats.ActiveFPS != 15 {
		t.Fatalf("stats %+v, err %v", stats, err)
	}
}

func TestInputLevel(t *testing.T) {
	fake := newFake(t)
	client, _ := connect(t, fake, password)
	defer client.Close()

	if _, ok := client.InputLevel("Edge audio"); ok {
		t.Fatal("level before any event")
	}
	fake.SendLevel("Edge audio", 0.01) // -40 dB
	level := waitForLevel(t, client, "Edge audio")
	if math.Abs(level.DB-(-40)) > 1e-9 {
		t.Fatalf("got %v dB", level.DB)
	}
}

func TestOBSCrashEndsClientAndFailsRequests(t *testing.T) {
	fake := newFake(t)
	client, _ := connect(t, fake, password)
	defer client.Close()

	fake.DropConnections()
	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client did not notice the dropped connection")
	}
	if client.Err() == nil {
		t.Fatal("Err is nil after the connection dropped")
	}
	if err := client.StartRecord(context.Background()); err == nil {
		t.Fatal("request on a dead connection succeeded")
	}
}

func TestReconnectAfterCrashSeesRecordingStillRunning(t *testing.T) {
	fake := newFake(t)
	first, _ := connect(t, fake, password)
	first.StartRecord(context.Background())
	fake.DropConnections()
	<-first.Done()

	second, err := connect(t, fake, password)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	status, err := second.RecordStatus(context.Background())
	if err != nil || !status.Active {
		t.Fatalf("status %+v, err %v", status, err)
	}
}

func waitForLevel(t *testing.T, client *obs.Client, input string) obs.InputLevel {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if level, ok := client.InputLevel(input); ok {
			return level
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no level for %q", input)
	return obs.InputLevel{}
}
