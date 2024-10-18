package audit

import (
	"bufio"
	"bytes"
	"io"
	"net/http"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
)

func CopyRequestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return bodyBytes, nil
}

func AuditRequest(r *http.Request, body []byte) {
	logrus.Debugf("Request: %s %s\nHeaders: %v\nBody: len(%d)\n",
		r.Method, r.URL.String(), r.Header, len(string(body)))
}

// Audit response will split the response body into two streams
// to audit the response asynchronously. This will allow streaming
// the response to the client without blocking and maintain the
// response body for auditing.
func AuditResponse(resp *http.Response) error {
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
			pw.Write(b)
		}
		originalBody.Close()
		pw.Close()
		eng.HandleResponseAfterFinish(resp, respBodyBuf.Bytes())
	}()

	return nil
}
