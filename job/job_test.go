package job

import (
	"fmt"
	"testing"
)

func TestUnmarshalJSON(t *testing.T) {
	tc := map[string]bool{
		``:              true,
		`{"foo"}`:       true,
		`{"foo":"bar"}`: true,

		// invalid url
		`{"aggr_id":"foo", "url":"foo","callback_url":"http://foo.bar","extra":"whatever"}`: true,
		`{"aggr_id":"foo", "url":"","callback_url":"http://foo.bar","extra":"whatever"}`:    true,

		// invalid cb url
		`{"aggr_id":"foo", "url":"http://foobar.com","callback_url":"fijfij","extra":"whatever"}`: true,

		// invalid aggr_id
		`{"aggr_id":true, "url":"http://foobar.com","callback_url":"http://foo.bar","extra":"whatever"}`: true,
		`{"aggr_id":"", "url":"http://foobar.com","callback_url":"http://foo.bar","extra":"whatever"}`:   true,

		`{"aggr_id":"foo","url":"http://foobar.com","callback_url":"http://foo.bar"}`:                     false,
		`{"aggr_id":"foo", "url":"http://foobar.com","callback_url":"http://foo.bar","extra":"whatever"}`: false,
		`{"aggr_id":"foo","url":"http://foobar.com","callback_url":"http://foo.bar","extra":""}`:          false,

		// user agent
		`{"aggr_id":"useragentfoo", "user_agent":"Downloader Test", "url":"http://foobar.com","callback_url":"http://foo.bar","extra":"whatever"}`: false,
		`{"aggr_id":"useragentfoo", "url":"http://foobar.com","callback_url":"http://foo.bar","extra":"whatever"}`:                                 false,
		`{"aggr_id":"useragentfoo", "user_agent":"", "url":"http://foobar.com","callback_url":"http://foo.bar","extra":"whatever"}`:                false,
		`{"aggr_id":"useragentfoo", "user_agent":null, "url":"http://foobar.com","callback_url":"http://foo.bar","extra":"whatever"}`:              true,
		`{"aggr_id":"useragentfoo", "user_agent":3, "url":"http://foobar.com","callback_url":"http://foo.bar","extra":"whatever"}`:                 true,
	}

	for data, expectErr := range tc {
		j := new(Job)
		err := j.UnmarshalJSON([]byte(data))
		receivedErr := (err != nil)
		if receivedErr != expectErr {
			if err != nil {
				fmt.Println(err)
			}
			t.Errorf("Expected receivedErr to be %v for '%s'", expectErr, data)
		}
	}
}
