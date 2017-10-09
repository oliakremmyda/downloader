package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-redis/redis"
)

// State represents the download & callback states.
// For valid values see constants below.
type State string

const (
	// Valid State values
	StatePending    = "Pending"
	StateFailed     = "Failed"
	StateSuccess    = "Success"
	StateInProgress = "InProgress"

	aggrKeyPrefix = "aggr:"
	jobKeyPrefix  = "jobs:"

	maxRetries = 3
)

// Job represents a user request for downloading a resource.
//
// It is the core entity of the downloader and holds all info and state of
// the download.
type Job struct {
	// Auto-generated
	ID string `json:"id"`

	// The URL pointing to the resource to be downloaded
	URL string `json:"url"`

	// AggrID is the ID of the aggregation the job belongs to.
	AggrID string `json:"aggr_id"`

	DownloadState State `json:"-"`

	// RetryCount is how many times the download was attempted.
	RetryCount int `json:"-"`

	// Auxiliary ad-hoc information. Typically used for communicating
	// errors back to the user.
	Meta string `json:"-"`

	CallbackURL   string `json:"callback_url"`
	CallbackCount int    `json:"-"`
	CallbackState State  `json:"-"`
}

// Aggregation is the concept through which the rate limit rules are defined
// and enforced.
type Aggregation struct {
	ID string

	// Maximum numbers of concurrent download requests
	Limit int
}

// MarshalBinary is used by redis driver to marshall custom type State
func (s State) MarshalBinary() (data []byte, err error) {
	return []byte(string(s)), nil
}

// Redis must be initialized before use
var Redis *redis.Client

// InitStorage initializes the Redis client singleton to be used by the
// storage class.
func InitStorage(host string, port int) error {
	Redis = redis.NewClient(&redis.Options{Addr: strings.Join([]string{host, strconv.Itoa(port)}, ":")})
	if ping := Redis.Ping(); ping.Err() != nil || ping.Val() != "PONG" {
		if ping.Err() != nil {
			return fmt.Errorf("Could not ping Redis Server successfully: %v", ping.Err())
		}
		return fmt.Errorf("Could not ping Redis Server successfully: Expected PONG, received %s", ping.Val())
	}
	return nil
}

// Save updates or creates j in Redis.
func (j *Job) Save() error {
	m, err := j.toMap()
	if err != nil {
		return err
	}
	scmd := Redis.HMSet(j.ID, m)
	_, err = scmd.Result()
	return err
}

// GetJob fetches the Job with the given id from Redis.
func GetJob(id string) (Job, error) {
	cmd := Redis.HGetAll(id)
	return jobFromMap(cmd.Val())
}

// Perform downloads the resource denoted by j.URL and updates its state in
// Redis accordingly. It may retry downloading on certain errors.
func (j *Job) Perform(ctx context.Context, saveDir string) {
	j.SetState(StateInProgress)
	out, err := os.Create(saveDir + j.ID)
	if err != nil {
		j.RetryOrFail(fmt.Sprintf("Could not write to file, %v", err))
		return
	}
	defer out.Close()

	req, err := http.NewRequest("GET", j.URL, nil)
	if err != nil {
		j.RetryOrFail(fmt.Sprintf("Could not create request, %v", err))
		return
	}

	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		if strings.Contains(err.Error(), "x509") || strings.Contains(err.Error(), "tls") {
			err = j.SetStateWithMeta(StateFailed, fmt.Sprintf("TLS Error occured, %v", err))
			if err != nil {
				log.Println(err)
			}
			return
		}

		j.RetryOrFail(err.Error())
		return
	}

	if resp.StatusCode >= http.StatusInternalServerError {
		j.RetryOrFail(fmt.Sprintf("Received status code %s", resp.Status))
		return
	} else if resp.StatusCode >= http.StatusBadRequest && resp.StatusCode < http.StatusInternalServerError {
		j.SetStateWithMeta(StateFailed, fmt.Sprintf("Received Status Code %d", resp.StatusCode))
		return
	}
	defer resp.Body.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		j.RetryOrFail(fmt.Sprintf("Could not download file, %v", err))
		return
	}
	j.SetState(StateSuccess)
}

// Exists checks if a job exists in Redis
func (j *Job) Exists() (bool, error) {
	res, err := Redis.Exists(j.ID).Result()

	if err != nil {
		return false, err
	}

	return res > 0, nil
}

// QueuePendingDownload sets the state of a job to "Pending", saves it and adds it to its aggregation queue
func (j *Job) QueuePendingDownload() error {
	j.DownloadState = StatePending
	err := j.Save()
	if err != nil {
		return err
	}
	intcmd := Redis.RPush(jobKeyPrefix+j.AggrID, j.ID)
	return intcmd.Err()
}

// SetStateWithMeta changes the current Job state to the provided value, updates the Meta field and reports any errors
func (j *Job) SetStateWithMeta(state State, meta string) error {
	j.DownloadState = state
	j.Meta = meta
	return j.Save()
}

// SetState changes the current Job state to the provided value and reports any errors
func (j *Job) SetState(state State) error {
	j.DownloadState = state
	return j.Save()
}

// RetryOrFail checks the retry count of the current download
// and retries the job if its RetryCount < maxRetries else it marks
// it as failed
func (j *Job) RetryOrFail(err string) error {
	if j.RetryCount < maxRetries {
		j.RetryCount++
		return j.QueuePendingDownload()
	}
	return j.SetStateWithMeta(StateFailed, err)
}

func (j *Job) toMap() (map[string]interface{}, error) {
	out := make(map[string]interface{})

	v := reflect.ValueOf(j)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	// we only accept structs
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("ToMap only accepts structs; got %T", v)
	}

	typ := v.Type()
	for i := 0; i < v.NumField(); i++ {
		// gets us a StructField
		fi := typ.Field(i)
		// set key of map to value in struct field
		out[fi.Name] = v.Field(i).Interface()
	}
	return out, nil
}

func jobFromMap(m map[string]string) (Job, error) {
	var err error
	j := Job{}
	for k, v := range m {
		switch k {
		case "ID":
			j.ID = v
		case "URL":
			j.URL = v
		case "AggrID":
			j.AggrID = v
		case "DownloadState":
			j.DownloadState = State(v)
		case "RetryCount":
			j.RetryCount, err = strconv.Atoi(v)
			if err != nil {
				return j, fmt.Errorf("Could not decode struct from map: %v", err)
			}
		case "Meta":
			j.Meta = v
		case "CallbackURL":
			j.CallbackURL = v
		case "CallbackCount":
			j.CallbackCount, err = strconv.Atoi(v)
			if err != nil {
				return j, fmt.Errorf("Could not decode struct from map: %v", err)
			}
		case "CallbackState":
			j.CallbackState = State(v)
		default:
			return j, fmt.Errorf("Field %s with value %s was not found in Job struct", k, v)
		}
	}
	return j, nil
}

// GetAggregation fetches an aggregation from the Redis and returns it
func GetAggregation(id string) (Aggregation, error) {
	aggr := Aggregation{ID: id, Limit: 0}

	cmd := Redis.HGet(aggr.RedisKey(), "Limit")
	err := cmd.Err()
	if err != nil {
		return Aggregation{}, err
	}

	maxConns, err := strconv.Atoi(cmd.Val())
	if err != nil {
		return Aggregation{}, err
	}
	aggr.Limit = maxConns

	return aggr, nil
}

// Return the Redis key
func (aggr *Aggregation) RedisKey() string {
	return aggrKeyPrefix + aggr.ID
}

// Return the Redis job list key
func (aggr *Aggregation) RedisJobsKey() string {
	return jobKeyPrefix + aggr.ID
}

// Save updates/ creates the current aggregation in redis.
func (aggr *Aggregation) Save() error {
	cmd := Redis.HSet(aggr.RedisKey(), "Limit", aggr.Limit)
	return cmd.Err()
}

// Remove deletes the aggregation key from Redis
// It does not remove the jobs list for the aggregation
// since we never want to lose track of already queued jobs
func (aggr *Aggregation) Remove() error {
	return Redis.Del(aggr.RedisKey()).Err()
}

// Exists checks if the given aggregation exists in the Redis
func (aggr *Aggregation) Exists() (bool, error) {
	res, err := Redis.Exists(aggr.RedisKey()).Result()

	if err != nil {
		return false, err
	}

	return res > 0, nil
}

// PopJob attempts to pop a Job for that aggregation.
// If it succeeds the job with the popped ID is returned.
func (aggr *Aggregation) PopJob() (Job, error) {
	cmd := Redis.LPop(aggr.RedisJobsKey())
	if err := cmd.Err(); err != nil {
		if cmd.Err().Error() != "redis: nil" {
			return Job{}, fmt.Errorf("Could not pop from redis queue: %s", cmd.Err().Error())
		}
		//No jobs popped
		return Job{}, errors.New("Queue is empty")
	}

	return GetJob(cmd.Val())
}
