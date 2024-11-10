package audit

import (
	"bufio"
	"bytes"
	"io"
	"net/http"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
)

// drainBody reads all of b to memory and then returns two equivalent
// ReadClosers yielding the same bytes.
//
// It returns an error if the initial slurp of all bytes fails. It does not attempt
// to make the returned ReadClosers have identical error-matching behavior.
func drainBody(b io.ReadCloser) (r1, r2 io.ReadCloser, err error) {
	if b == nil || b == http.NoBody {
		// No copying needed. Preserve the magic sentinel meaning of NoBody.
		return http.NoBody, http.NoBody, nil
	}
	var buf bytes.Buffer
	if _, err = buf.ReadFrom(b); err != nil {
		return nil, b, err
	}
	if err = b.Close(); err != nil {
		return nil, b, err
	}
	return io.NopCloser(&buf), io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

// Request Audit request will drain the request body and log the request
// method, URL, headers, and body.
func Request(r *http.Request) {
	body1, body2, err := drainBody(r.Body)
	defer func(body2 io.ReadCloser) {
		err := body2.Close()
		if err != nil {

		}
	}(body2)
	if err != nil {
		logrus.Errorf("Error draining body: %v", err)
		return
	}
	r.Body = body1

	rawBody, err := io.ReadAll(body2)
	if err != nil {
		logrus.Errorf("Error reading body: %v", err)
		return
	}

	logrus.Debugf("Request: %s %s\nHeaders: %v\nBody: len(%d)\n Raw Body: %v\n",
		r.Method, r.URL.String(), r.Header, r.ContentLength, string(rawBody))
}

// Response Audit response will split the response body into two streams
// to audit the response asynchronously. This will allow streaming
// the response to the client without blocking and maintain the
// response body for auditing.
func Response(resp *http.Response) error {
	eng := engine.FromContext(resp.Request.Context())
	if eng == nil {
		return nil
	}

	originalBody := resp.Body
	pr, pw := io.Pipe()
	resp.Body = pr

	go func() {
		var respBodyBuf bytes.Buffer
		scanner := bufio.NewScanner(originalBody)
		scanner.Split(bufio.ScanBytes)
		for scanner.Scan() {
			b := scanner.Bytes()
			respBodyBuf.Write(b)
			_, err := pw.Write(b)
			if err != nil {
				return
			}
		}
		err := originalBody.Close()
		if err != nil {
			return
		}
		err = pw.Close()
		if err != nil {
			return
		}
		eng.ResponseCallback(resp, bytes.NewReader(respBodyBuf.Bytes()))
	}()

	return nil
}
