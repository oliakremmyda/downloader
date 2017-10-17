package main

import (
	"context"
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

type QueueEmptyError string

func (err QueueEmptyError) Error() string {
	return fmt.Sprintf("Queue %s is empty", err)
}

const (
	// Each aggregation has a corresponding Redis Hash named in the form
	// "aggr:<aggregation-id>" and containing various information about
	// the aggregation itself (eg. its limit).
	aggrKeyPrefix = "aggr:"

	// Individual job IDs of an aggregation exist in a Redis List named
	// in the form "jobs:<aggregation-id>".
	jobKeyPrefix = "jobs:"

	callbackQueue = "CallbackQueue"

	maxDownloadRetries = 3
)

// CallbackInfo holds the info to be posted back to the provided callback url of the caller
type CallbackInfo struct {
	Success     bool   `json:"success"`
	Error       string `json:"error"`
	Extra       string `json:"extra"`
	DownloadURL string `json:"download_url"`
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
		case "Extra":
			j.Extra = v
		default:
			return j, fmt.Errorf("Field %s with value %s was not found in Job struct", k, v)
		}
	}
	return j, nil
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
			err = j.SetState(StateFailed, fmt.Sprintf("TLS Error occured, %v", err))
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
		j.SetState(StateFailed, fmt.Sprintf("Received Status Code %d", resp.StatusCode))
		return
	}
	defer resp.Body.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		j.RetryOrFail(fmt.Sprintf("Could not download file, %v", err))
		return
	}

	if err = j.SetState(StateSuccess); err != nil {
		log.Println(err)
		return
	}

	if err = j.QueuePendingCallback(); err != nil {
		log.Println(err)
	}
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

// QueuePendingCallback sets the state of a job to "Pending", saves it and adds it to its aggregation queue
func (j *Job) QueuePendingCallback() error {
	j.CallbackState = StatePending
	err := j.Save()
	if err != nil {
		return err
	}
	return Redis.RPush(callbackQueue, j.ID).Err()
}

// SetState changes the current Job state to the provided value and reports any errors
func (j *Job) SetState(state State, meta ...string) error {
	j.DownloadState = state
	j.Meta = strings.Join(meta, "\n")
	return j.Save()
}

// SetCallbackState changes the current Job state to the provided value and reports any errors
func (j *Job) SetCallbackState(state State, meta ...string) error {
	j.CallbackState = state
	j.Meta = strings.Join(meta, "\n")
	return j.Save()
}

// callbackInfo validates that the job is good for callback and
// return callbackInfo to the caller
func (j *Job) callbackInfo() (CallbackInfo, error) {
	if j.DownloadState != StateSuccess && j.DownloadState != StateFailed {
		return CallbackInfo{}, fmt.Errorf("Invalid Job State %s", j.DownloadState)
	}

	return CallbackInfo{
		Success:     j.DownloadState == StateSuccess,
		Error:       j.Meta,
		Extra:       j.Extra,
		DownloadURL: j.downloadURL(),
	}, nil
}

// downloadURL constructs the actual download URL to be provided to the user.
// TODO: Actually make it smart
func (j *Job) downloadURL() string {
	return fmt.Sprintf("http://localhost/%s", j.ID)
}

// PopCallback attempts to pop a Job from the callback queue.
// If it succeeds the job with the popped ID is returned.
func PopCallback() (Job, error) {
	cmd := Redis.LPop(callbackQueue)
	if err := cmd.Err(); err != nil {
		if cmd.Err().Error() != "redis: nil" {
			return Job{}, fmt.Errorf("Could not pop from redis queue: %s", cmd.Err().Error())
		}
		return Job{}, QueueEmptyError(callbackQueue)
	}

	return GetJob(cmd.Val())
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
		return Job{}, QueueEmptyError(aggr.RedisJobsKey())
	}

	return GetJob(cmd.Val())
}
