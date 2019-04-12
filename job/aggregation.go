package job

import (
	"encoding/json"
	"errors"
	"net/url"
)

// Timeout for http client that will handle the download, in seconds
var defaultTimeout = 10

// Aggregation is the concept through which the rate limit rules are defined
// and enforced.
type Aggregation struct {
	ID string `json:"aggr_id"`

	// Maximum numbers of concurrent download requests
	Limit int `json:"aggr_limit"`

	// Proxy url for the client to use, optional
	Proxy string `json:"aggr_proxy"`

	// Client timeout in seconds
	Timeout int `json:"aggr_timeout"`
}

// NewAggregation creates an aggregation with the provided ID and limit.
// If any of the prerequisites fail, an error is returned.
func NewAggregation(id string, limit int, proxy string, timeout int) (*Aggregation, error) {
	if id == "" {
		return nil, errors.New("Aggregation ID cannot be empty")
	}
	if limit <= 0 {
		return nil, errors.New("Aggregation limit must be greater than 0")
	}

	if _, err := url.ParseRequestURI(proxy); proxy != "" && err != nil {
		return nil, errors.New("Aggregation proxy must be valid")
	}

	if timeout <= 0 {
		return nil, errors.New("Aggregation timeout must be greater than 0")
	}

	return &Aggregation{ID: id, Limit: limit, Proxy: proxy, Timeout: timeout}, nil
}

// UnmarshalJSON populates the aggregation with the values in the provided JSON.
func (a *Aggregation) UnmarshalJSON(b []byte) error {
	var tmp map[string]interface{}

	err := json.Unmarshal(b, &tmp)
	if err != nil {
		return err
	}

	id, ok := tmp["aggr_id"].(string)
	if !ok {
		return errors.New("Aggregation ID must be a string")
	}
	if id == "" {
		return errors.New("Aggregation ID cannot be empty")
	}

	limitf, ok := tmp["aggr_limit"].(float64)
	if !ok {
		return errors.New("Aggregation limit must be a number")
	}

	limit := int(limitf)
	if limit <= 0 {
		return errors.New("Aggregation limit must be greater than 0")
	}

	proxy, ok := tmp["aggr_proxy"].(string)
	if !ok {
		proxy = ""
	} else {
		if _, err := url.ParseRequestURI(proxy); proxy != "" && err != nil {
			return errors.New("Aggregation proxy must be valid")
		}
	}

	var timeout int
	timeoutS, ok := tmp["aggr_timeout"]
	if !ok {
		timeout = defaultTimeout // The attribute is missing. Use the default value
	} else {
		timeoutf, ok := timeoutS.(float64) // The attribute is given, validate it.
		if !ok {
			return errors.New("Aggregation timeout must be a number")
		}
		timeout := int(timeoutf)
		if timeout <= 0 {
			return errors.New("Aggregation timeout must be greater than 0")
		}
	}

	a.ID = id
	a.Limit = limit
	a.Proxy = proxy
	a.Timeout = timeout

	return nil
}
