package httpsim

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	"net/url"

	"regexp"

	"github.com/gee-m/go-helpers/gstrings"
)

// Extracter is the interface that is used to extract potentially important Values
// out of a body (to be sent with later requests e.g. csrf tokens).
// The values are also passed if you need previous outputs from the requests
// to calculate your new value
type Extracter interface {
	Extract(body string, values map[string]interface{}) (name, value string, err error)
}

// Extractable represents a string that's extractable given the response body
// It implements the Extracter interface, and is a default you can use.
// Extract() extracts the first string betwen afterthis and beforethis
type Extractable struct {
	AfterThis  string
	BeforeThis string
	// Name is the name of the string to be extracted
	Name string

	// Custom parameters for smarter extraction, will keep trying until
	// parameters' condition are met if iterate is true

	Iterate bool
	// MaxLength means return an error when the value is longer than max length
	// -1 means no max length
	MaxLength int
	// MinLength means return an error when the value is smaller than min length
	// -1 means no min length
	MinLength int
	// MatchRegexp means to return an error if it doesn't match the regex
	// empty means no checking regex
	MatchRegexp string
	// IgnoreNotFound set to true if you want to ignore errors when not found
	IgnoreNotFound bool

	// Again reruns the Extract() (recursively) with the extracted content from the parent
	Again *Extractable
}

func stringBetweenN(body, bef, aft string, occ int) (found bool, str string) {
	if occ == 0 {
		return gstrings.StringBetween(body, bef, aft)
	}
	befIndex := strings.Index(body, bef)
	if befIndex == -1 {
		return false, ""
	}
	return stringBetweenN(body[befIndex+len(bef)+1:], bef, aft, occ-1)
}

// Extract extracts the string between the extractable delimiters
func (e Extractable) Extract(body string, v map[string]interface{}) (string, string, error) {
	for i := 0; ; i++ {
		found, bet := stringBetweenN(body, e.AfterThis, e.BeforeThis, i)
		if !found {
			if e.IgnoreNotFound {
				return e.Name, "", nil
			}
			return e.Name, "", errors.New("not found")
		}

		var err error
		// Check conditions
		if e.MaxLength != -1 && len(bet) > e.MaxLength {
			err = fmt.Errorf("max length of %d reached: %s", e.MaxLength, bet)
		} else if e.MinLength != -1 && len(bet) < e.MinLength {
			err = fmt.Errorf("min length of %d reached: %s", e.MinLength, bet)
		} else if e.MatchRegexp != "" {
			if e.MatchRegexp[0] != '^' {
				e.MatchRegexp = "^" + e.MatchRegexp
			}
			if e.MatchRegexp[len(e.MatchRegexp)-1] != '$' {
				e.MatchRegexp += "$"
			}
			mat, err2 := regexp.Match(e.MatchRegexp, []byte(bet))
			if err != nil {
				err = err2
			} else if !mat {
				err = fmt.Errorf("regex '%s' not matched: %s", e.MatchRegexp, bet)
			}
		}

		if err != nil {
			if e.Iterate {
				continue
			} else {
				if e.IgnoreNotFound {
					return e.Name, "", nil
				}
				return e.Name, "", err
			}
		} else {
			if e.Again != nil {
				return e.Again.Extract(bet, v)
			}
			return e.Name, bet, nil
		}
	}
}

// Request defines an http request to be executed
type Request struct {
	URL    string
	Method string
	Header http.Header
	// Body is an interface, right now the following types are supported:
	// string, []byte, url.Values
	Body interface{}
	// IgnoreRedirects is whether the redirects should be ignored (302)
	IgnoreRedirects bool
}

// Response is a respones to the http request. If body is filled, raw.Body
// may not be.
type Response struct {
	Raw    *http.Response
	Body   []byte
	Header http.Header
}

// Step is an http request to be executed when needed
type Step struct {
	// Name is for debugging purposes
	Name string

	// Request to be made during this step
	Request Request
	// Response is filled automatically as the steps are executed, and is the
	// response to the above request
	Response *Response

	// KeysInput are the inputs that are to be replaced in body before
	// the request is executed. Can ask for Flow.Values as wee as any previous
	// step's output
	KeysInput []string
	// KeysOutput is the keys that are extracted from this body and put in
	// a map[string]string to be used for later steps (as KeysInput).
	// The Ouputs are extracted in the order given, and put in the Flow.values.
	KeysOutput []Extracter

	// PostHook is mostly used as a sanity check, and thus should fail if
	// something went wrong during this step. It can also let you store special
	// values from this step if you wish to do so. (closure)
	PostHook func(statusCode int, header http.Header, body []byte) error
}

func countBody(v interface{}, c string) int {
	switch t := v.(type) {
	case []byte:
		return strings.Count(string(t), c)
	case string:
		return strings.Count(t, c)
	case url.Values:
		var total int
		for k, v := range t {
			total += strings.Count(k, c)
			total += strings.Count(v[len(v)-1], c)
		}
		return total
	case nil:
		return 0
	default:
		panic(fmt.Sprintf("Don't know how to handle the type %t", t))
	}
}

// Do executes the http step with the client
func (r *Request) Do(cl http.Client) (*http.Response, error) {
	var bod []byte
	if r.Body != nil {
		bod = r.Body.([]byte)
	}
	req, err := http.NewRequest(r.Method, r.URL, bytes.NewReader(bod))
	if err != nil {
		return nil, err
	}
	req.Header = r.Header
	if r.IgnoreRedirects {
		cl.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return cl.Do(req)
}

// SanityCheck performs simple sanity checks on the step
func (s *Step) SanityCheck(stepNb int) error {
	if countBody(s.Request.Body, "{{")+strings.Count(
		fmt.Sprintf("%v", s.Request.Header)+s.Request.URL, "{{") < len(s.KeysInput) {
		return fmt.Errorf("Step %d.'%s' request appears to not contain enough replacements",
			stepNb, s.Name)
	}
	return nil
}

func replaceInBytes(vals map[string]interface{}, bod []byte) ([]byte, error) {
	tpl, err := template.New("replacement").Parse(string(bod))
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	if err := tpl.Execute(&buffer, vals); err != nil {
		return nil, err
	}
	output := buffer.String()
	if output == string(bod) {
		return nil, fmt.Errorf("didn't replace anything in request, but should have")
	}

	return []byte(output), nil
}

func replaceInString(vals map[string]interface{}, str string) (string, error) {
	tpl, err := template.New("replacement").Parse(str)
	if err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	if err := tpl.Execute(&buffer, vals); err != nil {
		return "", err
	}

	return buffer.String(), nil
}

// ReplaceInBody replaces the KeysInput in the request body
func (s *Step) ReplaceInBody(vals map[string]interface{}, stepNb int) error {
	var (
		bod []byte
		err error
	)
	if len(s.KeysInput) != 0 && s.Request.Body != nil {
		switch t := s.Request.Body.(type) {
		case string:
			bod, err = replaceInBytes(vals, []byte(t))
			if err != nil {
				return fmt.Errorf("Step %d.'%s' %s", stepNb, s.Name, err.Error())
			}
		case []byte:
			bod, err = replaceInBytes(vals, t)
			if err != nil {
				return fmt.Errorf("Step %d.'%s' %s", stepNb, s.Name, err.Error())
			}
		case url.Values:
			tmp := url.Values{}
			for k, v := range t {
				newK, err := replaceInString(vals, k)
				if err != nil {
					return fmt.Errorf("Step %d.'%s' %s", stepNb, s.Name, err.Error())
				}
				newV, err := replaceInString(vals, v[len(v)-1])
				if err != nil {
					return fmt.Errorf("Step %d.'%s' %s", stepNb, s.Name, err.Error())
				}
				tmp[newK] = []string{newV}
				bod = []byte(tmp.Encode())
			}
		case nil:
			return nil
		default:
			panic(fmt.Sprintf("Don't know how to handle the type %t", t))
		}
	}

	if bod != nil {
		s.Request.Body = bod
	}
	return nil
}

// ReplaceInHeader replaces the KeysInput in the request header
func (s *Step) ReplaceInHeader(vals map[string]interface{}, stepNb int) error {
	for k := range s.Request.Header {
		tpl, err := template.New(s.Name).Parse(s.Request.Header.Get(k))
		if err != nil {
			return err
		}
		var buffer bytes.Buffer
		if err := tpl.Execute(&buffer, vals); err != nil {
			return err
		}
		s.Request.Header.Set(k, buffer.String())
	}
	return nil
}

// ReplaceInURL replaces needed values in url
func (s *Step) ReplaceInURL(vals map[string]interface{}, stepNB int) error {
	tpl, err := template.New(s.Name).Parse(s.Request.URL)
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	if err := tpl.Execute(&buffer, vals); err != nil {
		return err
	}
	output := buffer.String()
	s.Request.URL = output
	return nil
}
