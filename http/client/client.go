package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/gpahal/golib/retry"
	"github.com/labstack/echo/v4"
	"golang.org/x/net/publicsuffix"
)

const (
	defaultTimeout = 10 * time.Second
)

type Client struct {
	client    *http.Client
	retryOpts *retry.RetryOptions
	header    http.Header
}

type ClientOptions struct {
	BaseURL   string
	Timeout   time.Duration
	RetryOpts *retry.RetryOptions
	Header    http.Header
}

func NewClient(opts *ClientOptions) *Client {
	if opts == nil {
		opts = &ClientOptions{}
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	cookieJar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: timeout * 3 / 4,
			}).DialContext,
			TLSHandshakeTimeout: timeout * 3 / 4,
		},
		Jar: cookieJar,
	}

	return &Client{client: httpClient, retryOpts: opts.RetryOpts, header: opts.Header}
}

type Request struct {
	*http.Request
}

func (c *Client) NewRequest(method, url string, body io.Reader) (*Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	req.Header = c.header
	return &Request{Request: req}, nil
}

func (req *Request) GetHttpRequest() *http.Request {
	return req.Request
}

func (req *Request) SetBody(body io.Reader) {
	rc, ok := body.(io.ReadCloser)
	if !ok && body != nil {
		rc = io.NopCloser(body)
	}

	req.Body = rc
	if rc == nil {
		req.ContentLength = 0
		req.Body = http.NoBody
		req.GetBody = func() (io.ReadCloser, error) {
			return http.NoBody, nil
		}
		return
	}

	if rc != nil {
		switch v := body.(type) {
		case *bytes.Buffer:
			req.ContentLength = int64(v.Len())
			buf := v.Bytes()
			req.GetBody = func() (io.ReadCloser, error) {
				r := bytes.NewReader(buf)
				return io.NopCloser(r), nil
			}
		case *bytes.Reader:
			req.ContentLength = int64(v.Len())
			snapshot := *v
			req.GetBody = func() (io.ReadCloser, error) {
				r := snapshot
				return io.NopCloser(&r), nil
			}
		case *strings.Reader:
			req.ContentLength = int64(v.Len())
			snapshot := *v
			req.GetBody = func() (io.ReadCloser, error) {
				r := snapshot
				return io.NopCloser(&r), nil
			}
		default:
			if body != http.NoBody {
				req.ContentLength = -1
			}
		}

		if req.ContentLength == 0 {
			req.Body = http.NoBody
			req.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
		}
	}
}

func (req *Request) WithJsonBody(body any) error {
	req.Header.Set("Content-Type", "application/json")
	bs, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req.SetBody(bytes.NewReader(bs))
	return nil
}

func (req *Request) SetFormBody(data url.Values) {
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBody(strings.NewReader(data.Encode()))
}

type Response struct {
	*http.Response
}

func (resp *Response) GetHttpResponse() *http.Response {
	return resp.Response
}

func (resp *Response) GetStringBody() (string, error) {
	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(bs), nil
}

func (resp *Response) BindJsonBody(v any) error {
	err := json.NewDecoder(resp.Body).Decode(v)
	if err == nil {
		return nil
	}

	if ute, ok := err.(*json.UnmarshalTypeError); ok {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Unmarshal type error: expected=%v, got=%v, field=%v, offset=%v", ute.Type, ute.Value, ute.Field, ute.Offset)).SetInternal(err)
	} else if se, ok := err.(*json.SyntaxError); ok {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Syntax error: offset=%v, error=%v", se.Offset, se.Error())).SetInternal(err)
	}
	return err
}

func (c *Client) Do(req *Request) (*Response, error) {
	var resp *Response
	err := retry.Do(func() error {
		httpResp, err := c.client.Do(req.Request)
		if err != nil {
			return err
		}

		resp = &Response{Response: httpResp}
		return nil
	}, c.retryOpts)

	return resp, err
}