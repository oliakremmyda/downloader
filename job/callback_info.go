package job

import "fmt"

// CallbackInfo holds info to be posted back to the provided callback url.
type CallbackInfo struct {
	Success      bool   `json:"success"`
	Error        string `json:"error"`
	Extra        string `json:"extra"`
	ResourceURL  string `json:"resource_url"`
	DownloadURL  string `json:"download_url"`
	JobID        string `json:"job_id"`
	ResponseCode int    `json:"response_code"`
}

// NewCallbackInfo creates a callback info object
func NewCallbackInfo(success bool, err, extra, resourceURL, downloadURL, jobID string, responseCode int) CallbackInfo {
	return CallbackInfo{
		Success:      success,
		Error:        err,
		Extra:        extra,
		ResourceURL:  resourceURL,
		DownloadURL:  downloadURL,
		JobID:        jobID,
		ResponseCode: responseCode,
	}
}

// String returns a human readable representation of callback info
func (cb CallbackInfo) String() string {
	return fmt.Sprintf("CallbackInfo{ Success: %t, Error: %s, Extra: %s, ResourceURL: %s, DownloadURL: %s, JobID: %s, ResponseCode: %d}",
		cb.Success, cb.Error, cb.Extra, cb.ResourceURL, cb.DownloadURL, cb.JobID, cb.ResponseCode)
}
