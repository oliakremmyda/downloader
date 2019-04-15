package job

import (
	"encoding/json"
	"errors"
	"net/url"
)

// Aggregation is the concept through which the rate limit rules are defined
// and enforced.
type Aggregation struct {
	ID string `json:"aggr_id"`

	// Maximum numbers of concurrent download requests
	Limit int `json:"aggr_limit"`

	// Proxy url for the client to use, optional
	Proxy string `json:"aggr_proxy"`
}

// NewAggregation creates an aggregation with the provided ID and limit.
// If any of the prerequisites fail, an error is returned.
func NewAggregation(id string, limit int, proxy string) (*Aggregation, error) {
	if id == "" {
		return nil, errors.New("Aggregation ID cannot be empty")
	}
	if limit <= 0 {
		return nil, errors.New("Aggregation limit must be greater than 0")
	}

	if _, err := url.ParseRequestURI(proxy); proxy != "" && err != nil {
		return nil, errors.New("Aggregation proxy must be valid")
	}

	return &Aggregation{ID: id, Limit: limit, Proxy: proxy}, nil
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

	a.ID = id
	a.Limit = limit
	a.Proxy = proxy

	return nil
}
