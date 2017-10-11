package httpsim

import (
	"compress/gzip"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/url"
)

// Flow describes a flow (e.g. Login flow) that describes the requests to do
type Flow struct {
	// RequiredValues is never to be modified once defined. It defines the required
	// values that are to be given at the start of this flow (with the map[string]string)
	// in the Flow.Execute call.
	RequiredValues []string
	// Values contains all the defined key:values for this flow (be it RequiredValues,
	// or KeysOutput). In Step: KeysInput draws from here and replaces with go templating.
	// KeysOutput puts in here.
	// Rule of thumb is that they're all string except if suffixed by its time e.g. endTime
	Values map[string]interface{}
	// Steps to execute for the flow, in order
	Steps []Step
	// CookieJar is to be left nil if you don't need it, it'll be filled automatically
	CookieJar http.CookieJar
}

// MissingValueError is the error returned when a key value is missing
type MissingValueError struct {
	Prepend      string
	MissingValue string
}

func (e *MissingValueError) Error() string {
	if e.Prepend != "" {
		return e.Prepend + " missing or empty key value: " + e.MissingValue
	}
	return "Missing or empty key value: " + e.MissingValue
}

// NewMVE creates a new MissingValueError from the message
func NewMVE(pre, val string) *MissingValueError {
	return &MissingValueError{
		Prepend:      pre,
		MissingValue: val,
	}
}

// Execute executes a flow
func (f *Flow) Execute(values map[string]interface{}) error {

	// 1. Check that all values are given
	for _, k := range f.RequiredValues {
		if v, ok := values[k]; !ok || v == "" {
			return NewMVE("", k)
		}
	}
	f.Values = values

	// 2. Create cookie jar (mmmm)
	if f.CookieJar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return err
		}
		f.CookieJar = jar
	}

	// 3. Create HTTP client
	cl := http.Client{Jar: f.CookieJar}

	// 4. Go through steps
	for i, step := range f.Steps {

		// Verify all needed values for this step are here
		for _, k := range step.KeysInput {
			if v, ok := f.Values[k]; !ok || v == "" {
				return NewMVE(fmt.Sprintf("Step %d.'%s' failed:", i, step.Name), k)
			}
		}

		// Check that user didn't forget any input values
		if err := step.SanityCheck(i); err != nil {
			return err
		}

		// Replace needed values
		if err := step.ReplaceInBody(f.Values, i); err != nil {
			return err
		}
		if err := step.ReplaceInHeader(f.Values, i); err != nil {
			return err
		}
		if err := step.ReplaceInURL(f.Values, i); err != nil {
			return err
		}

		// Execute request
		resp, err := step.Request.Do(cl)
		if err != nil {
			return err
		}
		// check if gzip
		if resp.Header.Get("Content-Encoding") == "gzip" {
			resp.Body, err = gzip.NewReader(resp.Body)
			if err != nil {
				return err
			}
		}
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		// Store response
		f.Steps[i].Response = &Response{
			Raw:    resp,
			Body:   body,
			Header: resp.Header,
		}

		// Extract important values (KeysOutput)
		for _, extract := range step.KeysOutput {
			n, s, err := extract.Extract(string(body), f.Values)
			if err != nil {
				return fmt.Errorf("Step %d.'%s' failed because couldn't extract '%s': %s",
					i, step.Name, n, err.Error())
			}
			if n == "" {
				return fmt.Errorf("Step %d.'%s' failed because extracted value has no index %s",
					i, step.Name, s)
			}
			f.Values[n] = s
		}

		// Post hook / sanity check
		if step.PostHook != nil {
			if err := step.PostHook(resp.StatusCode, resp.Header, body); err != nil {
				return fmt.Errorf("Step %d.'%s' %s", i, step.Name, err.Error())
			}
		}
	}

	return nil
}

func newBody(v interface{}) interface{} {
	switch t := v.(type) {
	case []byte:
		newBody := make([]byte, len(t))
		copy(newBody, t)
		return newBody
	case string:
		return t
	case url.Values:
		newVals := url.Values{}
		for k, v := range t {
			newVals[k] = []string{v[len(v)-1]}
		}
		return newVals
	case nil:
		return nil
	default:
		panic(fmt.Sprintf("Don't know how to copy %t", t))
	}
}

// CompleteCopy makes a copy of the flow with all new values so that
// the flow may be used concurrently with the condition that you call execute
// with a copied flow. CookieJar is set to nil.
func (f Flow) CompleteCopy() Flow {
	newRequired := make([]string, len(f.RequiredValues))
	copy(newRequired, f.RequiredValues)
	newSteps := make([]Step, len(f.Steps))
	copy(newSteps, f.Steps)

	f.RequiredValues = newRequired
	f.Values = nil
	f.Steps = newSteps
	f.CookieJar = nil

	for i := range f.Steps {
		newHeader := make(http.Header, len(f.Steps[i].Request.Header))
		for k := range f.Steps[i].Request.Header {
			newHeader.Set(k, f.Steps[i].Request.Header.Get(k))
		}
		f.Steps[i].Request.Body = newBody(f.Steps[i].Request.Body)
		f.Steps[i].Request.Header = newHeader
		f.Steps[i].Response = nil

		// Output and input can stay the same, they are read only
		// PostHook is never modified as well in execute as well
	}

	return f
}
