package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/skroutz/downloader/backend"
	"github.com/skroutz/downloader/job"
	"github.com/skroutz/downloader/stats"
	"github.com/skroutz/downloader/storage"
)

const (
	maxCallbackRetries       = 2
	deliveryMonitors         = 2
	statsFailedCallbacks     = "failedCallbacks"     //Counter
	statsSuccessfulCallbacks = "successfulCallbacks" //Counter
)

var (
	// expvar.Publish() panics if a name is already registered, hence
	// we need to be able to override it in order to test Notifier easily.
	// TODO: we should probably get rid of expvar to avoid such issues
	statsID = "Notifier"

	// RetryBackoffDuration indicates the time to wait between retries.
	RetryBackoffDuration = 10 * time.Minute
)

// Notifier is the the component responsible for consuming the result of jobs
// and notifying back the respective users by issuing HTTP requests to their
// provided callback URLs.
type Notifier struct {
	Storage     *storage.Storage
	Log         *log.Logger
	DownloadURL *url.URL
	StatsIntvl  time.Duration

	// TODO: These should be exported
	concurrency int
	client      *http.Client
	cbChan      chan job.Job
	stats       *stats.Stats

	// registered backends
	backends map[string]backend.Backend
}

func init() {
	// Indicates we are in test mode
	if _, testMode := os.LookupEnv("DOWNLOADER_TEST_TIME"); testMode {
		RetryBackoffDuration = 200 * time.Millisecond
	}
}

// New takes the concurrency of the notifier as an argument
func New(s *storage.Storage, concurrency int, logger *log.Logger, dwnlURL string) (Notifier, error) {
	url, err := url.ParseRequestURI(dwnlURL)
	if err != nil {
		return Notifier{}, fmt.Errorf("Could not parse Download URL, %v", err)
	}

	if concurrency <= 0 {
		return Notifier{}, errors.New("Notifier Concurrency must be a positive number")
	}

	httpBackend := &backend.HTTPBackend{}
	kafkaBackend := &backend.KafkaBackend{}

	backends := map[string]backend.Backend{
		httpBackend.ID():  httpBackend,
		kafkaBackend.ID(): kafkaBackend,
	}

	n := Notifier{
		Storage:     s,
		Log:         logger,
		StatsIntvl:  5 * time.Second,
		concurrency: concurrency,
		cbChan:      make(chan job.Job),
		DownloadURL: url,
		backends:    backends,
	}

	n.stats = stats.New(statsID, n.StatsIntvl, func(m *expvar.Map) {
		// Store metrics in JSON
		err := n.Storage.SetStats("notifier", m.String(), 2*n.StatsIntvl)
		if err != nil {
			n.Log.Println("Could not report stats", err)
		}
	})

	return n, nil
}

// Start starts the Notifier loop and instruments the worker goroutines that
// perform the actual notify requests.
func (n *Notifier) Start(closeChan chan struct{}, cfg map[string]interface{}) {
	ctx, cancelfunc := context.WithCancel(context.Background())

	for id, backend := range n.backends {
		fmt.Println("Starting", id, "backend")
		backend.Start(ctx, cfg[id].(map[string]interface{}))
	}

	var wg sync.WaitGroup
	wg.Add(n.concurrency)
	for i := 0; i < n.concurrency; i++ {
		go func() {
			defer wg.Done()
			for job := range n.cbChan {
				payload, err := n.Prepare(&job)
				if err != nil {
					n.Log.Printf("Error while preparing payload: %s", err)
				}

				err = n.Notify(&job, payload)
				if err != nil {
					n.Log.Printf("Notify error: %s", err)
				}
			}
		}()
	}

	// Check Redis for jobs left in InProgress state
	n.collectRogueCallbacks()

	go n.stats.Run(ctx)

	// Start monitoring delivery reports
	var deliveriesWaitGroup sync.WaitGroup
	n.MonitorDeliveries(ctx, deliveryMonitors, &deliveriesWaitGroup)

	for {
		select {
		case <-closeChan:
			close(n.cbChan)
			wg.Wait()
			cancelfunc()
			for _, backend := range n.backends {
				fmt.Println("Closing", backend.ID(), "backend")
				backend.Finalize()
			}
			deliveriesWaitGroup.Wait()
			closeChan <- struct{}{}
			return
		default:
			job, err := n.Storage.PopCallback()
			if err != nil {
				switch err {
				case storage.ErrEmptyQueue:
					// noop
				case storage.ErrRetryLater:
					// noop
				default:
					n.Log.Println(err)
				}

				time.Sleep(time.Second)
				continue
			}
			n.cbChan <- job
		}
	}
}

// collectRogueCallbacks Scans Redis for jobs that have InProgress CallbackState.
// This indicates they are leftover from an interrupted previous run and should get requeued.
func (n *Notifier) collectRogueCallbacks() {
	var cursor uint64
	var rogueCount uint64

	for {
		var keys []string
		var err error
		keys, cursor, err = n.Storage.Redis.Scan(cursor, storage.JobKeyPrefix+"*", 50).Result()
		if err != nil {
			n.Log.Println(err)
			break
		}

		for _, jobID := range keys {
			strCmd := n.Storage.Redis.HGet(jobID, "CallbackState")
			if strCmd.Err() != nil {
				n.Log.Println(strCmd.Err())
				continue
			}
			if job.State(strCmd.Val()) == job.StateInProgress {
				jb, err := n.Storage.GetJob(strings.TrimPrefix(jobID, storage.JobKeyPrefix))
				if err != nil {
					n.Log.Printf("Could not get job for Redis: %v", err)
					continue
				}
				err = n.Storage.QueuePendingCallback(&jb, 0)
				if err != nil {
					n.Log.Printf("Could not queue job for download: %v", err)
					continue
				}
				rogueCount++
			}
		}

		if cursor == 0 {
			break
		}
	}

	if rogueCount > 0 {
		n.Log.Printf("Queued %d rogue callbacks", rogueCount)
	}
}

// MonitorDeliveries starts as many go routines as limit instructs in order to
// poll/monitor the delivery reports channel.
func (n *Notifier) MonitorDeliveries(ctx context.Context, limit int, dwg *sync.WaitGroup) {
	dwg.Add(limit)

	for i := 0; i < limit; i++ {
		go n.monitorDeliveriesRoutine(ctx, dwg)
	}
}

// monitorDeliveriesRoutine watches events from the delivery reports channel and
// also checks whether the context has been cancelled or not.
func (n *Notifier) monitorDeliveriesRoutine(ctx context.Context, dwg *sync.WaitGroup) {
	defer dwg.Done()

	for {
		select {
		case <-ctx.Done():
			n.Log.Println("Context has been cancelled")
			return
		case httpInfo := <-n.backends["http"].DeliveryReports():
			n.handleSuccessfullCallback(httpInfo)
			continue
		case kafkaInfo := <-n.backends["kafka"].DeliveryReports():
			n.handleSuccessfullCallback(kafkaInfo)
			continue
		}
	}
}

// handleSuccessfullCallback handles a successful callback info
// by incrementing stats counters and removing jobs from storage
func (n *Notifier) handleSuccessfullCallback(cbInfo job.CallbackInfo) {
	if cbInfo.Success {
		err := n.Storage.QueueJobForDeletion(cbInfo.JobID)
		if err != nil {
			n.Log.Printf("Error: Could not queue job %s for deletion: %s", cbInfo.JobID, err)
		}

		n.stats.Add(statsSuccessfulCallbacks, 1)

		err = n.Storage.RemoveJob(cbInfo.JobID)
		if err != nil {
			n.Log.Printf("Could not remove job %s. Operation returned error: %s", cbInfo.JobID, err)
		}
	}

}

// Prepare runs the necessary actions before calling Notify
// and returns the payload to be sent.
func (n *Notifier) Prepare(j *job.Job) ([]byte, error) {
	j.CallbackCount++

	err := n.markCbInProgress(j)
	if err != nil {
		return nil, err
	}

	cbInfo, err := j.GetCallbackInfo(*n.DownloadURL)
	if err != nil {
		return nil, n.markCbFailed(j, err.Error())
	}

	cbPayload, err := json.Marshal(cbInfo)
	if err != nil {
		return nil, n.markCbFailed(j, err.Error())
	}

	return cbPayload, nil
}

// Notify posts callback info to job's destination by calling Notify
// on each backend.
func (n *Notifier) Notify(j *job.Job, payload []byte) error {
	n.Log.Println("Performing callback action for", j, "...")

	cbType, cbDst := j.GetCallbackTypeAndDestination()

	if cbType == "http" {
		err := n.backends["http"].Notify(cbDst, payload)
		if err != nil {
			return n.retryOrFail(j, err.Error())
		}
	} else {
		err := n.backends["kafka"].Notify(cbDst, payload)
		if err != nil {
			return n.retryOrFail(j, err.Error())
		}
	}

	return nil
}

// retryOrFail checks the callback count of the current download
// and retries the callback if its Retry Counts < maxRetries else it marks
// it as failed
func (n *Notifier) retryOrFail(j *job.Job, err string) error {
	if j.CallbackCount >= maxCallbackRetries {
		return n.markCbFailed(j, err)
	}

	n.Log.Printf("Warn: Callback try no:%d failed for job:%s with: %s", j.CallbackCount, j, err)
	return n.Storage.QueuePendingCallback(j, time.Duration(j.CallbackCount)*RetryBackoffDuration)
}

func (n *Notifier) markCbInProgress(j *job.Job) error {
	j.CallbackState = job.StateInProgress
	j.CallbackMeta = ""
	return n.Storage.SaveJob(j)
}

func (n *Notifier) markCbFailed(j *job.Job, meta ...string) error {
	j.CallbackState = job.StateFailed
	j.CallbackMeta = strings.Join(meta, "\n")
	n.Log.Printf("Error: Callback for %s failed: %s", j, j.CallbackMeta)

	//Report stats
	n.stats.Add(statsFailedCallbacks, 1)
	return n.Storage.SaveJob(j)
}
