package copygateway

import (
	"encoding/xml"
	"net/http"
)

type S3Error struct {
	Code    string
	Message string
	Status  int
}

type xmlError struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func (e *S3Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

func writeS3Error(w http.ResponseWriter, s3err *S3Error) {
	if s3err == nil {
		s3err = errInternal("internal error")
	}
	status := s3err.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(xmlError{Code: s3err.Code, Message: s3err.Message})
}

func errMethodNotAllowed() *S3Error {
	return &S3Error{Code: "MethodNotAllowed", Message: "The specified method is not allowed against this resource.", Status: http.StatusMethodNotAllowed}
}

func errBadRequest(message string) *S3Error {
	return &S3Error{Code: "InvalidRequest", Message: message, Status: http.StatusBadRequest}
}

func errNoSuchBucket(bucket string) *S3Error {
	message := "The specified bucket does not exist."
	if bucket != "" {
		message = "The specified bucket does not exist: " + bucket
	}
	return &S3Error{Code: "NoSuchBucket", Message: message, Status: http.StatusNotFound}
}

func errNoSuchKey() *S3Error {
	return &S3Error{Code: "NoSuchKey", Message: "The specified key does not exist.", Status: http.StatusNotFound}
}

func errInvalidAccessKey() *S3Error {
	return &S3Error{Code: "InvalidAccessKeyId", Message: "The AWS access key id you provided does not exist in our records.", Status: http.StatusForbidden}
}

func errSignatureDoesNotMatch() *S3Error {
	return &S3Error{Code: "SignatureDoesNotMatch", Message: "The request signature we calculated does not match the signature you provided.", Status: http.StatusForbidden}
}

func errRequestTimeTooSkewed() *S3Error {
	return &S3Error{Code: "RequestTimeTooSkewed", Message: "The difference between the request time and the server's time is too large.", Status: http.StatusForbidden}
}

func errSlowDown() *S3Error {
	return &S3Error{Code: "SlowDown", Message: "Please reduce your request rate.", Status: http.StatusServiceUnavailable}
}

func errRequestTimeout() *S3Error {
	return &S3Error{Code: "RequestTimeout", Message: "The upstream operation did not complete within the timeout period.", Status: http.StatusGatewayTimeout}
}

func errNotImplemented(message string) *S3Error {
	return &S3Error{Code: "NotImplemented", Message: message, Status: http.StatusNotImplemented}
}

func errUpstream(message string) *S3Error {
	return &S3Error{Code: "InternalError", Message: message, Status: http.StatusBadGateway}
}

func errInternal(message string) *S3Error {
	return &S3Error{Code: "InternalError", Message: message, Status: http.StatusInternalServerError}
}
