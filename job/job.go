package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/skroutz/downloader/processor/mimetype"
)

// State represents the download & callback states.
// For valid values see constants below.
type State string

// The available states of a job's DownloadState/CallbackState.
const (
	StatePending    = "Pending"
	StateFailed     = "Failed"
	StateSuccess    = "Success"
	StateInProgress = "InProgress"
)

// Job represents a user request for downloading a resource.
//
// It is the core entity of the downloader and holds all info and state of
// the download.
//
// TODO: should this be valid with an empty aggregation id?
type Job struct {
	// Auto-generated
	ID string `json:"-"`

	// The URL pointing to the resource to be downloaded
	URL string `json:"url"`

	// AggrID is the ID of the aggregation the job belongs to.
	//
	// TODO: should this be a pointer to an Aggregation? If so, then
	// NewJob should be a function on Aggregation
	AggrID string `json:"aggr_id"`

	DownloadState State `json:"-"`

	// How many times the download request was attempted
	DownloadCount int `json:"-"`

	// Auxiliary ad-hoc information. Typically used for communicating
	// download errors back to the user.
	DownloadMeta string `json:"-"`

	CallbackState State  `json:"-"`
	CallbackType  string `json:"callback_type"`
	CallbackDst   string `json:"callback_dst"`
	CallbackURL   string `json:"callback_url"`

	// Auxiliary ad-hoc information used for debugging.
	CallbackMeta string `json:"-"`

	// How many times the callback request was attempted
	CallbackCount int `json:"-"`

	// Arbitrary info provided by the user that are posted
	// back during the callback
	Extra string `json:"extra"`

	// Response code of the download request
	ResponseCode int `json:"response_code"`

	// Mime type pattern provided by the client
	MimeType string `json:"mime_type"`
}

// MarshalBinary is used by redis driver to marshall custom type State
func (s State) MarshalBinary() (data []byte, err error) {
	return []byte(string(s)), nil
}

// Path returns the relative job path
func (j *Job) Path() string {
	return path.Join(string(j.ID[0:3]), j.ID)
}

// UnmarshalJSON is used to populate a job from the values in
// the provided JSON message.
func (j *Job) UnmarshalJSON(b []byte) error {
	var tmp map[string]interface{}

	err := json.Unmarshal(b, &tmp)
	if err != nil {
		return err
	}

	dlURL, ok := tmp["url"].(string)
	if !ok {
		return errors.New("URL must be a string")
	}
	_, err = url.ParseRequestURI(dlURL)
	if err != nil {
		return errors.New("Could not parse URL: " + err.Error())
	}
	j.URL = dlURL

	aggrID, ok := tmp["aggr_id"].(string)
	if !ok {
		return errors.New("aggr_id must be a string")
	}
	if aggrID == "" {
		return errors.New("aggr_id cannot be empty")
	}
	j.AggrID = aggrID

	cbURL, ok := tmp["callback_url"].(string)
	if ok {
		_, err = url.ParseRequestURI(cbURL)
		if err != nil {
			return errors.New("Could not parse callback URL: " + err.Error())
		}
		j.CallbackURL = cbURL
	}

	// Check if callback_type and callback_dst are present
	// only if callback_url is empty
	if j.CallbackURL == "" {
		cbType, ok := tmp["callback_type"].(string)
		if ok && cbType == "" {
			return errors.New("Since callback_url is absent, callback_type cannot be empty")
		}

		if !ok {
			return errors.New("callback_type must be a string")
		}

		j.CallbackType = cbType

		cbDestination, ok := tmp["callback_dst"].(string)
		if ok && cbDestination == "" {
			return errors.New("Since callback_url is absent, callback_dst cannot be empty")
		}

		if !ok {
			return errors.New("callback_dst must be a string")
		}

		if strings.HasPrefix(cbDestination, "http") {
			_, err = url.ParseRequestURI(cbDestination)
			if err != nil {
				return errors.New("Could not parse URL: " + err.Error())
			}
		}
		j.CallbackDst = cbDestination
	}

	// Do a final check for the callback information
	if j.CallbackURL == "" && j.CallbackType == "" && j.CallbackDst == "" {
		return errors.New("You need to provide either callback_url OR callback_type and callback_dst")
	}

	extra, ok := tmp["extra"].(string)
	if ok {
		j.Extra = extra
	}

	m, ok := tmp["mime_type"]
	if ok {
		// Since mime_type is optional, if it is not in the json doc
		// set it to the default ""
		if mime, ok := m.(string); ok {
			j.MimeType = mime
			err = mimetype.ValidateMimeTypePattern(mime)
		} else {
			err = errors.New("MimeType pattern must be a string")
		}
		return err
	}
	j.MimeType = ""

	return nil
}

// GetCallbackInfo validates the state of a job and returns a callback info
// along with an error if appropriate
func (j *Job) GetCallbackInfo(downloadURL url.URL) (CallbackInfo, error) {
	if j.DownloadState != StateSuccess && j.DownloadState != StateFailed {
		return CallbackInfo{}, fmt.Errorf("Invalid job download state: '%s'", j.DownloadState)
	}

	downloadURL.Path = path.Join(downloadURL.Path, j.Path())
	return NewCallbackInfo(j.DownloadState == StateSuccess, j.DownloadMeta, j.Extra,
		j.URL, downloadURL.String(), j.ID, j.ResponseCode), nil
}

// GetCallbackTypeAndDestination returns callback type and destination from either
// the job's callback_url or callback_type and callback_dst.
// When callback_url is present then as callback type the "http" is returned.
func (j *Job) GetCallbackTypeAndDestination() (string, string) {
	if j.CallbackURL != "" {
		return "http", j.CallbackURL
	}

	return j.CallbackType, j.CallbackDst
}

func (j Job) String() string {
	return fmt.Sprintf("Job{ID:%s, Aggr:%s, URL:%s, callback_url:%s}",
		j.ID, j.AggrID, j.URL, j.CallbackURL)
}
