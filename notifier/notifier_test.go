package notifier

import (
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/skroutz/downloader/job"
	"github.com/skroutz/downloader/storage"

	"github.com/go-redis/redis"
)

var (
	Redis    = redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	cbServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	}))
	store  *storage.Storage
	logger = log.New(os.Stderr, "[test-notifier] ", log.Ldate|log.Ltime)
)

func init() {
	err := Redis.FlushDB().Err()
	if err != nil {
		log.Fatal(err)
	}
	store, err = storage.New(Redis)
	if err != nil {
		log.Fatal(err)
	}
}

func TestNotifyJobDeletion(t *testing.T) {
	type cases struct {
		j           *job.Job
		shouldExist bool
	}

	testcases := []cases{
		{&job.Job{
			ID:            "successjob",
			URL:           "http://localhost:12345",
			AggrID:        "notifoo",
			DownloadState: job.StateSuccess,
			CallbackURL:   cbServer.URL}, false},

		{&job.Job{
			ID:            "failjob",
			URL:           "http://localhost:12345",
			AggrID:        "notifoo",
			DownloadState: job.StateSuccess,
			CallbackURL:   "http://localhost:39871/nonexistent"}, true},
	}

	statsID = "jobdeletion"
	notifier, err := New(store, 10, logger, "http://blah.com/")
	if err != nil {
		t.Fatal(err)
	}

	cfg := map[string]interface{}{
		"http": map[string]interface{}{},
		"kafka": map[string]interface{}{
			"bootstrap.servers": "localhost",
		},
	}
	ch := make(chan struct{})
	go notifier.Start(ch, cfg)

	for _, tc := range testcases {
		err := store.QueuePendingCallback(tc.j, 0)
		if err != nil {
			t.Fatal(err)
		}

		exists, err := store.JobExists(tc.j)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("Expected job with id %s to exist in Redis", tc.j.ID)
		}

		time.Sleep(2 * time.Second)

		exists, err = store.JobExists(tc.j)
		if err != nil {
			t.Fatal(err)
		}
		if exists != tc.shouldExist {
			t.Fatalf("Expected job exist to be %v", tc.shouldExist)
		}
	}

	ch <- struct{}{}
	<-ch
}

func TestRogueCollection(t *testing.T) {
	statsID = "rogue"
	notifier, err := New(store, 10, logger, "http://blah.com/")
	if err != nil {
		t.Fatal(err)
	}

	testcases := []struct {
		Job           job.Job
		expectedState job.State
	}{
		{
			job.Job{
				ID:            "RogueOne",
				CallbackState: job.StateInProgress,
			},
			job.StatePending,
		},
		{
			job.Job{
				ID:            "Valid",
				CallbackState: job.StateFailed,
			},
			job.StateFailed,
		},
	}

	for _, tc := range testcases {
		err := store.SaveJob(&tc.Job)
		if err != nil {
			t.Fatal(err)
		}
	}

	cfg := map[string]interface{}{
		"http": map[string]interface{}{},
		"kafka": map[string]interface{}{
			"bootstrap.servers": "localhost",
		},
	}
	//start and close Notifier
	ch := make(chan struct{})
	go notifier.Start(ch, cfg)
	ch <- struct{}{}
	<-ch

	for _, tc := range testcases {
		j, err := store.GetJob(tc.Job.ID)
		if err != nil {
			t.Fatal(err)
		}

		if j.CallbackState != tc.expectedState {
			t.Fatalf("Expected job state %s, found %s", tc.expectedState, j.DownloadState)
		}
	}
}
